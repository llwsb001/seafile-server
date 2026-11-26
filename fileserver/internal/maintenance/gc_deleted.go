package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
)

const deletedStoreEligible = `
	NOT EXISTS (SELECT 1 FROM Repo WHERE repo_id = GarbageRepos.repo_id)
	AND NOT EXISTS (SELECT 1 FROM Branch WHERE repo_id = GarbageRepos.repo_id)
	AND NOT EXISTS (SELECT 1 FROM RepoTrash WHERE repo_id = GarbageRepos.repo_id)
	AND NOT EXISTS (SELECT 1 FROM VirtualRepo
		WHERE repo_id = GarbageRepos.repo_id OR origin_repo = GarbageRepos.repo_id)`

// CollectGCDeleted reports or collects the physical stores queued in GarbageRepos.
func (s *Store) CollectGCDeleted(ctx context.Context, dryRun bool, output io.Writer) error {
	if dryRun {
		fmt.Fprintln(output, "=== Repos deleted by users ===")
		ids, err := deletedStoreIDs(ctx, s.db)
		if err != nil {
			return err
		}
		for _, id := range ids {
			fmt.Fprintf(output, "Repo %s can be GC'ed.\n", id)
		}
		return nil
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
	return s.collectDeletedStores(ctx, db, root, output)
}

func (s *Store) collectDeletedStores(ctx context.Context, db *sql.DB, root string, output io.Writer) error {
	fmt.Fprintln(output, "=== Repos deleted by users ===")
	ids, err := deletedStoreIDs(ctx, db)
	if err != nil {
		return err
	}
	var failed []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failed, err)...)
		}
		fmt.Fprintf(output, "Start to GC deleted repo %s.\n", id)
		if err := collectDeletedStore(ctx, db, id, func(kind string) error {
			return removeDeletedStore(root, kind, id)
		}); err != nil {
			failed = append(failed, fmt.Errorf("clear deleted store %s: %w", id, err))
		}
	}
	return errors.Join(failed...)
}

func deletedStoreIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT repo_id FROM GarbageRepos WHERE "+deletedStoreEligible+" ORDER BY repo_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if ValidRepoID(id) {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func collectDeletedStore(ctx context.Context, db *sql.DB, id string, remove func(string) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The uncommitted DELETE holds the write lock. A failed sweep rolls back
	// the queue marker even if one of its stores has already been removed.
	var claimed string
	err = tx.QueryRowContext(ctx, `DELETE FROM GarbageRepos WHERE repo_id = ? AND
		`+deletedStoreEligible+` RETURNING repo_id`, id).Scan(&claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, kind := range []string{"commits", "fs", "blocks"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := remove(kind); err != nil {
			return fmt.Errorf("remove %s store: %w", kind, err)
		}
	}
	return tx.Commit()
}

func removeDeletedStore(root, kind, id string) error {
	path := filepath.Join(root, "storage", kind, id)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("object store is not a directory: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return fmt.Errorf("object store follows a symbolic link: %s", path)
	}
	return os.RemoveAll(path)
}
