package seafserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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
func (r *Reader) RPCHandlers(config *ServerConfig) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_get_repo":            r.getRepo,
		"seafile_get_commit":          r.getCommit,
		"seafile_get_commit_list":     r.commitList(config),
		"seafile_diff":                r.commitDiff,
		"seafile_list_file_revisions": r.fileRevisions(config),
		"get_deleted":                 r.deletedEntries(config),
		"seafile_list_dir_by_path":    r.listDirByPath,
		"seafile_list_dir":            r.listDirByID,
		"seafile_get_file_id_by_path": r.fileIDByPath,
		"seafile_get_dir_id_by_path":  r.dirIDByPath,
		"seafile_get_dirent_by_path":  r.direntByPath,
		"seafile_get_repo_owner":      r.getRepoOwner,
		"seafile_is_repo_owner":       r.isRepoOwner,
		"seafile_list_owned_repos":    r.listOwnedRepos,
		"get_repo_status":             r.getRepoStatus,
		"get_repo_history_limit":      r.getRepoHistoryLimit(config),
		"get_file_count_info_by_path": r.fileCountInfoByPath,
		"get_trash_repo_list":         r.listTrashRepos,
		"get_trash_repos_by_owner":    r.listTrashReposByOwner,
		"get_trash_repo_owner":        r.getTrashRepoOwner,

		"seafile_get_file_id_by_commit_and_path": r.fileIDByCommitAndPath,
		"seafile_get_dir_id_by_commit_and_path":  r.dirIDByCommitAndPath,

		"seafile_list_share_repos":          r.listShareRepos,
		"seafile_list_repo_shared_to":       r.listRepoSharedTo,
		"get_shared_users_by_repo":          r.sharedUsersByRepo,
		"org_get_shared_users_by_repo":      r.orgSharedUsersByRepo,
		"get_shared_repo_by_path":           r.personalSharedRepoByPath,
		"seafile_list_repo_shared_group":    r.listRepoSharedGroup,
		"seafile_get_shared_groups_by_repo": r.sharedGroupsByRepo,
		"seafile_get_group_repoids":         r.groupRepoIDs,
		"get_group_repo_owner":              r.groupRepoOwner,
		"seafile_get_repos_by_group":        r.listReposByGroup,
		"get_group_repos_by_owner":          r.listGroupReposByOwner,
		"seafile_list_file_blocks":          r.listFileBlocks,
		"seafile_check_repo_blocks_missing": r.checkRepoBlocksMissing,
		"repo_has_been_shared":              r.repoHasBeenShared,
		"list_inner_pub_repos":              r.listInnerPublicRepos,
		"list_inner_pub_repos_by_owner":     r.listInnerPublicReposByOwner,
		"count_inner_pub_repos":             r.countInnerPublicRepos,
		"is_inner_pub_repo":                 r.isInnerPublicRepo,
	}
}

func (r *Reader) fileCountInfoByPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, path := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || path == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errGeneral, "Library not exists")
	}
	dir, rpcErr := r.directoryByPath(repo, repo.RootID, *path)
	if rpcErr != nil {
		return nil, rpcError(errBadArgs, "Bad path")
	}
	counts, err := fsmgr.ReadFileCountInfo(r.fs, repo.StoreID, dir.DirID, repo.Version)
	if err != nil {
		return nil, nil
	}
	return map[string]any{
		"file-count": counts.FileCount, "dir-count": counts.DirCount, "size": counts.Size,
	}, nil
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
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY {
			return nil, rpcError(errInternal, "Repository database is busy")
		}
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
	dir, rpcErr := r.directoryAtCommit(ctx, args)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var entries []map[string]any
	for _, entry := range dir.Entries {
		if utils.IsObjectIDValid(entry.ID) {
			entries = append(entries, direntObject(entry, dir.Version))
		}
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
	return optionalString(asciiLower(value))
}

func asciiLower(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

// newRepoObject supplies the Vala defaults used by both full and listing RPCs.
func newRepoObject(id string) map[string]any {
	return map[string]any{
		"id": id, "name": nil, "desc": nil, "version": 0,
		"last-modify": 0, "size": 0, "file-count": 0, "last-modifier": nil, "head-cmmt-id": nil,
		"root": nil, "status": 0, "repo-type": nil,
		"repo-id": id, "repo-name": nil, "repo-desc": nil, "last-modified": 0,
		"encrypted": false, "magic": nil, "enc-version": 0,
		"random-key": nil, "salt": nil, "pwd-hash": nil, "pwd-hash-algo": nil, "pwd-hash-params": nil,
		"worktree": nil, "relay-id": nil, "last-sync-time": 0, "auto-sync": false, "worktree-invalid": false,
		"is-virtual": false, "origin-repo-id": nil, "origin-repo-name": nil, "origin-path": nil,
		"is-original-owner": false, "virtual-perm": nil, "store-id": id,
		"is-corrupted": false, "repaired": false,
		"share-type": nil, "permission": nil, "user": nil, "group-id": 0, "group-name": nil, "is-shared": false,
	}
}

func repoObject(repo *repomgr.Repo) map[string]any {
	object := newRepoObject(repo.ID)
	object["name"], object["repo-name"] = repo.Name, repo.Name
	object["desc"], object["repo-desc"] = repo.Desc, repo.Desc
	object["version"], object["last-modifier"] = repo.Version, creatorName(repo.LastModifier)
	object["last-modify"], object["last-modified"] = repo.LastModificationTime, repo.LastModificationTime
	object["size"], object["file-count"], object["status"] = repo.Size, repo.FileCount, repo.Status
	object["root"], object["head-cmmt-id"], object["store-id"] = repo.RootID, repo.HeadCommitID, repo.StoreID
	object["encrypted"], object["enc-version"], object["magic"] = repo.IsEncrypted, repo.EncVersion, repo.Magic
	object["pwd-hash"] = repo.PwdHash
	object["pwd-hash-algo"], object["pwd-hash-params"] = optionalString(repo.PwdHashAlgo), optionalString(repo.PwdHashParams)
	object["is-corrupted"], object["repaired"] = repo.IsCorrupted, repo.Repaired

	if repo.Type.Valid {
		object["repo-type"] = repo.Type.String
	}
	if repo.VirtualInfo != nil {
		object["is-virtual"] = true
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
