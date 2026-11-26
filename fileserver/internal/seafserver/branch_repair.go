package seafserver

import (
	"context"
	"database/sql"
	"fmt"
)

// PublishSQLiteRepair conditionally publishes a repair commit and invalidates
// the repaired library's sync tokens in the same transaction.
func PublishSQLiteRepair(ctx context.Context, db *sql.DB, repoID, oldHead, newHead string) (bool, error) {
	return withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		var result sql.Result
		var err error
		if oldHead == "" {
			result, err = conn.ExecContext(ctx, `INSERT INTO Branch(name, repo_id, commit_id)
				SELECT 'master', repo_id, ? FROM Repo WHERE repo_id = ? ON CONFLICT DO NOTHING`, newHead, repoID)
		} else {
			result, err = conn.ExecContext(ctx, `UPDATE Branch SET commit_id = ?
				WHERE repo_id = ? AND name = 'master' AND commit_id = ?
				AND EXISTS (SELECT 1 FROM Repo WHERE repo_id = ?)`, newHead, repoID, oldHead, repoID)
		}
		if err != nil {
			return false, fmt.Errorf("update repair branch: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if count != 1 {
			return false, ErrSQLiteBranchChanged
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM RepoTokenPeerInfo
			WHERE token IN (SELECT token FROM RepoUserToken WHERE repo_id = ?)
			AND token NOT IN (SELECT token FROM RepoUserToken WHERE repo_id <> ? AND token IS NOT NULL)`, repoID, repoID)
		if err != nil {
			return false, fmt.Errorf("delete repair peer tokens: %w", err)
		}
		_, err = conn.ExecContext(ctx, "DELETE FROM RepoUserToken WHERE repo_id = ?", repoID)
		if err != nil {
			return false, fmt.Errorf("delete repair sync tokens: %w", err)
		}
		return true, nil
	})
}
