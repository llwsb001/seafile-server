package seafserver

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSQLiteRepoStatsKeepsCurrentHead(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seafile.db")
	observer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	schema, err := os.ReadFile("../../../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	const repo = "size-test"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := observer.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(string(schema))
	exec("INSERT INTO Branch VALUES ('master', ?, 'first')", repo)
	writer, err := OpenSQLiteReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	check := func(expectedHead string, expectedSize, expectedCount int64) {
		t.Helper()
		var head string
		var size, count int64
		err := observer.QueryRow(`SELECT s.head_id, s.size, f.file_count FROM RepoSize s
			JOIN RepoFileCount f USING (repo_id) WHERE s.repo_id = ?`, repo).Scan(&head, &size, &count)
		if err != nil || head != expectedHead || size != expectedSize || count != expectedCount {
			t.Fatalf("stats = (%q, %d, %d), err %v, want (%q, %d, %d)",
				head, size, count, err, expectedHead, expectedSize, expectedCount)
		}
	}
	store := func(head string, size, count int64) {
		t.Helper()
		stored, err := StoreSQLiteRepoStats(ctx, writer, repo, head, size, count)
		if err != nil || !stored {
			t.Fatalf("store returned %v, %v", stored, err)
		}
	}
	store("first", 50, 2)
	check("first", 50, 2)

	exec("UPDATE Branch SET commit_id = 'second' WHERE repo_id = ?", repo)
	exec(`CREATE TRIGGER reject_stats BEFORE UPDATE ON RepoFileCount
		BEGIN SELECT RAISE(ABORT, 'test count error'); END`)
	if stored, err := StoreSQLiteRepoStats(ctx, writer, repo, "second", 60, 3); stored || err == nil {
		t.Fatalf("count error returned %v, %v", stored, err)
	}
	check("first", 50, 2)
	exec("DROP TRIGGER reject_stats")
	store("second", 60, 3)
	if stored, err := StoreSQLiteRepoStats(ctx, writer, repo, "first", 50, 2); stored || err != nil {
		t.Fatalf("stale write returned %v, %v", stored, err)
	}
	check("second", 60, 3)

	restarted, err := OpenSQLiteReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var count int64
	if err := restarted.QueryRow("SELECT file_count FROM RepoFileCount WHERE repo_id = ?", repo).Scan(&count); err != nil || count != 3 {
		t.Fatalf("new process count = %d, %v", count, err)
	}
}
