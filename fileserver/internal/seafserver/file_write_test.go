package seafserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileWriteRequiresGrantAndWritableRepository(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "seafile.db")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	db, err := OpenSQLiteReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("../../../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	reader := NewReader(db, dir)
	args := fileWriteRPCArgs(t, "fixture", "file writes", "owner@example.test")
	result, rpcErr := reader.createRepo(ctx, db, args)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := result.(string)
	repo, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO SharedRepo (repo_id, from_email, to_email, permission) VALUES (?, ?, ?, 'r')",
		repoID, "owner@example.test", "reader@example.test"); err != nil {
		t.Fatal(err)
	}

	post := reader.FileWriteRPCHandlers(db, nil, false)["seafile_post_empty_file"]
	if _, rpcErr := post(ctx, fileWriteRPCArgs(t, repoID, "/", "denied.txt", "reader@example.test")); rpcErr == nil || rpcErr.Code != errRepoAuth {
		t.Fatalf("reader's write resulted in %v", rpcErr)
	}
	if _, err := db.Exec("UPDATE RepoInfo SET status = 1 WHERE repo_id = ?", repoID); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := post(ctx, fileWriteRPCArgs(t, repoID, "/", "denied.txt", "owner@example.test")); rpcErr == nil || rpcErr.Code != errRepoLocked {
		t.Fatalf("owner's write to a read-only library resulted in %v", rpcErr)
	}
	current, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if current.HeadCommitID != repo.HeadCommitID {
		t.Fatal("denied writes changed the repository head")
	}
}

func fileWriteRPCArgs(t *testing.T, words ...string) []json.RawMessage {
	t.Helper()
	args := make([]json.RawMessage, len(words))
	for i, value := range words {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		args[i] = data
	}
	return args
}
