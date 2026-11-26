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
	var days int
	err := s.db.QueryRowContext(ctx, "SELECT days FROM RepoHistoryLimit WHERE repo_id = ?", repo.StoreID).Scan(&days)
	if errors.Is(err, sql.ErrNoRows) {
		days = s.defaultDays
		var value string
		const query = "SELECT value FROM SeafileConf WHERE cfg_group = 'history' AND cfg_key = 'keep_days'"
		if s.db.QueryRowContext(ctx, query).Scan(&value) == nil {
			days, err = strconv.Atoi(value)
			if err != nil {
				days = -1
			}
		}
	} else if err != nil {
		days = -1
	}
	if days == 0 {
		return 0
	}
	var since int64
	if s.db.QueryRowContext(ctx, "SELECT timestamp FROM RepoValidSince WHERE repo_id = ?", repo.ID).Scan(&since) != nil {
		since = -1
	}
	if days < 0 {
		return since
	}
	return max(since, now.Unix()-int64(days)*24*60*60)
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
