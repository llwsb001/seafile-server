package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func accessPropertyRepoID(args []json.RawMessage) (string, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return "", rpcError(errBadArgs, "Argument should not be null")
	}
	if len(*id) != 36 {
		return "", rpcError(errBadArgs, "Wrong repo id")
	}
	return *id, nil
}

// SetRepoAccessPropertyRPC writes the legacy WebAP property on the management socket.
func SetRepoAccessPropertyRPC(db *sql.DB) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, rpcErr := accessPropertyRepoID(args)
		if rpcErr != nil {
			return -1, rpcErr
		}
		property := stringArgument(args, 1)
		if property == nil || (*property != "public" && *property != "own" && *property != "private") {
			return -1, rpcError(errBadArgs, "Wrong access property")
		}
		result, err := db.ExecContext(ctx, `INSERT INTO WebAP (repo_id, access_property) VALUES (?, ?)
			ON CONFLICT (repo_id) DO UPDATE SET access_property = excluded.access_property
			WHERE WebAP.access_property IS NOT NULL`, id, *property)
		if err != nil {
			log.Printf("set repo access property %s: %v", id, err)
			return -1, rpcError(errGeneral, "Internal server error")
		}
		// In C a stored NULL follows the INSERT path and encounters the unique key.
		rows, err := result.RowsAffected()
		if err != nil || rows == 0 {
			return -1, rpcError(errGeneral, "Internal server error")
		}
		return 0, nil
	}
}

func (r *Reader) getRepoAccessProperty(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, rpcErr := accessPropertyRepoID(args)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var property sql.NullString
	err := r.db.QueryRowContext(ctx, "SELECT access_property FROM WebAP WHERE repo_id = ?", id).Scan(&property)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("get repo access property %s: %v", id, err)
	}
	if !property.Valid {
		return nil, nil
	}
	return property.String, nil
}
