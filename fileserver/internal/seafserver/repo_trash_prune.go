package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"math"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const oneDay = 24 * time.Hour

func (r *Reader) pruneOneTrashRepoRPC(db *sql.DB) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id := stringArgument(args, 0)
		if id == nil {
			return -1, rpcError(errBadArgs, "Argument should not be null")
		}
		if !validRepoID(*id) {
			return -1, rpcError(errBadArgs, "Invalid repo id")
		}
		if err := r.pruneTrash(ctx, db, false, "repo_id = ?", *id); err != nil {
			return -1, rpcError(errGeneral, "DB error: Add deleted record")
		}
		return 0, nil
	}
}

func (r *Reader) emptyRepoTrashRPC(db *sql.DB, byOwner bool) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		predicate := "1"
		var params []any
		if byOwner {
			owner := stringArgument(args, 0)
			if owner == nil {
				return -1, rpcError(errBadArgs, "Argument should not be null")
			}
			predicate, params = "owner_id = ?", []any{*owner}
		}
		if err := r.pruneTrash(ctx, db, true, predicate, params...); err != nil {
			return -1, rpcError(errGeneral, "Failed to empty repo trash.")
		}
		return 0, nil
	}
}

func (r *Reader) pruneTrash(ctx context.Context, db *sql.DB, verifyHead bool, predicate string, params ...any) error {
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		ids, err := r.selectTrashToPrune(ctx, conn, verifyHead, predicate, params...)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if _, err := conn.ExecContext(ctx, "INSERT INTO GarbageRepos (repo_id) VALUES (?) ON CONFLICT (repo_id) DO NOTHING", id); err != nil {
				return false, err
			}
			for _, table := range []string{"RepoFileCount", "RepoInfo", "RepoTrash"} {
				if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE repo_id = ?", id); err != nil {
					return false, err
				}
			}
		}
		return len(ids) != 0, nil
	})
	if err != nil {
		log.Printf("prune library trash: %v", err)
	}
	return err
}

func (r *Reader) selectTrashToPrune(ctx context.Context, conn *sql.Conn, verifyHead bool, predicate string, params ...any) ([]string, error) {
	// Only actual trash entries without an active library may be queued for collection.
	query := `SELECT repo_id, repo_name, head_id, owner_id FROM RepoTrash
		WHERE (` + predicate + `) AND NOT EXISTS (SELECT 1 FROM Repo WHERE Repo.repo_id = RepoTrash.repo_id)`
	rows, err := conn.QueryContext(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		var name, head, owner sql.NullString
		if err := rows.Scan(&id, &name, &head, &owner); err != nil {
			return nil, err
		}
		if !validRepoID(id) {
			continue
		}
		if verifyHead {
			// C bulk operations select from the visible trash listing.
			if !name.Valid || !head.Valid || !owner.Valid {
				continue
			}
			if _, err := r.Commit(id, head.String); err != nil {
				continue
			}
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// StartRepoTrashExpiry moves expired SQLite trash entries to the offline GC queue.
func (r *Reader) StartRepoTrashExpiry(ctx context.Context, db *sql.DB, config *ServerConfig) func() {
	text, found := config.file[configKey{"library_trash", "scan_days"}]
	days := configValue{text: text, found: found}.int()
	if days <= 0 || int64(days) > math.MaxInt64/int64(oneDay) {
		days = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(time.Duration(days) * oneDay)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.pruneExpiredTrash(ctx, db, config, time.Now().Unix())
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (r *Reader) pruneExpiredTrash(ctx context.Context, db *sql.DB, config *ServerConfig, now int64) error {
	days := config.value(ctx, "library_trash", "expire_days").int()
	if days <= 0 {
		days = 30
	}
	cutoff := now - int64(days)*int64(oneDay/time.Second)
	return r.pruneTrash(ctx, db, false, "del_time <= ?", cutoff)
}
