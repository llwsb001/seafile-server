package seafserver

import (
	"context"
	"encoding/json"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) getVirtualRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	origin, path, owner := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if origin == nil || path == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}

	query := "SELECT repo_id FROM VirtualRepo WHERE origin_repo = ? AND path = ?"
	values := []any{*origin, formatPath(*path)}
	if owner != nil {
		query = `SELECT o.repo_id FROM RepoOwner o JOIN VirtualRepo v ON o.repo_id = v.repo_id
			WHERE v.origin_repo = ? AND v.path = ? AND o.owner_id = ?`
		values = append(values, *owner)
	}
	var repoID string
	if err := r.db.QueryRowContext(ctx, query, values...).Scan(&repoID); err != nil {
		return nil, nil
	}
	if !validRepoID(repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	repo, err := r.Repo(ctx, repoID)
	if err != nil {
		return nil, nil
	}
	return repoObject(repo), nil
}

func (r *Reader) listVirtualReposByOwner(permissions *permissionReader) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		owner := stringArgument(args, 0)
		if owner == nil {
			return nil, rpcError(errBadArgs, "Argument should not be null")
		}
		ids, err := r.virtualRepoIDsByOwner(ctx, *owner)
		if err != nil {
			return nil, rpcError(errGeneral, "DB error")
		}
		var repos []map[string]any
		for _, id := range ids {
			repo, err := r.Repo(ctx, id)
			if err != nil || repo.VirtualInfo == nil {
				continue
			}
			origin, err := r.Repo(ctx, repo.VirtualInfo.OriginRepoID)
			if err != nil {
				continue
			}
			originalOwner, err := permissions.lookupText(ctx, "SELECT owner_id FROM RepoOwner WHERE repo_id = ?", origin.ID)
			if err != nil {
				log.Printf("get_virtual_repos_by_owner: original owner: %v", err)
			}
			permission, err := permissions.check(ctx, id, *owner)
			if err != nil {
				log.Printf("get_virtual_repos_by_owner: check permission: %v", err)
			}
			object := repoObject(repo)
			object["is-original-owner"] = originalOwner != nil && asciiLower(*originalOwner) == *owner
			object["origin-repo-name"] = origin.Name
			object["virtual-perm"] = permission
			repos = append(repos, object)
		}
		return repos, nil
	}
}

func (r *Reader) virtualRepoIDsByOwner(ctx context.Context, owner string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT o.repo_id FROM RepoOwner o JOIN VirtualRepo v
		ON o.repo_id = v.repo_id WHERE o.owner_id = ?`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
