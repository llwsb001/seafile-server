package seafserver

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

type deletedEntry struct {
	commit   string
	base     string
	dirent   *fsmgr.SeafDirent
	deleted  int32
	fileSize int64
}

type deletedScan struct {
	reader  *Reader
	repo    *repomgr.Repo
	base    string
	entries map[string]deletedEntry
}

func (r *Reader) deletedEntries(config *ServerConfig) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		repoID := stringArgument(args, 0)
		if repoID == nil {
			return nil, rpcError(errBadArgs, "Bad arguments")
		}
		if !validRepoID(*repoID) {
			return nil, rpcError(errBadArgs, "Invalid repo id")
		}
		cutoff := r.historyTruncateTime(ctx, *repoID, config)
		if cutoff == 0 {
			return []map[string]any{deletedScanCursor(nil)}, nil
		}
		showSince := int64(-1)
		if days := intArgument(args, 1); days > 0 {
			showSince = historyDate(time.Now().Unix(), days)
		}
		cutoff = max(cutoff, showSince)
		repo, err := r.Repo(ctx, *repoID)
		if err != nil {
			return nil, rpcError(errGeneral, "Invalid repo id")
		}
		head, err := r.Commit(*repoID, repo.HeadCommitID)
		if err != nil {
			return nil, rpcError(errInternal, "Internal error")
		}
		queue, err := r.deletedCommitQueue(*repoID, head, stringArgument(args, 3))
		if err != nil {
			return nil, rpcError(errInternal, "Internal error")
		}
		base := "/"
		if path := stringArgument(args, 2); path != nil {
			base = formatPath(*path)
			if base != "/" {
				base += "/"
			}
		}
		scan := deletedScan{reader: r, repo: repo, base: base, entries: make(map[string]deletedEntry)}
		limit := max(1, intArgument(args, 4))
		for visited := 0; len(queue.pending) > 0 && visited < limit; {
			commit := queue.pop()
			if commit.Ctime <= cutoff {
				continue
			}
			if err := scan.collect(commit); err != nil {
				return nil, rpcError(errInternal, "Internal error")
			}
			if err := queue.addParents(commit); err != nil {
				return nil, rpcError(errInternal, "Internal error")
			}
			visited++
		}
		return scan.results(head, queue.pending), nil
	}
}

func (r *Reader) deletedCommitQueue(repoID string, head *commitmgr.Commit, cursor *string) (*commitQueue, error) {
	if cursor == nil {
		return newCommitQueue(r, repoID, head), nil
	}
	var ids []json.RawMessage
	if err := json.Unmarshal([]byte(*cursor), &ids); err != nil {
		return nil, err
	}
	pending := make([]*commitmgr.Commit, 0, len(ids))
	// C prepends cursor items before sorting timestamps, reversing tied items.
	for i := len(ids) - 1; i >= 0; i-- {
		id := stringArgument(ids, i)
		if id == nil || len(*id) != 40 {
			continue
		}
		commit, err := r.Commit(repoID, *id)
		if err != nil {
			return nil, err
		}
		pending = append(pending, commit)
	}
	if len(pending) == 0 {
		return nil, errors.New("empty deleted entries cursor")
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].Ctime > pending[j].Ctime })
	return newCommitQueue(r, repoID, pending...), nil
}

func (s *deletedScan) collect(child *commitmgr.Commit) error {
	if !child.ParentID.Valid ||
		!(strings.Contains(child.Desc, `Deleted "`) ||
			strings.Contains(child.Desc, `Removed directory "`) ||
			strings.Contains(child.Desc, `Removed "`)) {
		return nil
	}
	for _, id := range []commitmgr.String{child.ParentID, child.SecondParentID} {
		if !id.Valid {
			continue
		}
		parent, err := s.reader.Commit(s.repo.ID, id.String)
		if err != nil {
			return err
		}
		// The requested directory has to exist in both commits. Deletion of
		// the directory itself is reported from its parent path.
		after, afterErr := s.reader.directoryByPath(s.repo, child.RootID, s.base)
		before, beforeErr := s.reader.directoryByPath(s.repo, parent.RootID, s.base)
		if afterErr != nil || beforeErr != nil {
			continue
		}
		if err := s.compareTrees(after, before, s.base, child, parent); err != nil {
			return err
		}
	}
	return nil
}

