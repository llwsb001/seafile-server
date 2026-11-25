package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// RPCHandlers exposes usage and upload quota checks.
func (q *UserQuota) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_get_user_quota_usage": quotaUsageRPC(q.usage),
		"check_quota":                  q.checkRPC,
	}
}

// ShareUsageRPC uses the identity database to count group recipients.
func (q *UserQuota) ShareUsageRPC(identity *Identity) searpc.Handler {
	return quotaUsageRPC(func(ctx context.Context, user string) (int64, error) {
		return q.shareUsage(ctx, identity, user)
	})
}

// ManagementRPCHandlers exposes the quota settings and the full usage list.
func (q *UserQuota) ManagementRPCHandlers(db *sql.DB) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"get_user_quota":        q.getRPC,
		"set_user_quota":        setUserQuotaRPC(db),
		"list_user_quota_usage": q.listUsageRPC,
	}
}

func (q *UserQuota) getRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	user := stringArgument(args, 0)
	if user == nil {
		return -1, rpcError(errBadArgs, "Bad arguments")
	}
	return q.get(ctx, cString(*user)), nil
}

func setUserQuotaRPC(db *sql.DB) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		user := stringArgument(args, 0)
		if user == nil {
			return -1, rpcError(errBadArgs, "Bad arguments")
		}
		const query = "REPLACE INTO UserQuota (user, quota) VALUES (?, ?)"
		if _, err := db.ExecContext(ctx, query, cString(*user), int64Argument(args, 1)); err != nil {
			log.Printf("SQLite user quota write: %v", err)
			return -1, nil
		}
		return 0, nil
	}
}

func (q *UserQuota) checkRPC(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil {
		return -1, rpcError(errBadArgs, "Bad arguments")
	}
	return q.check(ctx, cString(*repoID), int64Argument(args, 1)), nil
}

func quotaUsageRPC(read func(context.Context, string) (int64, error)) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		user := stringArgument(args, 0)
		if user == nil {
			return -1, rpcError(errBadArgs, "Bad user id")
		}
		usage, err := read(ctx, cString(*user))
		if err != nil || usage < 0 {
			return -1, rpcError(500, "Internal server error")
		}
		return usage, nil
	}
}

func (q *UserQuota) listUsageRPC(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	users, err := q.listUsage(ctx)
	if err != nil {
		log.Printf("SQLite user quota usage list: %v", err)
		return nil, nil
	}
	return users, nil
}
