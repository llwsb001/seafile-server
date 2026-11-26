package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
)

// CollectGCBlocks requires a stopped installation. Each store publishes its
// history boundary before any block referenced by old history can disappear.
func (s *Store) CollectGCBlocks(ctx context.Context, opts GCOptions, output io.Writer) error {
	fmt.Fprintln(output, "Database is SQLite, use offline GC.")
	ids, err := s.gcRepoIDs(ctx, opts.RepoIDs, opts.Prefix)
	if err != nil {
		return err
	}
	db, err := seafserver.OpenSQLiteReadWrite(filepath.Join(s.dataDir, "seafile.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	root, err := filepath.EvalSymlinks(s.dataDir)
	if err != nil {
		return err
	}

	now := time.Now()
	var failed []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failed, err)...)
		}
		if err := s.collectGCStore(ctx, db, root, id, now, output); err != nil {
			failed = append(failed, fmt.Errorf("GC for %s: %w", id, err))
		}
	}
	fmt.Fprintln(output, "=== GC is finished ===")
	return errors.Join(failed...)
}

func (s *Store) collectGCStore(ctx context.Context, db *sql.DB, root, id string, now time.Time, output io.Writer) error {
	repo, err := s.Repo(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || repo.ID != repo.StoreID {
		return err
	}
	references, err := gcReferences(ctx, s.db, repo.ID)
	if err != nil {
		return err
	}
	plan, err := s.planGC(ctx, repo, now)
	if err != nil {
		return err
	}
	if plan.skip != "" {
		fmt.Fprintln(output, plan.skip)
		return nil
	}
	fmt.Fprintf(output, "Scanning and deleting unused blocks for repo %.8s.\n", id)
	removed, err := applyBlockPlan(ctx, db, plan, references, func(blockID string) error {
		return removeGCBlock(root, repo.StoreID, blockID)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "GC finished for repo %.8s. %d blocks total, about %d reachable blocks, %d blocks are removed.\n",
		id, plan.blockCount, len(plan.reachable.blocks), removed)
	return nil
}

func applyBlockPlan(ctx context.Context, db *sql.DB, plan gcPlan, before []string, remove func(string) error) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	actual, err := gcReferences(ctx, tx, plan.repo.ID)
	if err != nil {
		return 0, err
	}
	if !slices.Equal(before, actual) {
		return 0, errors.New("SQLite references changed during offline GC")
	}
	for _, id := range sortedStringKeys(plan.cutoffs) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO RepoValidSince (repo_id, timestamp) VALUES (?, ?)
			ON CONFLICT (repo_id) DO UPDATE SET timestamp = excluded.timestamp
			WHERE RepoValidSince.timestamp IS NULL OR RepoValidSince.timestamp < excluded.timestamp`,
			id, plan.cutoffs[id]); err != nil {
			return 0, fmt.Errorf("publish history boundary for %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit history boundaries: %w", err)
	}

	for index, block := range plan.unused {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		if err := remove(block); err != nil {
			return index, fmt.Errorf("remove block %s: %w", block, err)
		}
	}
	return len(plan.unused), nil
}

type queryRows interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func gcReferences(ctx context.Context, db queryRows, origin string) ([]string, error) {
	// JSON retains NULL distinctly and avoids delimiter collisions in row comparisons.
	const query = `WITH ids AS (
		SELECT ? AS id UNION SELECT repo_id FROM VirtualRepo WHERE origin_repo = ?
	) SELECT row FROM (
		SELECT json_array('Repo', repo_id) AS row FROM Repo WHERE repo_id IN ids
		UNION ALL SELECT json_array('Branch', repo_id, name, commit_id) FROM Branch WHERE repo_id IN ids
		UNION ALL SELECT json_array('VirtualRepo', repo_id, origin_repo, path, base_commit) FROM VirtualRepo WHERE repo_id IN ids
		UNION ALL SELECT json_array('RepoTrash', repo_id, head_id) FROM RepoTrash WHERE repo_id IN ids
		UNION ALL SELECT json_array('RepoHistoryLimit', repo_id, days) FROM RepoHistoryLimit WHERE repo_id IN ids
		UNION ALL SELECT json_array('RepoValidSince', repo_id, timestamp) FROM RepoValidSince WHERE repo_id IN ids
		UNION ALL SELECT json_array('SeafileConf', cfg_key, value) FROM SeafileConf WHERE cfg_group = 'history'
	) ORDER BY row`
	rows, err := db.QueryContext(ctx, query, origin, origin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var references []string
	for rows.Next() {
		var reference string
		if err := rows.Scan(&reference); err != nil {
			return nil, err
		}
		references = append(references, reference)
	}
	return references, rows.Err()
}

func removeGCBlock(root, storeID, id string) error {
	directory := filepath.Join(root, "storage", "blocks", storeID, id[:2])
	resolved, err := filepath.EvalSymlinks(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if resolved != directory {
		return fmt.Errorf("object directory follows a symbolic link: %s", directory)
	}
	path := filepath.Join(directory, id[2:])
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("object is not a regular file: %s", path)
	}
	return os.Remove(path)
}