func (s *deletedScan) compareTrees(after, before *fsmgr.SeafDir, base string, child, parent *commitmgr.Commit) error {
	if s.repo.Version == 0 {
		for _, dir := range []*fsmgr.SeafDir{after, before} {
			sort.SliceStable(dir.Entries, func(i, j int) bool { return dir.Entries[i].Name > dir.Entries[j].Name })
		}
	}
	current := 0
	for _, old := range before.Entries {
		for current < len(after.Entries) && after.Entries[current].Name > old.Name {
			current++
		}
		if current == len(after.Entries) || after.Entries[current].Name != old.Name {
			s.add(base, old, child.Ctime, parent.CommitID)
			continue
		}
		now := after.Entries[current]
		current++
		if now.Mode&syscall.S_IFMT != old.Mode&syscall.S_IFMT {
			s.add(base, old, child.Ctime, parent.CommitID)
			continue
		}
		if fsmgr.IsDir(now.Mode) && now.ID != old.ID {
			afterDir, err := s.reader.Directory(s.repo, now.ID)
			if err != nil {
				return err
			}
			beforeDir, err := s.reader.Directory(s.repo, old.ID)
			if err != nil {
				return err
			}
			if err := s.compareTrees(afterDir, beforeDir, base+now.Name+"/", child, parent); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *deletedScan) add(base string, dirent *fsmgr.SeafDirent, deleted int64, parent string) {
	path := base + dirent.Name
	if _, exists := s.entries[path]; exists {
		return
	}
	entry := deletedEntry{commit: parent, base: base, dirent: dirent, deleted: int32(deleted)}
	if fsmgr.IsRegular(dirent.Mode) {
		file, err := s.reader.File(s.repo, dirent.ID)
		if err != nil {
			return
		}
		entry.fileSize = int64(file.FileSize)
	}
	s.entries[path] = entry
}

func (s *deletedScan) results(head *commitmgr.Commit, pending []*commitmgr.Commit) []map[string]any {
	paths := make([]string, 0, len(s.entries))
	for path, entry := range s.entries {
		current, _ := s.reader.entryAtHistoryPath(s.repo, head.RootID, path)
		if current != nil && utils.IsObjectIDValid(current.ID) &&
			current.Mode&syscall.S_IFMT == entry.dirent.Mode&syscall.S_IFMT {
			continue
		}
		paths = append(paths, path)
	}
	// Seahub displays the newest removals first. Paths order items sharing a timestamp.
	sort.Slice(paths, func(i, j int) bool {
		a, b := s.entries[paths[i]], s.entries[paths[j]]
		if a.deleted != b.deleted {
			return a.deleted > b.deleted
		}
		return paths[i] < paths[j]
	})
	result := make([]map[string]any, 0, len(paths)+1)
	for _, path := range paths {
		entry := s.entries[path]
		result = append(result, map[string]any{
			"commit-id": entry.commit, "obj-id": entry.dirent.ID, "obj-name": entry.dirent.Name,
			"basedir": entry.base, "mode": entry.dirent.Mode, "delete-time": entry.deleted,
			"file-size": entry.fileSize, "scan-stat": nil,
		})
	}
	return append(result, deletedScanCursor(pending))
}

func deletedScanCursor(pending []*commitmgr.Commit) map[string]any {
	var cursor any
	if len(pending) > 0 {
		ids := make([]string, len(pending))
		for i, commit := range pending {
			ids[i] = commit.CommitID
		}
		payload, _ := json.Marshal(ids)
		cursor = string(payload)
	}
	return map[string]any{
		"commit-id": nil, "obj-id": nil, "obj-name": nil, "basedir": nil, "mode": 0,
		"delete-time": 0, "file-size": 0, "scan-stat": cursor,
	}
}
