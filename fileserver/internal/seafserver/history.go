package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) historyTruncateTime(ctx context.Context, repoID string, config *ServerConfig) int64 {
	days := r.historyLimit(ctx, repoID, config)
	if days == 0 {
		return 0
	}
	var since int64
	err := r.db.QueryRowContext(ctx, "SELECT COALESCE(timestamp, 0) FROM RepoValidSince WHERE repo_id = ?", repoID).Scan(&since)
	if err != nil {
		// The C reader treats both a missing row and a database error as unset.
		since = -1
	}
	if days > 0 {
		return max(since, historyDate(time.Now().Unix(), days))
	}
	return since
}

func historyDate(now int64, days int) int64 {
	// C multiplies the RPC's signed 32-bit day count before subtracting it from the timestamp.
	return now - int64(int32(days)*86400)
}

// HistoryRPCHandlers exposes SQLite history writes for the private management socket.
func (r *Reader) HistoryRPCHandlers(db *sql.DB) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"set_repo_history_limit": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			return setRepoHistoryLimit(ctx, db, args)
		},
		"set_repo_valid_since": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			if _, err := db.ExecContext(ctx, "REPLACE INTO RepoValidSince (repo_id, timestamp) VALUES (?, ?)",
				stringArgument(args, 0), int64Argument(args, 1)); err != nil {
				log.Printf("set_repo_valid_since: %v", err)
				return -1, nil
			}
			return 0, nil
		},
		"clean_up_repo_history": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			return r.cleanUpRepoHistory(ctx, db, args)
		},
	}
}

func setRepoHistoryLimit(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil || !validRepoID(*repoID) {
		return -1, rpcError(errBadArgs, "Invalid repo id")
	}
	// Virtual repositories read their origin's policy. C accepts a write to a
	// virtual repository without creating or replacing any policy row.
	const query = `REPLACE INTO RepoHistoryLimit (repo_id, days)
		SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM VirtualRepo WHERE repo_id = ?)`
	if _, err := db.ExecContext(ctx, query, *repoID, intArgument(args, 1), *repoID); err != nil {
		log.Printf("set_repo_history_limit: %v", err)
		return -1, rpcError(errInternal, "DB Error")
	}
	return 0, nil
}

var errHistoryRepoMissing = errors.New("missing history repository")

func (r *Reader) cleanUpRepoHistory(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil || !validRepoID(*repoID) {
		return -1, rpcError(errBadArgs, "Invalid arguments")
	}
	keepDays := intArgument(args, 1)
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		repo, err := repomgr.Load(ctx, conn, *repoID, r.Commit)
		if err != nil {
			log.Printf("clean_up_repo_history: %v", err)
			return false, errHistoryRepoMissing
		}
		var since int64
		if keepDays > 0 {
			since = historyDate(time.Now().Unix(), keepDays)
		}
		if since < 0 {
			return false, nil
		}
		if since == 0 {
			since = repo.LastModificationTime
		}
		// Compare against the current cutoff inside the write transaction.
		// Cleanup only changes the cutoff and keeps all stored objects.
		const query = `REPLACE INTO RepoValidSince (repo_id, timestamp)
			SELECT ?, ? WHERE ? > COALESCE(
				(SELECT COALESCE(timestamp, 0) FROM RepoValidSince WHERE repo_id = ?), -1)`
		_, err = conn.ExecContext(ctx, query, *repoID, since, since, *repoID)
		return err == nil, err
	})
	if errors.Is(err, errHistoryRepoMissing) {
		return -1, rpcError(errBadArgs, "Invalid arguments")
	}
	if err != nil {
		log.Printf("clean_up_repo_history: %v", err)
		return -1, rpcError(errGeneral, "Database error")
	}
	return 0, nil
}
