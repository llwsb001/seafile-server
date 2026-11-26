package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) restoreTrashRepoRPC(db *sql.DB) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id := stringArgument(args, 0)
		if id == nil {
			return -1, rpcError(errBadArgs, "Argument should not be null")
		}
		if !validRepoID(*id) {
			return -1, rpcError(errBadArgs, "Invalid repo id")
		}
		restored, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
			return r.restoreTrashRepo(ctx, conn, *id)
		})
		if err != nil {
			log.Printf("restore library %s from trash: %v", *id, err)
			return -1, rpcError(errGeneral, "Failed to restore repo from trash.")
		}
		if !restored {
			return -1, nil
		}
		return 0, nil
	}
}

func (r *Reader) restoreTrashRepo(ctx context.Context, conn *sql.Conn, id string) (bool, error) {
	var name, head, owner sql.NullString
	var size int64
	err := conn.QueryRowContext(ctx, `SELECT repo_name, head_id, owner_id, COALESCE(size, 0)
		FROM RepoTrash WHERE repo_id = ?`, id).Scan(&name, &head, &owner, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !name.Valid || !head.Valid || !owner.Valid {
		return false, nil
	}
	commit, err := r.Commit(id, head.String)
	if err != nil {
		log.Printf("read head %s for trashed library %s: %v", head.String, id, err)
		return false, nil
	}

	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO Repo (repo_id) VALUES (?) ON CONFLICT (repo_id) DO NOTHING", []any{id}},
		{"INSERT INTO RepoOwner (repo_id, owner_id) VALUES (?, ?) ON CONFLICT (repo_id) DO NOTHING", []any{id, owner.String}},
		{`INSERT INTO Branch (name, repo_id, commit_id)
			SELECT 'master', ?, ? WHERE NOT EXISTS (SELECT 1 FROM Branch WHERE repo_id = ?)`, []any{id, head.String, id}},
		{"INSERT INTO RepoHead (repo_id, branch_name) VALUES (?, 'master') ON CONFLICT (repo_id) DO NOTHING", []any{id}},
		{"INSERT INTO RepoSize (repo_id, size, head_id) VALUES (?, ?, ?) ON CONFLICT (repo_id) DO NOTHING", []any{id, size, head.String}},
		{`INSERT INTO RepoInfo (repo_id, name, update_time, version, is_encrypted, last_modifier)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (repo_id) DO NOTHING`,
			[]any{id, name.String, commit.Ctime, commit.Version, commit.Encrypted == "true", commit.CreatorName}},
		{"DELETE FROM RepoTrash WHERE repo_id = ?", []any{id}},
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return false, fmt.Errorf("restore metadata: %w", err)
		}
	}
	return true, nil
}
