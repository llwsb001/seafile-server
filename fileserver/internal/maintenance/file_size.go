package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

func (s *Store) historyCutoff(ctx context.Context, repo Repo, now time.Time) int64 {
	cutoff, err := s.readHistoryCutoff(ctx, repo, now)
	if err != nil {
		return -1
	}
	return cutoff
}

func (s *Store) readHistoryCutoff(ctx context.Context, repo Repo, now time.Time) (int64, error) {
	var days int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(days, 0) FROM RepoHistoryLimit WHERE repo_id = ?", repo.StoreID).Scan(&days)
	if errors.Is(err, sql.ErrNoRows) {
		days = s.defaultDays
		var value sql.NullString
		const query = "SELECT value FROM SeafileConf WHERE cfg_group = 'history' AND cfg_key = 'keep_days'"
		err = s.db.QueryRowContext(ctx, query).Scan(&value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if value.Valid {
			text := strings.TrimSpace(value.String)
			parsed, err := strconv.ParseInt(text, 10, 64)
			days = int(int32(parsed))
			if err != nil && text != "" && !errors.Is(err, strconv.ErrRange) {
				days = -1
			}
		}
	} else if err != nil {
		return 0, err
	}
	if days == 0 {
		return 0, nil
	}
	var since int64
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(timestamp, 0) FROM RepoValidSince WHERE repo_id = ?", repo.ID).Scan(&since)
	if errors.Is(err, sql.ErrNoRows) {
		since = -1
	} else if err != nil {
		return 0, err
	}
	if days < 0 {
		return since, nil
	}
	// C multiplies the signed 32-bit day count before subtracting from Unix time.
	return max(since, now.Unix()-int64(int32(days)*86400)), nil
}

func (c *repoCheck) reportFileSize(ctx context.Context, path, id string) {
	const format = "Repo[%s] file %s is damaged, as its size does not match the expected value."
	commit := c.commitThatUpdatedFile(ctx, path, id)
	if commit == nil {
		c.log(format, c.repo.ID, path)
		return
	}
	client := commit.ClientVersion
	if client == "" {
		client = "(null)"
	}
	c.log(format+" It was uploaded via %s (commit id is %s), commit desc is %s, commit time is %s.",
		c.repo.ID, path, client, commit.CommitID, commit.Desc, formatMaintenanceTime(commit.Ctime))
}

func (c *repoCheck) commitThatUpdatedFile(ctx context.Context, path, fileID string) *commitmgr.Commit {
	if c.head == nil {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	seen := map[string]bool{c.head.CommitID: true}
	queue := []*commitmgr.Commit{c.head}
	lastMatch := c.head
	for len(queue) != 0 && ctx.Err() == nil {
		commit := queue[0]
		queue = queue[1:]
		if !c.pathHasFile(commit.RootID, parts, fileID) {
			return lastMatch
		}
		lastMatch = commit
		if c.truncateAt == 0 || (commit != c.head && commit.Ctime < c.truncateAt) {
			continue
		}
		for _, id := range []string{commit.ParentID.ValueOrZero(), commit.SecondParentID.ValueOrZero()} {
			if id == "" || seen[id] {
				continue
			}
			parent, err := c.store.Commit(c.repo, id)
			if err != nil {
				return nil
			}
			seen[id] = true
			queue = slices.Insert(queue, 0, parent)
		}
		slices.SortStableFunc(queue, newestCommitFirst)
	}
	return nil
}

func (c *repoCheck) pathHasFile(rootID string, parts []string, fileID string) bool {
	dirID := rootID
	for i, name := range parts {
		dir, err := fsmgr.ReadSeafdir(c.store.fs, c.repo.StoreID, dirID, c.version)
		if err != nil {
			return false
		}
		dirID = ""
		for _, entry := range dir.Entries {
			if entry.Name == name {
				dirID = entry.ID
				break
			}
		}
		if i == len(parts)-1 {
			return dirID == fileID
		}
		if dirID == "" {
			return false
		}
	}
	return false
}
