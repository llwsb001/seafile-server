package seafserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const testVirtualOrigin = "2526f1ca-635b-4eb4-baf3-0db7fc20ff77"
const testVirtualOwner = "owner@example.test"

func virtualTestOrigin(t *testing.T) (*Reader, *sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	db, err := OpenSQLiteReadWrite(filepath.Join(root, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Install the existing schema before opening the mode=rw pool.
	setup, err := sql.Open("sqlite", filepath.Join(root, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	schema, err := os.ReadFile("../../../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(db, root)
	dir, err := fsmgr.NewSeafdir(initialRepoVersion, []*fsmgr.SeafDirent{
		{Mode: syscall.S_IFDIR | 0o755, ID: emptyObjectID, Name: "team"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var object bytes.Buffer
	if err := dir.ToData(&object); err != nil {
		t.Fatal(err)
	}
	if err := reader.fs.Write(testVirtualOrigin, dir.DirID, &object, true); err != nil {
		t.Fatal(err)
	}
	commit := newInitialRepoCommit(testVirtualOrigin, dir.DirID, "origin", "", "Created library", testVirtualOwner, initialRepoVersion, 1700000000)
	if err := reader.writeInitialRepoCommit(commit); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		err := publishInitialRepo(ctx, conn, commit, testVirtualOwner)
		return err == nil, err
	}); err != nil {
		t.Fatal(err)
	}
	return reader, db, root
}

func virtualTestArgs(path string) []json.RawMessage {
	args := []string{testVirtualOrigin, path, "team folder", "team description", "different-owner@example.test", ""}
	raw := make([]json.RawMessage, len(args))
	for index, value := range args {
		raw[index], _ = json.Marshal(value)
	}
	return raw
}

func virtualTestState(t *testing.T, db *sql.DB) [6]int {
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

func TestCreateVirtualRepoSerializesDuplicateRequests(t *testing.T) {
	reader, first, root := virtualTestOrigin(t)
	second, err := OpenSQLiteReadWrite(filepath.Join(root, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	pools := []*sql.DB{first, second}
	paths := []string{"team///", "/team"}
	const requests = 12
	type outcome struct {
		id     any
		rpcErr *searpc.RPCError
	}
	results := make(chan outcome, requests)
	start := make(chan struct{})
	var calls sync.WaitGroup
	for n := 0; n < requests; n++ {
		pool, path := pools[n%len(pools)], paths[n%len(paths)]
		calls.Add(1)
		go func() {
			defer calls.Done()
			<-start
			id, rpcErr := reader.createVirtualRepo(context.Background(), pool, virtualTestArgs(path))
			results <- outcome{id, rpcErr}
		}()
	}
	close(start)
	calls.Wait()
	close(results)
	var virtualID string
	for result := range results {
		id, ok := result.id.(string)
		if result.rpcErr != nil || !ok || !validRepoID(id) {
			t.Fatalf("concurrent create returned %v, %v", result.id, result.rpcErr)
		}
		if virtualID == "" {
			virtualID = id
		}
		if id != virtualID {
			t.Fatalf("duplicate directory produced IDs %q and %q", virtualID, id)
		}
	}
	if got := virtualTestState(t, first); got != [6]int{2, 2, 2, 2, 2, 1} {
		t.Fatalf("duplicate creation published unexpected rows %v", got)
	}
	repo, err := reader.Repo(context.Background(), virtualID)
	if err != nil || repo.VirtualInfo == nil || repo.StoreID != testVirtualOrigin ||
		repo.VirtualInfo.Path != "/team" || repo.RootID != emptyObjectID {
		t.Fatalf("created virtual repository does not reuse the origin directory: %+v, %v", repo, err)
	}
}

func TestCreateEncryptedVirtualRepoUsesOriginFileKey(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		version int
		useHash bool
	}{
		{"version 2 with magic", 2, false},
		{"version 3 with magic", 3, false},
		{"version 4 with password hash", 4, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			passwords, db, root, _ := seedSQLitePasswordRepo(t, test.version)
			if test.useHash {
				args := governanceArgs(t, testVirtualOrigin, testVirtualOwner, oldSyntheticPassword, "pbkdf2_sha256", "1500")
				if result, rpcErr := passwords.upgradeRepoPasswordHash(ctx, db, args); rpcErr != nil || result != 0 {
					t.Fatalf("prepare password hash: %v, %v", result, rpcErr)
				}
			}
			reader := NewReader(db, root)
			create := reader.RepoCreationRPCHandlers(db)["create_virtual_repo"]
			args := virtualTestArgs("/team")
			for _, test := range []struct {
				password, message string
				code              int
			}{
				{"", "Password is not set", errBadArgs},
				{newSyntheticPassword, "Incorrect password", errGeneral},
			} {
				args[5], _ = json.Marshal(test.password)
				if result, rpcErr := create(ctx, args); result != nil || rpcErr == nil || rpcErr.Code != test.code || rpcErr.Message != test.message {
					t.Fatalf("unexpected rejected creation: %v, %+v", result, rpcErr)
				}
			}
			if got := virtualTestState(t, db); got != [6]int{1, 1, 1, 1, 1, 0} {
				t.Fatalf("rejected creation published records: %v", got)
			}
			args[5], _ = json.Marshal(oldSyntheticPassword)
			result, rpcErr := create(ctx, args)
			id, ok := result.(string)
			if rpcErr != nil || !ok || !validRepoID(id) {
				t.Fatalf("create encrypted virtual repo: %v, %v", result, rpcErr)
			}
			if repeat, rpcErr := create(ctx, args); repeat != id || rpcErr != nil {
				t.Fatalf("duplicate creation: %v, %v", repeat, rpcErr)
			}
			origin, err := reader.Repo(ctx, testVirtualOrigin)
			if err != nil {
				t.Fatal(err)
			}
			view, err := reader.Repo(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if view.VirtualInfo == nil || view.VirtualInfo.Path != "/team" || view.VirtualInfo.BaseCommitID != origin.HeadCommitID ||
				view.StoreID != origin.ID || !view.IsEncrypted || view.EncVersion != origin.EncVersion ||
				view.RandomKey != origin.RandomKey || view.Salt != origin.Salt || view.PwdHashAlgo != origin.PwdHashAlgo ||
				view.PwdHashParams != origin.PwdHashParams || view.HasPwdHashParams != origin.HasPwdHashParams {
				t.Fatal("virtual repo metadata does not match its origin and projection")
			}
			if view.Magic == origin.Magic && view.PwdHash == origin.PwdHash {
				t.Fatal("virtual repo reused its origin's ID-bound password verifier")
			}
			originKey, originIV, originOK := unlockRepo(origin, oldSyntheticPassword)
			viewKey, viewIV, viewOK := unlockRepo(view, oldSyntheticPassword)
			if !originOK || !viewOK || !bytes.Equal(originKey, viewKey) || !bytes.Equal(originIV, viewIV) {
				t.Fatal("origin and virtual repo did not unlock to the same file key")
			}
		})
	}
}

func TestCreateVirtualRepoRollsBackFailures(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"object-write", "mapping-insert", "db-commit"} {
		t.Run(failure, func(t *testing.T) {
			reader, db, root := virtualTestOrigin(t)
			switch failure {
			case "object-write":
				if os.Geteuid() == 0 {
					t.Skip("filesystem permission injection requires an unprivileged user")
				}
				commits := filepath.Join(root, "storage", "commits")
				if err := os.Chmod(commits, 0o500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(commits, 0o700)
			case "mapping-insert":
				_, err := db.Exec(`CREATE TRIGGER fail_virtual_write BEFORE INSERT ON VirtualRepo
					BEGIN SELECT RAISE(ABORT, 'injected virtual mapping error'); END;`)
				if err != nil {
					t.Fatal(err)
				}
			case "db-commit":
				_, err := db.Exec(`PRAGMA foreign_keys = ON;
					CREATE TABLE VirtualParent (id INTEGER PRIMARY KEY);
					CREATE TABLE VirtualChild (parent_id INTEGER REFERENCES VirtualParent(id) DEFERRABLE INITIALLY DEFERRED);
					CREATE TRIGGER fail_virtual_write AFTER INSERT ON VirtualRepo
					BEGIN INSERT INTO VirtualChild (parent_id) VALUES (1); END;`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if id, rpcErr := reader.createVirtualRepo(ctx, db, virtualTestArgs("/team")); id != nil || rpcErr == nil {
				t.Fatalf("injected %s succeeded: %v, %v", failure, id, rpcErr)
			}
			if got := virtualTestState(t, db); got != [6]int{1, 1, 1, 1, 1, 0} {
				t.Fatalf("failed virtual creation published records: %v", got)
			}
			if failure == "object-write" {
				if err := os.Chmod(filepath.Join(root, "storage", "commits"), 0o700); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Exec("DROP TRIGGER fail_virtual_write"); err != nil {
				t.Fatal(err)
			}
			if id, rpcErr := reader.createVirtualRepo(ctx, db, virtualTestArgs("/team")); id == nil || rpcErr != nil {
				t.Fatalf("create after %s failed: %v, %v", failure, id, rpcErr)
			}
			if got := virtualTestState(t, db); got != [6]int{2, 2, 2, 2, 2, 1} {
				t.Fatalf("retry published unexpected rows: %v", got)
			}
		})
	}
}
