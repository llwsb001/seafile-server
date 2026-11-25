package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"path"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

type directoryShareAction uint8

const (
	directoryShareCreate directoryShareAction = iota
	directorySharePermission
	directoryShareRemove
)

type directoryShare struct {
	repo, path, owner string
	user, permission  string
	group             int
}

func (r *Reader) DirectoryShareRPCHandlers(db *sql.DB) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"share_subdir_to_user":               r.writeDirectoryShare(db, directoryShareCreate, false),
		"share_subdir_to_group":              r.writeDirectoryShare(db, directoryShareCreate, true),
		"update_share_subdir_perm_for_user":  r.writeDirectoryShare(db, directorySharePermission, false),
		"update_share_subdir_perm_for_group": r.writeDirectoryShare(db, directorySharePermission, true),
		"unshare_subdir_for_user":            r.writeDirectoryShare(db, directoryShareRemove, false),
		"unshare_subdir_for_group":           r.writeDirectoryShare(db, directoryShareRemove, true),
	}
}

func directoryShareArgument(args []json.RawMessage, action directoryShareAction, group bool) (directoryShare, *searpc.RPCError) {
	repoID, folder, owner := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	var share directoryShare
	if repoID == nil || !validRepoID(*repoID) {
		return share, rpcError(errBadArgs, "Invalid repo_id parameter")
	}
	if folder == nil || *folder == "" || *folder == "/" {
		return share, rpcError(errBadArgs, "Invalid path parameter")
	}
	if owner == nil || *owner == "" {
		return share, rpcError(errBadArgs, "Invalid owner parameter")
	}
	share.repo, share.path, share.owner = *repoID, formatPath(*folder), *owner
	if group {
		share.group = intArgument(args, 3)
		if share.group < 0 {
			return share, rpcError(errBadArgs, "Invalid share_group parameter")
		}
	} else {
		user := stringArgument(args, 3)
		if user == nil || *user == "" || (*user == *owner && action != directoryShareCreate) {
			return share, rpcError(errBadArgs, "Invalid share_user parameter")
		}
		if *user == *owner {
			return share, rpcError(errBadArgs, "Can't share subdir to myself")
		}
		share.user = *user
	}
	if action != directoryShareRemove {
		permission := stringArgument(args, 4)
		if permission == nil || !validSharePermission(*permission) {
			return share, rpcError(errBadArgs, "Invalid permission parameter")
		}
		share.permission = *permission
	}
	return share, nil
}

func (r *Reader) writeDirectoryShare(db *sql.DB, action directoryShareAction, group bool) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		share, rpcErr := directoryShareArgument(args, action, group)
		if action == directoryShareCreate {
			if rpcErr != nil {
				return nil, rpcErr
			}
			var password string
			if value := stringArgument(args, 5); value != nil {
				password = *value
			}
			return r.createDirectoryShare(ctx, db, share, password, group)
		}
		if rpcErr != nil {
			return -1, rpcErr
		}
		message, err := share.change(ctx, db, action, group)
		if err != nil {
			log.Printf("change directory share: %v", err)
			return -1, rpcError(errGeneral, message)
		}
		return 0, nil
	}
}

func (r *Reader) createDirectoryShare(ctx context.Context, db *sql.DB, share directoryShare, password string, group bool) (any, *searpc.RPCError) {
	name := path.Base(share.path)
	var repoID string
	var rpcErr *searpc.RPCError
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		repoID, rpcErr = r.createVirtualRepoInTx(ctx, conn, share.repo, share.path, name, name, password)
		if rpcErr != nil {
			return false, nil
		}
		if group {
			_, err := conn.ExecContext(ctx, insertGroupShareSQL, repoID, share.group, share.owner, share.permission)
			return err == nil, err
		}
		_, err := conn.ExecContext(ctx, insertPersonalShareSQL, repoID, asciiLower(share.owner), asciiLower(share.user), share.permission)
		return err == nil, err
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	if err != nil {
		log.Printf("create directory share: %v", err)
		if group {
			return nil, rpcError(errGeneral, "Failed to share subdir to group")
		}
		return nil, rpcError(errGeneral, "Failed to share subdir to user")
	}
	return repoID, nil
}

func (s directoryShare) change(ctx context.Context, db *sql.DB, action directoryShareAction, group bool) (string, error) {
	const virtualPath = "repo_id IN (SELECT repo_id FROM VirtualRepo WHERE origin_repo = ? AND path = ?)"
	switch {
	case group && action == directoryShareRemove:
		_, err := db.ExecContext(ctx, "DELETE FROM RepoGroup WHERE "+virtualPath+" AND group_id = ? AND user_name = ?",
			s.repo, s.path, s.group, s.owner)
		return "Failed to unshare subdir for group", err
	case group:
		_, err := db.ExecContext(ctx, "UPDATE RepoGroup SET permission = ? WHERE "+virtualPath+" AND group_id = ? AND user_name = ?",
			s.permission, s.repo, s.path, s.group, s.owner)
		return "Failed to update share subdir permission for group", err
	case action == directoryShareRemove:
		_, err := db.ExecContext(ctx, "DELETE FROM SharedRepo WHERE "+virtualPath+" AND from_email = ? AND to_email = ?",
			s.repo, s.path, s.owner, s.user)
		return "Failed to unshare subdir for user", err
	default:
		_, err := db.ExecContext(ctx, "UPDATE SharedRepo SET permission = ? WHERE "+virtualPath+" AND from_email = ? AND to_email = ?",
			s.permission, s.repo, s.path, asciiLower(s.owner), asciiLower(s.user))
		return "Failed to update share subdir permission for user", err
	}
}
