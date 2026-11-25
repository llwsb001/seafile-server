package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAccountRenameRollback(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	corePath := filepath.Join(root, "seafile.db")
	groupPath := filepath.Join(root, "GroupMgr", "groupmgr.db")
	userPath := filepath.Join(root, "PeerMgr", "usermgr.db")
	for _, p := range []string{groupPath, userPath} {
		if err := os.Mkdir(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	open := func(path, schema string) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
		return db
	}
	schema, err := os.ReadFile("../../../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	core := open(corePath, string(schema)+`
		INSERT INTO RepoOwner (repo_id, owner_id) VALUES ('repo', 'before@example.test');
		INSERT INTO RepoUserToken (repo_id, email, token) VALUES ('repo', 'before@example.test', 'synthetic-token');
		INSERT INTO UserQuota (user, quota) VALUES ('before@example.test', 123);
		CREATE TRIGGER reject_rename BEFORE UPDATE ON UserQuota
		BEGIN SELECT RAISE(ABORT, 'fixture rename failure'); END;`)
	groups := open(groupPath, `
		CREATE TABLE GroupUser (group_id INTEGER, user_name TEXT, is_staff INTEGER);
		INSERT INTO GroupUser VALUES (1, 'before@example.test', 0);`)
	users := open(userPath, `
		CREATE TABLE EmailUser (id INTEGER PRIMARY KEY, email TEXT UNIQUE);
		INSERT INTO EmailUser VALUES (1, 'before@example.test');
		CREATE TABLE UserRole (email TEXT, role TEXT, UNIQUE (email, role));
		INSERT INTO UserRole VALUES ('before@example.test', 'librarian');`)
	accounts, err := OpenAccounts(root, corePath)
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	args := []json.RawMessage{json.RawMessage(`"before@example.test"`), json.RawMessage(`"after@example.test"`)}
	assertReferences := func(want string) {
		t.Helper()
		for _, reference := range []struct {
			db    *sql.DB
			query string
		}{
			{core, "SELECT owner_id FROM RepoOwner WHERE repo_id = 'repo'"},
			{core, "SELECT email FROM RepoUserToken WHERE token = 'synthetic-token'"},
			{groups, "SELECT user_name FROM GroupUser WHERE group_id = 1"},
			{users, "SELECT email FROM EmailUser WHERE id = 1"},
			{users, "SELECT email FROM UserRole WHERE role = 'librarian'"},
		} {
			var actual string
			if err := reference.db.QueryRow(reference.query).Scan(&actual); err != nil || actual != want {
				t.Fatalf("%s = %q, want %q (%v)", reference.query, actual, want, err)
			}
		}
	}
	if result, _ := accounts.rename(ctx, args); result != -1 {
		t.Fatalf("rename returned %v before dropping the trigger", result)
	}
	assertReferences("before@example.test")
	if _, err := core.Exec("DROP TRIGGER reject_rename"); err != nil {
		t.Fatal(err)
	}
	if result, _ := accounts.rename(ctx, args); result != 0 {
		t.Fatalf("rename retry returned %v", result)
	}
	assertReferences("after@example.test")
}
