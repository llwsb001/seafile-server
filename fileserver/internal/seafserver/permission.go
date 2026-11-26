package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

type permissionReader struct {
	db        *sql.DB
	identity  *Identity
	cloudMode bool
}

// PermissionRPCHandlers exposes library permissions and group shares that need ccnet.
func (r *Reader) PermissionRPCHandlers(identity *Identity, cloudMode bool) map[string]searpc.Handler {
	permissions := &permissionReader{db: r.db, identity: identity, cloudMode: cloudMode}
	return map[string]searpc.Handler{
		"check_permission":              permissions.handler(1),
		"check_permission_by_path":      permissions.handler(2),
		"get_group_repos_by_user":       r.listGroupReposByUser(identity, false),
		"get_org_group_repos_by_user":   r.listGroupReposByUser(identity, true),
		"get_group_shared_repo_by_path": r.groupSharedRepoByPath(identity),
		"convert_repo_path":             r.convertSharedRepoPath(identity),
		"get_virtual_repos_by_owner":    r.listVirtualReposByOwner(permissions),
	}
}

func (s *permissionReader) handler(userIndex int) searpc.Handler {
	// CE evaluates the same library permission for both calls.
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		repoID, user := stringArgument(args, 0), stringArgument(args, userIndex)
		if repoID == nil || user == nil {
			return nil, rpcError(errBadArgs, "Arguments should not be empty")
		}
		if !validRepoID(*repoID) {
			return nil, rpcError(errBadArgs, "Invalid repo id")
		}
		if *user == "" {
			return nil, nil
		}
		permission, err := s.check(ctx, *repoID, *user)
		if err != nil {
			log.Printf("check_permission: %v", err)
			return nil, nil
		}
		return permission, nil
	}
}

const (
	personalShare   = "SELECT permission FROM SharedRepo WHERE repo_id = ? AND to_email = ?"
	personalFolders = `SELECT v.path, s.permission FROM SharedRepo s, VirtualRepo v
		WHERE s.repo_id = v.repo_id AND s.to_email = ? AND v.origin_repo = ?`
	groupFolders = `SELECT v.path, s.permission FROM RepoGroup s, VirtualRepo v
		WHERE s.repo_id = v.repo_id AND v.origin_repo = ?
		  AND s.group_id IN (SELECT value FROM json_each(?))`
	groupShare = `SELECT permission FROM RepoGroup WHERE repo_id = ?
		  AND group_id IN (SELECT value FROM json_each(?))
		  AND permission IN ('r', 'rw')
		ORDER BY permission DESC LIMIT 1`
)

func (s *permissionReader) check(ctx context.Context, repoID, user string) (*string, error) {
	var origin, path string
	err := s.db.QueryRowContext(ctx, "SELECT origin_repo, path FROM VirtualRepo WHERE repo_id = ?", repoID).
		Scan(&origin, &path)
	virtual := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if virtual {
		repoID = origin
	}
	owner, err := s.lookupText(ctx, "SELECT owner_id FROM RepoOwner WHERE repo_id = ?", repoID)
	if err != nil || (owner == nil && !virtual) {
		return nil, err
	}
	if owner != nil && asciiLower(*owner) == user {
		permission := "rw"
		return &permission, nil
	}

	if virtual {
		perm, err := s.folderPermission(ctx, path, false, personalFolders, user, repoID)
		if perm != nil || err != nil {
			return perm, err
		}
	} else {
		perm, err := s.lookupText(ctx, personalShare, repoID, user)
		if perm != nil || err != nil {
			return perm, err
		}
	}

	groups, err := s.identity.groupsByUser(ctx, user, true)
	if err != nil {
		return nil, err
	}
	groupIDs := encodeGroupIDs(groupInfoIDs(groups))

	if virtual {
		if len(groups) != 0 {
			perm, err := s.folderPermission(ctx, path, true, groupFolders, repoID, groupIDs)
			if perm != nil || err != nil {
				return perm, err
			}
		}
		perm, err := s.lookupText(ctx, personalShare, repoID, user)
		if perm != nil || err != nil {
			return perm, err
		}
	}
	if len(groups) != 0 {
		perm, err := s.lookupText(ctx, groupShare, repoID, groupIDs)
		if perm != nil || err != nil {
			return perm, err
		}
	}
	if s.cloudMode {
		return nil, nil
	}
	return s.lookupText(ctx, "SELECT permission FROM InnerPubRepo WHERE repo_id = ?", repoID)
}

func (s *permissionReader) lookupText(ctx context.Context, query string, args ...any) (*string, error) {
	var value *string
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return value, err
}

func (s *permissionReader) folderPermission(ctx context.Context, path string, group bool, query string, args ...any) (*string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	perms := make(map[string]*string)
	for rows.Next() {
		var folder string
		var perm *string
		if err := rows.Scan(&folder, &perm); err != nil {
			return nil, err
		}
		previous := perms[folder]
		if !group || previous == nil || *previous == "r" {
			perms[folder] = perm
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Paths are stored with a leading slash. Trim whole components so a
	// share of /docs can cover /docs/sub while leaving /docset independent.
	for path != "" {
		if perm := perms[path]; perm != nil {
			return perm, nil
		}
		slash := strings.LastIndexByte(path, '/')
		if slash < 0 {
			break
		}
		path = path[:slash]
	}
	return nil, nil
}
