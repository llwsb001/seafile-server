package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) sharedUsersForSubdir(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = `SELECT v.repo_id, s.to_email, s.permission FROM SharedRepo s, VirtualRepo v
		WHERE s.repo_id = v.repo_id AND v.origin_repo = ? AND v.path = ? AND s.from_email = ?`
	return readSubdirShares(ctx, r.db, args, "Failed to get shared users for subdir from db", query, scanSharedUser)
}

func (r *Reader) sharedGroupsForSubdir(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = `SELECT v.repo_id, CAST(COALESCE(g.group_id, 0) AS INTEGER), g.permission
		FROM RepoGroup g, VirtualRepo v
		WHERE g.repo_id = v.repo_id AND v.origin_repo = ? AND v.path = ? AND g.user_name = ?`
	return readSubdirShares(ctx, r.db, args, "Failed to get shared groups fro subdir from db", query, scanSharedGroup)
}

func readSubdirShares[T any](ctx context.Context, db *sql.DB, args []json.RawMessage, message, query string, scan func(*sql.Rows) (T, error)) (any, *searpc.RPCError) {
	id, path, owner := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if id == nil || path == nil || owner == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if !validRepoID(*id) {
		return nil, rpcError(errBadArgs, "Invalid repo_id")
	}
	items, rpcErr := readShareRows(ctx, db, message, query, scan, *id, formatPath(*path), *owner)
	if rpcErr != nil {
		return nil, rpcErr
	}
	slices.Reverse(items)
	return items, nil
}
