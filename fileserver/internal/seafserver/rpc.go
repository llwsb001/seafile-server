package seafserver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

// RPCService is the service name used by Seahub and the Go fileserver.
const RPCService = "seafserv-threaded-rpcserver"

const (
	errBadCommit   = 502
	errBadArgs     = 503
	errInternal    = 504
	errDirMissing  = 515
	errPathMissing = 516
)

// RPCHandlers exposes the repository reads using the existing Searpc names.
func (r *Reader) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_get_repo":            r.getRepo,
		"seafile_get_commit":          r.getCommit,
		"seafile_list_dir_by_path":    r.listDirByPath,
		"seafile_get_file_id_by_path": r.fileIDByPath,
	}
}

func stringArgument(args []json.RawMessage, index int) *string {
	var result *string
	if index < len(args) {
		if err := json.Unmarshal(args[index], &result); err != nil {
			return nil
		}
	}
	return result
}

func rpcError(code int, message string) *searpc.RPCError {
	return &searpc.RPCError{Code: code, Message: message}
}

func (r *Reader) getRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if !validRepoID(*id) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	repo, err := r.Repo(ctx, *id)
	if err != nil {
		return nil, nil
	}
	return repoObject(repo), nil
}

func (r *Reader) getCommit(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil || !validRepoID(*repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	// The server stores commits below the repository ID for all supported versions.
	commitID := stringArgument(args, 2)
	if commitID == nil || !utils.IsObjectIDValid(*commitID) {
		return nil, rpcError(errBadArgs, "Invalid commit id")
	}
	commit, err := r.Commit(*repoID, *commitID)
	if err != nil {
		return nil, nil
	}
	return commitObject(commit), nil
}

func (r *Reader) listDirByPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
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
	dir, rpcErr := r.directoryByPath(repo, commit.RootID, formatPath(*path))
	if rpcErr != nil {
		return nil, rpcErr
	}
	var entries []map[string]any
	for _, entry := range dir.Entries {
		entries = append(entries, direntObject(entry, dir.Version))
	}
	return entries, nil
}

func (r *Reader) fileIDByPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
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
	formatted := formatPath(*path)
	if formatted == "/" {
		return nil, nil
	}
	slash := strings.LastIndexByte(formatted, '/')
	parent, name := formatted[:slash], formatted[slash+1:]
	dir, rpcErr := r.directoryByPath(repo, repo.RootID, parent)
	if rpcErr != nil {
		if rpcErr.Code == errPathMissing {
			return nil, nil
		}
		return nil, rpcErr
	}
	for _, entry := range dir.Entries {
		if entry.Name == name && !fsmgr.IsDir(entry.Mode) {
			return entry.ID, nil
		}
	}
	return nil, nil
}

func formatPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if trimmed := strings.TrimRight(path, "/"); trimmed != "" {
		return trimmed
	}
	return "/"
}

func (r *Reader) directoryByPath(repo *repomgr.Repo, rootID, path string) (*fsmgr.SeafDir, *searpc.RPCError) {
	dir, err := r.Directory(repo, rootID)
	if err != nil {
		return nil, rpcError(errDirMissing, "directory is missing")
	}
	for _, name := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' }) {
		var nextID string
		for _, entry := range dir.Entries {
			if entry.Name == name && fsmgr.IsDir(entry.Mode) {
				nextID = entry.ID
				break
			}
		}
		if nextID == "" {
			return nil, rpcError(errPathMissing, "Path does not exists "+path)
		}
		dir, err = r.Directory(repo, nextID)
		if err != nil {
			return nil, rpcError(errDirMissing, "directory is missing")
		}
	}
	return dir, nil
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func creatorName(value string) any {
	return optionalString(strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value))
}

func repoObject(repo *repomgr.Repo) map[string]any {
	object := map[string]any{
		"id": repo.ID, "name": repo.Name, "desc": repo.Desc, "version": repo.Version,
		"last-modify": repo.LastModificationTime, "size": repo.Size, "file-count": repo.FileCount,
		"last-modifier": creatorName(repo.LastModifier), "head-cmmt-id": repo.HeadCommitID,
		"root": repo.RootID, "status": repo.Status, "repo-type": nil,
		"repo-id": repo.ID, "repo-name": repo.Name, "repo-desc": repo.Desc, "last-modified": repo.LastModificationTime,
		"encrypted": repo.IsEncrypted, "magic": repo.Magic, "enc-version": repo.EncVersion,
		"random-key": nil, "salt": nil, "pwd-hash": repo.PwdHash,
		"pwd-hash-algo": optionalString(repo.PwdHashAlgo), "pwd-hash-params": optionalString(repo.PwdHashParams),
		"worktree": nil, "relay-id": nil, "last-sync-time": 0, "auto-sync": false, "worktree-invalid": false,
		"is-virtual": repo.VirtualInfo != nil, "origin-repo-id": nil, "origin-repo-name": nil, "origin-path": nil,
		"is-original-owner": false, "virtual-perm": nil, "store-id": repo.StoreID,
		"is-corrupted": repo.IsCorrupted, "repaired": repo.Repaired,
		"share-type": nil, "permission": nil, "user": nil, "group-id": 0, "group-name": nil, "is-shared": false,
	}
	if repo.Type.Valid {
		object["repo-type"] = repo.Type.String
	}
	if repo.VirtualInfo != nil {
		object["origin-repo-id"] = repo.VirtualInfo.OriginRepoID
		object["origin-path"] = repo.VirtualInfo.Path
	}
	if repo.IsEncrypted && repo.EncVersion >= 2 {
		object["random-key"] = repo.RandomKey
	}
	if repo.IsEncrypted && repo.EncVersion >= 3 {
		object["salt"] = repo.Salt
	}
	return object
}

func commitObject(commit *commitmgr.Commit) map[string]any {
	return map[string]any{
		"id": commit.CommitID, "creator-name": creatorName(commit.CreatorName), "creator": commit.CreatorID,
		"desc": commit.Desc, "ctime": commit.Ctime, "parent-id": commit.ParentID, "second-parent-id": commit.SecondParentID,
		"repo-id": commit.RepoID, "root-id": commit.RootID, "version": commit.Version,
		"new-merge": commit.NewMerge != 0, "conflict": commit.Conflict != 0,
		"rev-file-id": nil, "rev-file-size": 0, "rev-renamed-old-path": nil,
		"device-name": optionalString(commit.DeviceName), "client-version": optionalString(commit.ClientVersion), "next-start-commit": nil,
	}
}

func direntObject(entry *fsmgr.SeafDirent, version int) map[string]any {
	return map[string]any{
		"obj-id": entry.ID, "obj-name": entry.Name, "mode": entry.Mode, "version": version,
		"mtime": entry.Mtime, "size": entry.Size, "modifier": nil, "permission": nil,
		"is-locked": false, "lock-owner": nil, "lock-time": 0, "is-shared": false,
	}
}
