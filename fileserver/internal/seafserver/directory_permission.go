package seafserver

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

func (r *Reader) listDirWithPermission(permissions *permissionReader) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		repoID, path, dirID, user := stringArgument(args, 0), stringArgument(args, 1),
			stringArgument(args, 2), stringArgument(args, 3)
		switch {
		case repoID == nil || !validRepoID(*repoID):
			return nil, rpcError(errBadArgs, "Invalid repo id")
		case path == nil:
			return nil, rpcError(errBadArgs, "Invalid path")
		case dirID == nil || !utils.IsObjectIDValid(*dirID):
			return nil, rpcError(errBadArgs, "Invalid dir id")
		case user == nil:
			return nil, rpcError(errBadArgs, "Invalid user")
		}

		permission, err := permissions.check(ctx, *repoID, *user)
		if err != nil {
			log.Printf("list_dir_with_perm: check permission: %v", err)
		}
		if err != nil || permission == nil {
			return nil, rpcError(errBadArgs, "Access denied")
		}
		repo, err := r.Repo(ctx, *repoID)
		if err != nil {
			return nil, rpcError(errBadArgs, "Bad repo id")
		}

		dirPath := formatPath(*path)
		// A virtual library shares the origin's object store. Bind the object
		// ID to a directory under the requested library's projected root.
		dir, rpcErr := r.directoryByPath(repo, repo.RootID, dirPath)
		if rpcErr != nil || dir.DirID != *dirID {
			return nil, rpcError(errBadDirID, "Bad dir id")
		}
		parent := strings.TrimSuffix(dirPath, "/") + "/"
		var shared map[string]bool
		if repo.VirtualInfo == nil {
			owner, err := permissions.lookupText(ctx, "SELECT owner_id FROM RepoOwner WHERE repo_id = ?", *repoID)
			if err == nil && owner != nil && asciiLower(*owner) == *user {
				shared, err = r.sharedDirectoryPaths(ctx, repo.StoreID, parent+"%")
			}
			if err != nil {
				log.Printf("list_dir_with_perm: load shared paths: %v", err)
			}
		}

		var entries []map[string]any
		for _, entry := range directoryEntryPage(dir, intArgument(args, 4), intArgument(args, 5)) {
			if !utils.IsObjectIDValid(entry.ID) {
				continue
			}
			value := direntObject(entry, dir.Version)
			value["permission"] = *permission
			value["modifier"] = optionalString(entry.Modifier)
			value["is-shared"] = fsmgr.IsDir(entry.Mode) && shared[parent+entry.Name]
			entries = append(entries, value)
		}
		return entries, nil
	}
}

func (r *Reader) sharedDirectoryPaths(ctx context.Context, storeID, pattern string) (map[string]bool, error) {
	const query = `
		SELECT v.path FROM VirtualRepo v JOIN SharedRepo s ON s.repo_id = v.repo_id
		WHERE v.origin_repo = ? AND v.path LIKE ?
		UNION
		SELECT v.path FROM VirtualRepo v JOIN RepoGroup g ON g.repo_id = v.repo_id
		WHERE v.origin_repo = ? AND v.path LIKE ?`
	rows, err := r.db.QueryContext(ctx, query, storeID, pattern, storeID, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shared := make(map[string]bool)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		shared[path] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return shared, nil
}
