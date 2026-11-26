package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

// SetVirtualBaseCommitRPC exposes the virtual library maintenance write.
func SetVirtualBaseCommitRPC(db *sql.DB) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, base := stringArgument(args, 0), stringArgument(args, 1)
		if id == nil || base == nil || !validRepoID(*id) || !utils.IsObjectIDValid(*base) {
			return -1, rpcError(errBadArgs, "Invalid arguments")
		}

		// Maintenance callers may suspend merging with a missing base commit.
		result, err := db.ExecContext(ctx, "UPDATE VirtualRepo SET base_commit = ? WHERE repo_id = ?", *base, *id)
		if err != nil {
			log.Printf("set virtual repo %s base commit: %v", *id, err)
			return -1, rpcError(errGeneral, "Internal server error")
		}
		rows, err := result.RowsAffected()
		if err != nil {
			log.Printf("set virtual repo %s base commit: %v", *id, err)
			return -1, rpcError(errGeneral, "Internal server error")
		}
		if rows == 0 {
			return -1, rpcError(errBadArgs, "Virtual repo not found")
		}
		return 0, nil
	}
}
