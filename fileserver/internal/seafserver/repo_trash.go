package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const selectTrashRepos = `SELECT repo_id, repo_name, head_id, owner_id, size, del_time FROM RepoTrash`

// RepoTrashWriteRPCHandlers exposes mutations on the private management socket.
func (r *Reader) RepoTrashWriteRPCHandlers(db *sql.DB, cloudMode bool) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_destroy_repo": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			id := stringArgument(args, 0)
			if id == nil {
				return -1, rpcError(errBadArgs, "Argument should not be null")
			}
			if !validRepoID(*id) {
				return -1, rpcError(errBadArgs, "Invalid repo id")
			}
			_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
				return r.moveRepoToTrash(ctx, conn, *id, cloudMode)
			})
			if err != nil {
				log.Printf("move library %s to trash: %v", *id, err)
				return -1, rpcError(errGeneral, "Failed to delete repo from db")
			}
			return 0, nil
		},
		"restore_repo_from_trash":   r.restoreTrashRepoRPC(db),
		"del_repo_from_trash":       r.pruneOneTrashRepoRPC(db),
		"empty_repo_trash":          r.emptyRepoTrashRPC(db, false),
		"empty_repo_trash_by_owner": r.emptyRepoTrashRPC(db, true),
	}
}

func (r *Reader) moveRepoToTrash(ctx context.Context, conn *sql.Conn, id string, cloudMode bool) (bool, error) {
	var head string
	err := conn.QueryRowContext(ctx, `SELECT commit_id FROM Branch WHERE repo_id = ? AND name = 'master'`, id).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	commit, err := r.Commit(id, head)
	if err != nil {
		// Keep the active reference when the head object cannot be read.
		return false, err
	}
	var owner string
	var size int64
	err = conn.QueryRowContext(ctx, `SELECT owner_id, COALESCE(RepoSize.size, 0)
		FROM RepoOwner LEFT JOIN RepoSize USING (repo_id) WHERE RepoOwner.repo_id = ?`, id).Scan(&owner, &size)
	if err != nil {
		return false, err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO RepoTrash (repo_id, repo_name, head_id, owner_id, size, org_id, del_time)
		VALUES (?, ?, ?, ?, ?, -1, ?)`, id, commit.RepoName, head, owner, size, time.Now().Unix())
	if err != nil {
		return false, err
	}

	views, err := virtualTrashChildren(ctx, conn, id)
	if err != nil {
		return false, err
	}
	var hasQueue bool
	err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema
		WHERE type = 'table' AND name = 'GoFileWriteQueue')`).Scan(&hasQueue)
	if err != nil {
		return false, err
	}

	for _, repoID := range append(views, id) {
		tables := []string{"Repo", "RepoHead", "Branch", "RepoOwner", "SharedRepo", "RepoGroup",
			"RepoValidSince", "RepoSize", "RepoInfo"}
		if !cloudMode {
			tables = append(tables, "InnerPubRepo")
		}
		if hasQueue {
			tables = append(tables, "GoFileWriteQueue")
		}
		if repoID == id {
			// The locked C SQLite build leaves the origin's token and peer rows in place.
			tables = append(tables, "RepoHistoryLimit")
		} else {
			tables = append(tables, "RepoUserToken")
			if _, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO GarbageRepos (repo_id) VALUES (?)", repoID); err != nil {
				return false, err
			}
		}
		for _, table := range tables {
			if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE repo_id = ?", repoID); err != nil {
				return false, fmt.Errorf("delete %s for %s: %w", table, repoID, err)
			}
		}
	}
	_, err = conn.ExecContext(ctx, "DELETE FROM VirtualRepo WHERE repo_id = ? OR origin_repo = ?", id, id)
	return err == nil, err
}

func virtualTrashChildren(ctx context.Context, conn *sql.Conn, id string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT repo_id FROM VirtualRepo WHERE origin_repo = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var repoID string
		if err := rows.Scan(&repoID); err != nil {
			return nil, err
		}
		ids = append(ids, repoID)
	}
	return ids, rows.Err()
}

func (r *Reader) listTrashRepos(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	start, limit := intArgument(args, 0), intArgument(args, 1)
	return r.readTrashRepos(ctx, false, selectTrashRepos+" ORDER BY del_time DESC LIMIT ? OFFSET ?", limit, start)
}

func (r *Reader) listTrashReposByOwner(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	owner := stringArgument(args, 0)
	if owner == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	return r.readTrashRepos(ctx, true, selectTrashRepos+" WHERE owner_id = ?", *owner)
}

func (r *Reader) readTrashRepos(ctx context.Context, reverse bool, query string, args ...any) (any, *searpc.RPCError) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, rpcError(errGeneral, "Failed to get trashed repo from db.")
	}
	defer rows.Close()
	var repos []map[string]any
	for rows.Next() {
		var id, name, head, owner sql.NullString
		var size, deleted sql.NullInt64
		if err := rows.Scan(&id, &name, &head, &owner, &size, &deleted); err != nil {
			return nil, rpcError(errGeneral, "Failed to get trashed repo from db.")
		}
		if !id.Valid || !name.Valid || !head.Valid || !owner.Valid {
			continue
		}
		commit, err := r.Commit(id.String, head.String)
		if err != nil {
			continue
		}
		repos = append(repos, map[string]any{
			"repo-id": id.String, "repo-name": name.String, "head-id": head.String, "owner-id": owner.String,
			"size": size.Int64, "del-time": deleted.Int64, "encrypted": commit.Encrypted == "true",
		})
	}
	if err := rows.Err(); err != nil {
		return nil, rpcError(errGeneral, "Failed to get trashed repo from db.")
	}
	if reverse {
		slices.Reverse(repos)
	}
	return repos, nil
}

func (r *Reader) getTrashRepoOwner(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	var owner sql.NullString
	err := r.db.QueryRowContext(ctx, "SELECT owner_id FROM RepoTrash WHERE repo_id = ?", *id).Scan(&owner)
	if err != nil || !owner.Valid {
		return nil, nil
	}
	return owner.String, nil
}
