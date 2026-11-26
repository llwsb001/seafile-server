package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGCBlocksCommitBoundaryBeforeDeletionAndCanRetry(t *testing.T) {
	const repoID = "12345678-1234-4234-8234-123456789012"
	const first = "1111111111111111111111111111111111111111"
	const second = "2222222222222222222222222222222222222222"
	const cutoff = 2_000_000_000

	for _, failure := range []string{"changed-reference", "commit", "before-deletion", "partial-deletion"} {
		t.Run(failure, func(t *testing.T) {
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
			if _, err = db.Exec(string(schema)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO Branch VALUES ('master', ?, ?)", repoID, first); err != nil {
				t.Fatal(err)
			}
			for _, block := range []string{first, second} {
				dir := filepath.Join(root, "storage", "blocks", repoID, block[:2])
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, block[2:]), []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := gcReferences(ctx, db, repoID)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "commit":
				if _, err := db.Exec(`CREATE TRIGGER fail_gc BEFORE INSERT ON RepoValidSince
					BEGIN SELECT RAISE(ABORT, 'fixture history failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "changed-reference":
				if _, err := db.Exec("UPDATE Branch SET commit_id = ? WHERE repo_id = ?", second, repoID); err != nil {
					t.Fatal(err)
				}
			}
			plan := gcPlan{repo: Repo{ID: repoID}, unused: []string{first, second}, cutoffs: map[string]int64{repoID: cutoff}}
			interrupted, cancel := context.WithCancel(ctx)
			defer cancel()
			removed, err := applyGCPlan(interrupted, db, plan, before, func(kind, block string) error {
				var current int64
				if err := db.QueryRow("SELECT timestamp FROM RepoValidSince WHERE repo_id = ?", repoID).Scan(&current); err != nil || current != cutoff {
					t.Fatalf("deletion ran before the boundary commit: %d, %v", current, err)
				}
				if failure == "before-deletion" {
					return errors.New("fixture stopped after commit")
				}
				if err := removeGCObject(root, kind, repoID, block); err != nil {
					return err
				}
				cancel()
				return nil
			})
			if err == nil {
				t.Fatal("injected failure was ignored")
			}
			wantRemoved := 0
			if failure == "partial-deletion" {
				wantRemoved = 1
			}
			if removed != wantRemoved {
				t.Fatalf("removed %d blocks, want %d", removed, wantRemoved)
			}
			store := Store{dataDir: root}
			remaining, err := store.objectIDs("blocks", repoID)
			if err != nil || len(remaining) != 2-wantRemoved {
				t.Fatalf("remaining blocks %v, error %v", remaining, err)
			}
			if failure == "commit" {
				if _, err := db.Exec("DROP TRIGGER fail_gc"); err != nil {
					t.Fatal(err)
				}
			}
			retryReferences, err := gcReferences(ctx, db, repoID)
			if err != nil {
				t.Fatal(err)
			}
			plan.unused = remaining
			_, err = applyGCPlan(ctx, db, plan, retryReferences, func(kind, block string) error {
				return removeGCObject(root, kind, repoID, block)
			})
			if err != nil {
				t.Fatal(err)
			}
			remaining, err = store.objectIDs("blocks", repoID)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("retry left blocks %v, error %v", remaining, err)
			}
		})
	}
}
