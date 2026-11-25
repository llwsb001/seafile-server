package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRemoveOrgResolvesMemberEmail(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	open := func(name, schema string) (*sql.DB, string) {
		t.Helper()
		path := filepath.Join(dir, name)
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if _, err := db.ExecContext(ctx, schema); err != nil {
			t.Fatal(err)
		}
		return db, path
	}
	_, orgPath := open("orgmgr.db", `
CREATE TABLE Organization (org_id INTEGER PRIMARY KEY);
CREATE TABLE OrgGroup (org_id INTEGER, group_id INTEGER);
CREATE TABLE OrgUser (org_id INTEGER, email TEXT);
INSERT INTO Organization VALUES (1), (2);
INSERT INTO OrgUser VALUES (1, 'Reader@example.test'), (1, 'Shared@example.test'),
                           (2, 'shared@example.test'), (1, 'Legacy@example.test');
`)
	_, groupPath := open("groupmgr.db", `
CREATE TABLE "Group" (group_id INTEGER, parent_group_id INTEGER);
CREATE TABLE GroupUser (group_id INTEGER);
CREATE TABLE GroupStructure (group_id INTEGER);
`)
	users, userPath := open("usermgr.db", `
CREATE TABLE EmailUser (email TEXT PRIMARY KEY);
CREATE TABLE UserRole (email TEXT, role TEXT);
INSERT INTO EmailUser VALUES ('reader@example.test'), ('shared@example.test'),
                             ('Legacy@example.test'), ('legacy@example.test');
INSERT INTO UserRole SELECT email, 'librarian' FROM EmailUser;
`)
	handler := &Organizations{orgPath: orgPath, groupPath: groupPath, userPath: userPath}
	if result, err := handler.removeOrg(ctx, []json.RawMessage{json.RawMessage("1")}); result != 0 || err != nil {
		t.Fatalf("remove_org returned (%v, %v)", result, err)
	}
	for email, want := range map[string]int{
		"reader@example.test": 0, "shared@example.test": 1,
		"Legacy@example.test": 0, "legacy@example.test": 1,
	} {
		var accounts, roles int
		const query = `SELECT (SELECT COUNT(*) FROM EmailUser WHERE email = ?1),
			(SELECT COUNT(*) FROM UserRole WHERE email = ?1)`
		if err := users.QueryRowContext(ctx, query, email).Scan(&accounts, &roles); err != nil {
			t.Fatal(err)
		}
		if accounts != want || roles != want {
			t.Errorf("%s: found %d accounts and %d roles, want %d of each", email, accounts, roles, want)
		}
	}
}
