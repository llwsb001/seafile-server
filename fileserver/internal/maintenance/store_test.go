package maintenance

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteSnapshotWithWAL(t *testing.T) {
	source := filepath.Join(t.TempDir(), "seafile.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA wal_autocheckpoint = 0",
		"CREATE TABLE Repo (repo_id TEXT PRIMARY KEY)",
		"PRAGMA wal_checkpoint(TRUNCATE)",
		"INSERT INTO Repo VALUES ('2526f1ca-635b-4eb4-baf3-0db7fc20ff77')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	// Copy the main database and uncheckpointed WAL without an SHM file.
	// The writer remains idle so the pair represents a stopped snapshot.
	dataDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataDir, "tmpfiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "seafile.conf"), []byte("[database]\ntype = sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := make(map[string][]byte)
	for _, suffix := range []string{"", "-wal"} {
		name := "seafile.db" + suffix
		data, err := os.ReadFile(source + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		original[name] = data
	}

	ctx := context.Background()
	store, err := Open(ctx, dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ids, err := store.RepoIDs(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "2526f1ca-635b-4eb4-baf3-0db7fc20ff77" {
		t.Fatalf("repo IDs from uncheckpointed WAL = %v", ids)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "seafile.db-shm")); !os.IsNotExist(err) {
		t.Fatalf("read-only snapshot created an SHM file or stat failed: %v", err)
	}
	for name, before := range original {
		after, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("read-only snapshot changed %s", name)
		}
	}
}
