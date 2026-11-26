package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDeletedGCProtectsReferencesAndKeepsFailedMarker(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "seafile.db"))
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
	const deleted = "11111111-1111-4111-8111-111111111111"
	const active = "22222222-2222-4222-8222-222222222222"
	const trashed = "33333333-3333-4333-8333-333333333333"
	const shared = "44444444-4444-4444-8444-444444444444"
	statements := []struct{ query, id string }{
		{"INSERT INTO GarbageRepos VALUES (?)", deleted},
		{"INSERT INTO GarbageRepos VALUES (?)", active},
		{"INSERT INTO GarbageRepos VALUES (?)", trashed},
		{"INSERT INTO GarbageRepos VALUES (?)", shared},
		{"INSERT INTO Repo VALUES (?)", active},
		{"INSERT INTO RepoTrash (repo_id) VALUES (?)", trashed},
		{"INSERT INTO VirtualRepo (origin_repo) VALUES (?)", shared},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.id); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := deletedStoreIDs(ctx, db)
	if err != nil || !slices.Equal(candidates, []string{deleted}) {
		t.Fatalf("unexpected deletion candidates: %v, %v", candidates, err)
	}
	for _, kind := range []string{"commits", "fs", "blocks"} {
		dir := filepath.Join(root, "storage", kind, deleted)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sample"), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	injected := errors.New("fixture store failure")
	err = collectDeletedStore(ctx, db, deleted, func(kind string) error {
		if kind == "fs" {
			return injected
		}
		return removeDeletedStore(root, kind, deleted)
	})
	if !errors.Is(err, injected) {
		t.Fatalf("failed store removal returned %v", err)
	}
	candidates, err = deletedStoreIDs(ctx, db)
	if err != nil || !slices.Equal(candidates, []string{deleted}) {
		t.Fatalf("failed store lost its marker: %v, %v", candidates, err)
	}
	if err := collectDeletedStore(ctx, db, deleted, func(kind string) error {
		return removeDeletedStore(root, kind, deleted)
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err = deletedStoreIDs(ctx, db)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("completed store kept a marker: %v, %v", candidates, err)
	}
	for _, kind := range []string{"commits", "fs", "blocks"} {
		if _, err := os.Stat(filepath.Join(root, "storage", kind, deleted)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s store remained after retry: %v", kind, err)
		}
	}
}
