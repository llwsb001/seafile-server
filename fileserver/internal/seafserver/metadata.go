package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) getRepoOwner(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if !validRepoID(*repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	owner, err := repomgr.LoadRepoOwner(ctx, r.db, *repoID)
	if err != nil {
		return nil, nil
	}
	return asciiLower(owner), nil
}

func (r *Reader) isRepoOwner(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email, repoID := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || !validRepoID(*repoID) {
		return 0, rpcError(errBadArgs, "Invalid repo id")
	}
	owner, err := repomgr.LoadRepoOwner(ctx, r.db, *repoID)
	if err != nil || email == nil || asciiLower(owner) != *email {
		return 0, nil
	}
	return 1, nil
}

func (r *Reader) getRepoStatus(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil || !validRepoID(*repoID) {
		return -1, rpcError(errBadArgs, "Arguments error")
	}
	status, err := repomgr.LoadRepoStatus(ctx, r.db, *repoID)
	if err != nil || status == -1 {
		return 0, nil
	}
	return status, nil
}

func (r *Reader) getRepoHistoryLimit(config *ServerConfig) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		repoID := stringArgument(args, 0)
		if repoID == nil || !validRepoID(*repoID) {
			return -1, rpcError(errBadArgs, "Invalid repo id")
		}
		return r.historyLimit(ctx, *repoID, config), nil
	}
}

func (r *Reader) historyLimit(ctx context.Context, repoID string, config *ServerConfig) int {
	const query = `SELECT COALESCE(days, 0) FROM RepoHistoryLimit
		WHERE repo_id = COALESCE((SELECT origin_repo FROM VirtualRepo WHERE repo_id = ?), ?)`
	var days int
	err := r.db.QueryRowContext(ctx, query, repoID, repoID).Scan(&days)
	if errors.Is(err, sql.ErrNoRows) {
		return max(-1, config.value(ctx, "history", "keep_days").int())
	}
	if err != nil || days < 0 {
		return -1
	}
	return days
}
