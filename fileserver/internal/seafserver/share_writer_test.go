package seafserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestShareWriterRollsBackBlockedCommit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "seafile.db")
	setup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()

	const repo = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	if _, err := setup.ExecContext(ctx, `
CREATE TABLE Repo (repo_id TEXT PRIMARY KEY);
CREATE TABLE VirtualRepo (repo_id TEXT PRIMARY KEY);
CREATE TABLE SharedRepo (repo_id TEXT, permission TEXT);
INSERT INTO Repo VALUES ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa');`); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenSQLiteReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var count int
	if err := readTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM SharedRepo").Scan(&count); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenShareWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	const insert = "INSERT INTO SharedRepo VALUES (?, 'r')"
	if _, err := writer.exec(ctx, repo, insert, repo); err == nil {
		t.Fatal("COMMIT should fail while a reader holds the SQLite read lock")
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM SharedRepo").Scan(&count); err != nil || count != 0 {
		t.Fatalf("after failed commit, row count = %d, error = %v", count, err)
	}
	if inScope, err := writer.exec(ctx, repo, insert, repo); err != nil || !inScope {
		t.Fatalf("retry failed: in scope = %v, error = %v", inScope, err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM SharedRepo").Scan(&count); err != nil || count != 1 {
		t.Fatalf("after retry, row count = %d, error = %v", count, err)
	}
}
