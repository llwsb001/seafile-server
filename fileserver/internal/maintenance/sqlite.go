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

// openSQLiteSnapshot keeps SQLite recovery and WAL bookkeeping outside the
// input installation. A stopped database with no journal can be read in place.
func openSQLiteSnapshot(ctx context.Context, source string) (*sql.DB, string, error) {
	var sidecars []string
	for _, suffix := range []string{"-wal", "-journal"} {
		path := source + suffix
		_, err := os.Stat(path)
		if err == nil {
			sidecars = append(sidecars, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	if len(sidecars) == 0 {
		db, err := seafserver.OpenSQLiteImmutable(ctx, source)
		return db, "", err
	}

	dir, err := os.MkdirTemp("", "seafile-maintenance-")
	if err != nil {
		return nil, "", err
	}
	db, err := openSQLiteCopy(ctx, dir, source, sidecars)
	if err != nil {
		return nil, "", errors.Join(err, closeSQLiteSnapshot(dir))
	}
	return db, dir, nil
}

func openSQLiteCopy(ctx context.Context, dir, source string, sidecars []string) (*sql.DB, error) {
	for _, path := range append([]string{source}, sidecars...) {
		if err := copySQLiteFile(path, filepath.Join(dir, filepath.Base(path))); err != nil {
			return nil, err
		}
	}
	return openRepairDB(ctx, filepath.Join(dir, filepath.Base(source)))
}

func openRepairDB(ctx context.Context, path string) (*sql.DB, error) {
	db, err := seafserver.OpenSQLiteReadWrite(path)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open SQLite database %s: %w", path, err)
	}
	return db, nil
}

func copySQLiteFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

func closeSQLiteSnapshot(dir string) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(dir)
}
