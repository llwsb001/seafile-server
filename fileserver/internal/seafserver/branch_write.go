package seafserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
)

// ErrSQLiteBranchChanged indicates that the caller must reload the current head.
var ErrSQLiteBranchChanged = errors.New("head commit id has changed")

// PublishSQLiteBranch publishes an already stored commit and its metadata in one
// transaction. The returned bool indicates whether it advanced the master branch.
func PublishSQLiteBranch(ctx context.Context, db *sql.DB, oldHead string, commit *commitmgr.Commit) (bool, error) {
	return withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		return publishSQLiteBranch(ctx, conn, oldHead, commit)
	})
}

func publishSQLiteBranch(ctx context.Context, conn *sql.Conn, oldHead string, commit *commitmgr.Commit) (bool, error) {
	if oldHead == commit.CommitID {
		var head string
		err := conn.QueryRowContext(ctx, "SELECT commit_id FROM Branch WHERE repo_id = ? AND name = 'master'",
			commit.RepoID).Scan(&head)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && head != oldHead) {
			return false, ErrSQLiteBranchChanged
		}
		return false, err
	}

	result, err := conn.ExecContext(ctx, `UPDATE Branch SET commit_id = ?
			WHERE repo_id = ? AND name = 'master' AND commit_id = ?`, commit.CommitID, commit.RepoID, oldHead)
	if err != nil {
		return false, fmt.Errorf("update SQLite branch: %w", err)
	}
	matched, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if matched == 0 {
		return false, ErrSQLiteBranchChanged
	}

	updateTime := commit.Ctime
	if updateTime == 0 {
		updateTime = time.Now().Unix()
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO RepoInfo
			(repo_id, name, update_time, version, is_encrypted, last_modifier) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (repo_id) DO UPDATE SET
				name = excluded.name, update_time = excluded.update_time, version = excluded.version,
				is_encrypted = excluded.is_encrypted, last_modifier = excluded.last_modifier`,
		commit.RepoID, commit.RepoName, updateTime, commit.Version, commit.Encrypted == "true", commit.CreatorName)
	if err != nil {
		return false, fmt.Errorf("update SQLite repository metadata: %w", err)
	}
	return true, nil
}
