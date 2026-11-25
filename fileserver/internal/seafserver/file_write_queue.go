package seafserver

import (
	"context"
	"database/sql"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
)

const createFileWriteQueue = `CREATE TABLE IF NOT EXISTS GoFileWriteQueue (
	repo_id TEXT PRIMARY KEY,
	head_id TEXT NOT NULL
)`

// EnsureFileWriteQueue makes the core's committed work visible to fileserver.
func EnsureFileWriteQueue(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, createFileWriteQueue)
	return err
}

func publishFileWrite(ctx context.Context, conn *sql.Conn, oldHead string, commit *commitmgr.Commit) (bool, error) {
	changed, err := publishSQLiteBranch(ctx, conn, oldHead, commit)
	if err != nil || !changed {
		return changed, err
	}
	if _, err := conn.ExecContext(ctx, createFileWriteQueue); err != nil {
		return false, err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO GoFileWriteQueue (repo_id, head_id) VALUES (?, ?)
		ON CONFLICT (repo_id) DO UPDATE SET head_id = excluded.head_id`, commit.RepoID, commit.CommitID)
	return err == nil, err
}

// FileWriteJob identifies the latest queued commit for a library.
type FileWriteJob struct {
	RepoID  string
	HeadID  string
	Deleted bool
}

// PendingFileWrites reads committed work without holding a database connection during merges.
func PendingFileWrites(ctx context.Context, db *sql.DB) ([]FileWriteJob, error) {
	rows, err := db.QueryContext(ctx, `SELECT q.repo_id, q.head_id, b.repo_id IS NULL
		FROM GoFileWriteQueue AS q LEFT JOIN Branch AS b ON b.repo_id = q.repo_id AND b.name = 'master'
		ORDER BY q.repo_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []FileWriteJob
	for rows.Next() {
		var job FileWriteJob
		if err := rows.Scan(&job.RepoID, &job.HeadID, &job.Deleted); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// FinishFileWrite clears the completed revision and preserves any newer publication.
func FinishFileWrite(ctx context.Context, db *sql.DB, job FileWriteJob) error {
	_, err := db.ExecContext(ctx, `DELETE FROM GoFileWriteQueue WHERE repo_id = ? AND head_id = ?`, job.RepoID, job.HeadID)
	return err
}
