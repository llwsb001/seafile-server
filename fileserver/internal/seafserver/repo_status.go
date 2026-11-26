package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// SetRepoStatusRPC exposes the status change on the private management socket.
func SetRepoStatusRPC(db *sql.DB) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, status := stringArgument(args, 0), intArgument(args, 1)
		if id == nil || !validRepoID(*id) || status < repomgr.RepoStatusNormal || status >= repomgr.NRepoStatus {
			return -1, rpcError(errBadArgs, "Arguments error")
		}
		// One statement keeps the source repository and its virtual repositories in sync.
		if _, err := db.ExecContext(ctx, `UPDATE RepoInfo SET status = ?
			WHERE repo_id = ? OR repo_id IN (SELECT repo_id FROM VirtualRepo WHERE origin_repo = ?)`,
			status, *id, *id); err != nil {
			log.Printf("set repo status %s: %v", *id, err)
			return -1, nil
		}
		return 0, nil
	}
}
