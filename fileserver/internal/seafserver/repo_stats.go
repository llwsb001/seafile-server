package seafserver

import (
	"context"
	"database/sql"
	"fmt"
)

// StoreSQLiteRepoStats updates both counters if the computed commit is still the
// head. A superseded or deleted branch leaves the stored statistics untouched.
func StoreSQLiteRepoStats(ctx context.Context, db *sql.DB, repoID, headID string, size, fileCount int64) (bool, error) {
	return withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		var current bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM Branch WHERE repo_id = ? AND name = 'master' AND commit_id = ?
		)`, repoID, headID).Scan(&current); err != nil || !current {
			return false, err
		}
		err := writeSQLiteRepoStats(ctx, conn, repoID, headID, size, fileCount)
		return err == nil, err
	})
}

func writeSQLiteRepoStats(ctx context.Context, conn *sql.Conn, repoID, headID string, size, fileCount int64) error {
	if _, err := conn.ExecContext(ctx, `INSERT INTO RepoSize (repo_id, size, head_id) VALUES (?, ?, ?)
			ON CONFLICT (repo_id) DO UPDATE SET size = excluded.size, head_id = excluded.head_id`,
		repoID, size, headID); err != nil {
		return fmt.Errorf("store SQLite repository size: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO RepoFileCount (repo_id, file_count) VALUES (?, ?)
			ON CONFLICT (repo_id) DO UPDATE SET file_count = excluded.file_count`,
		repoID, fileCount); err != nil {
		return fmt.Errorf("store SQLite repository file count: %w", err)
	}
	return nil
}
