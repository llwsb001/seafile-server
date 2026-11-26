package seafserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) dirIDByPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, path := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || path == nil {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	if !validRepoID(*repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errInternal, "Get repo error")
	}
	dir, rpcErr := r.directoryByPath(repo, repo.RootID, formatPath(*path))
	if rpcErr != nil {
		if rpcErr.Code == errPathMissing {
			return nil, nil
		}
		return nil, rpcErr
	}
	return dir.DirID, nil
}

func (r *Reader) direntByPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, path := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || path == nil {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	if !validRepoID(*repoID) {
		return nil, rpcError(errBadArgs, "invalid repo id")
	}
	formatted := formatPath(*path)
	if formatted == "/" {
		return nil, rpcError(errBadArgs, "invalid path")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errInternal, "Get repo error")
	}
	slash := strings.LastIndexByte(formatted, '/')
	dir, rpcErr := r.directoryByPath(repo, repo.RootID, formatted[:slash])
	if rpcErr != nil {
		if rpcErr.Code == errPathMissing {
			return nil, nil
		}
		return nil, rpcErr
	}
	index := findEntry(dir.Entries, formatted[slash+1:])
	if index < 0 {
		return nil, nil
	}
	entry := dir.Entries[index]
	object := direntObject(entry, dir.Version)
	object["modifier"] = optionalString(entry.Modifier)
	return object, nil
}

func (r *Reader) commitList(config *ServerConfig) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		repoID := stringArgument(args, 0)
		if repoID == nil || !validRepoID(*repoID) {
			return nil, rpcError(errBadArgs, "Invalid repo id")
		}
		repo, err := r.Repo(ctx, *repoID)
		if err != nil {
			return nil, rpcError(501, "No such repository")
		}
		head, err := r.Commit(*repoID, repo.HeadCommitID)
		if err != nil {
			return nil, rpcError(507, "Failed to list commits")
		}

		truncate := r.historyTruncateTime(ctx, *repoID, config)
		offset, limit := max(0, intArgument(args, 1)), intArgument(args, 2)
		// C compares the visit count against an end offset held in a signed int.
		if limit > 0 && int32(offset)+int32(limit) < 0 {
			return nil, nil
		}
		return r.collectCommits(*repoID, head, offset, limit, truncate), nil
	}
}

func (r *Reader) collectCommits(repoID string, head *commitmgr.Commit, offset, limit int, truncate int64) []map[string]any {
	var result []map[string]any
	queue := newCommitQueue(r, repoID, head)
	for index := 0; len(queue.pending) > 0; index++ {
		commit := queue.pop()
		if index >= offset {
			result = append(result, commitObject(commit))
			if limit > 0 && len(result) == limit {
				break
			}
		}
		if truncate == 0 || (truncate > 0 && index > 0 && commit.Ctime <= truncate) {
			continue
		}
		queue.addParents(commit)
	}
	return result
}

type commitQueue struct {
	reader  *Reader
	repoID  string
	visited map[string]bool
	pending []*commitmgr.Commit
}

func newCommitQueue(r *Reader, repoID string, commits ...*commitmgr.Commit) *commitQueue {
	visited := make(map[string]bool, len(commits))
	for _, commit := range commits {
		visited[commit.CommitID] = true
	}
	return &commitQueue{reader: r, repoID: repoID, visited: visited, pending: commits}
}

func (q *commitQueue) pop() *commitmgr.Commit {
	next := q.pending[0]
	q.pending[0] = nil
	q.pending = q.pending[1:]
	return next
}

func (q *commitQueue) addParents(commit *commitmgr.Commit) error {
	var loadErr error
	for _, parent := range []commitmgr.String{commit.ParentID, commit.SecondParentID} {
		if !parent.Valid || q.visited[parent.String] {
			continue
		}
		next, err := q.reader.Commit(q.repoID, parent.String)
		if err != nil {
			loadErr = err
			continue
		}
		q.visited[parent.String] = true
		// Like the C queue, visit newer commits first and prepend ties.
		position := sort.Search(len(q.pending), func(i int) bool { return q.pending[i].Ctime <= next.Ctime })
		q.pending = append(q.pending, nil)
		copy(q.pending[position+1:], q.pending[position:])
		q.pending[position] = next
	}
	return loadErr
}
