package seafserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

const errBadDirID = 511

func (r *Reader) fileIDByCommitAndPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, commitID, path := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if repoID == nil || !validRepoID(*repoID) || commitID == nil || path == nil {
		return nil, rpcError(errBadArgs, "Arguments should not be empty")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errBadArgs, "Bad repo id")
	}
	commit, err := r.Commit(*repoID, *commitID)
	if err != nil {
		return nil, rpcError(errBadArgs, "bad commit id")
	}
	entry, rpcErr := r.entryAtHistoryPath(repo, commit.RootID, formatPath(*path))
	if rpcErr != nil {
		return nil, rpcErr
	}
	if entry == nil || fsmgr.IsDir(entry.Mode) || !utils.IsObjectIDValid(entry.ID) {
		return nil, nil
	}
	return entry.ID, nil
}

// entryAtHistoryPath looks up an entry below rootID using a path from formatPath.
func (r *Reader) entryAtHistoryPath(repo *repomgr.Repo, rootID, path string) (*fsmgr.SeafDirent, *searpc.RPCError) {
	if path == "/" {
		return nil, nil
	}
	slash := strings.LastIndexByte(path, '/')
	parent, name := path[:slash], path[slash+1:]
	dir, rpcErr := r.directoryByPath(repo, rootID, parent)
	if rpcErr != nil {
		if rpcErr.Code == errPathMissing {
			return nil, nil
		}
		return nil, rpcErr
	}
	index := findEntry(dir.Entries, name)
	if index < 0 {
		return nil, nil
	}
	return dir.Entries[index], nil
}

func (r *Reader) dirIDByCommitAndPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	dir, rpcErr := r.directoryAtCommit(ctx, args)
	if rpcErr != nil {
		if rpcErr.Code == errPathMissing {
			return nil, nil
		}
		return nil, rpcErr
	}
	return dir.DirID, nil
}

func (r *Reader) directoryAtCommit(ctx context.Context, args []json.RawMessage) (*fsmgr.SeafDir, *searpc.RPCError) {
	repoID, commitID, path := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if repoID == nil || commitID == nil || path == nil {
		return nil, rpcError(errBadArgs, "Args can't be NULL")
	}
	if !validRepoID(*repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	if !utils.IsObjectIDValid(*commitID) {
		return nil, rpcError(errBadArgs, "Invalid commit id")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errBadArgs, "Bad repo id")
	}
	commit, err := r.Commit(*repoID, *commitID)
	if err != nil {
		return nil, rpcError(errBadCommit, "No such commit")
	}
	return r.directoryByPath(repo, commit.RootID, formatPath(*path))
}

func (r *Reader) listDirByID(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, dirID := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || !validRepoID(*repoID) {
		return nil, rpcError(errBadDirID, "Bad repo id")
	}
	if dirID == nil || !utils.IsObjectIDValid(*dirID) {
		return nil, rpcError(errBadDirID, "Bad dir id")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errBadArgs, "Bad repo id")
	}
	dir, err := r.Directory(repo, *dirID)
	if err != nil {
		return nil, rpcError(errBadDirID, "Bad dir id")
	}
	var entries []map[string]any
	for _, entry := range directoryEntryPage(dir, intArgument(args, 2), intArgument(args, 3)) {
		if utils.IsObjectIDValid(entry.ID) {
			value := direntObject(entry, dir.Version)
			value["permission"] = ""
			entries = append(entries, value)
		}
	}
	return entries, nil
}

func directoryEntryPage(dir *fsmgr.SeafDir, offset, limit int) []*fsmgr.SeafDirent {
	sort.SliceStable(dir.Entries, func(i, j int) bool {
		a, b := dir.Entries[i], dir.Entries[j]
		if fsmgr.IsDir(a.Mode) && fsmgr.IsRegular(b.Mode) {
			return true
		}
		if fsmgr.IsRegular(a.Mode) && fsmgr.IsDir(b.Mode) {
			return false
		}
		return asciiLower(a.Name) < asciiLower(b.Name)
	})
	offset = max(0, offset)
	end := len(dir.Entries)
	if limit > 0 {
		end = min(end, int(int32(offset)+int32(limit)))
	}
	if offset >= end {
		return nil
	}
	return dir.Entries[offset:end]
}
