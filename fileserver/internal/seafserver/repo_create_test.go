package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateRepoPublicationsAreAtomic(t *testing.T) {
	ctx := context.Background()
	schema, err := os.ReadFile("../../../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	args := []json.RawMessage{
		json.RawMessage(`"new library"`), json.RawMessage(`"description"`),
		json.RawMessage(`"owner@example.test"`), json.RawMessage("null"), json.RawMessage("2"),
	}
	for _, failure := range []string{"object-write", "db-commit"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "seafile.db")
			bootstrap, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bootstrap.Exec(string(schema)); err != nil {
				bootstrap.Close()
				t.Fatal(err)
			}
			bootstrap.Close()

			db, err := OpenSQLiteReadWrite(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			state := func() [6]int {
				t.Helper()
				var rows [6]int
				row := db.QueryRow(`SELECT (SELECT count(*) FROM Repo), (SELECT count(*) FROM Branch),
					(SELECT count(*) FROM RepoHead), (SELECT count(*) FROM RepoOwner),
					(SELECT count(*) FROM RepoInfo), (SELECT count(*) FROM VirtualRepo)`)
				if err := row.Scan(&rows[0], &rows[1], &rows[2], &rows[3], &rows[4], &rows[5]); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			data := root
			if failure == "object-write" {
				data = filepath.Join(root, "blocked")
				if err := os.WriteFile(data, []byte("fixture obstruction"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := db.Exec(`PRAGMA foreign_keys = ON;
					CREATE TABLE RequiredParent (id INTEGER PRIMARY KEY);
					CREATE TABLE DeferredChild (parent_id INTEGER REFERENCES RequiredParent(id) DEFERRABLE INITIALLY DEFERRED);
					CREATE TRIGGER fail_repo_commit AFTER INSERT ON RepoInfo
					BEGIN INSERT INTO DeferredChild (parent_id) VALUES (1); END;`)
				if err != nil {
					t.Fatal(err)
				}
			}

			if got, rpcErr := NewReader(db, data).createRepo(ctx, db, args); got != nil || rpcErr == nil {
				t.Fatalf("creation should fail for %s", failure)
			}
			if got := state(); got != ([6]int{}) {
				t.Fatalf("failed creation published records: %v", got)
			}
			if failure == "db-commit" {
				if _, err := db.Exec("DROP TRIGGER fail_repo_commit"); err != nil {
					t.Fatal(err)
				}
			}
			reader := NewReader(db, root)
			result, rpcErr := reader.createRepo(ctx, db, args)
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			repo, err := reader.Repo(ctx, result.(string))
			if err != nil {
				t.Fatal(err)
			}
			if repo.Name != "new library" || repo.RootID != emptyObjectID {
				t.Fatalf("unexpected repository after retry: %q / %q", repo.Name, repo.RootID)
			}
			if got := state(); got != [6]int{1, 1, 1, 1, 1, 0} {
				t.Fatalf("retry published unexpected rows: %v", got)
			}
		})
	}
}
