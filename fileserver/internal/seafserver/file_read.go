package seafserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

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

		days := r.historyLimit(ctx, *repoID, config)
		truncate := int64(-1)
		if days == 0 {
			truncate = 0
		} else {
			_ = r.db.QueryRowContext(ctx, "SELECT timestamp FROM RepoValidSince WHERE repo_id = ?", *repoID).Scan(&truncate)
			if days > 0 {
				truncate = max(truncate, time.Now().Unix()-int64(days)*24*60*60)
			}
		}
		offset, limit := max(0, intArgument(args, 1)), intArgument(args, 2)
		return r.collectCommits(head, offset, limit, truncate), nil
	}
}

func (r *Reader) collectCommits(head *commitmgr.Commit, offset, limit int, truncate int64) []map[string]any {
	var result []map[string]any
	visited := map[string]bool{head.CommitID: true}
	pending := []*commitmgr.Commit{head}
	for index := 0; len(pending) > 0; index++ {
		commit := pending[0]
		pending = pending[1:]
		if index >= offset {
			result = append(result, commitObject(commit))
			if limit > 0 && len(result) == limit {
				break
			}
		}
		if truncate == 0 || (truncate > 0 && index > 0 && commit.Ctime <= truncate) {
			continue
		}
		for _, parent := range []commitmgr.String{commit.ParentID, commit.SecondParentID} {
			if !parent.Valid || visited[parent.String] {
				continue
			}
			visited[parent.String] = true
			next, err := r.Commit(commit.RepoID, parent.String)
			if err != nil {
				continue
			}
			// Like the C queue, visit newer commits first and prepend ties.
			position := sort.Search(len(pending), func(i int) bool { return pending[i].Ctime <= next.Ctime })
			pending = append(pending, nil)
			copy(pending[position+1:], pending[position:])
			pending[position] = next
		}
	}
	return result
}
