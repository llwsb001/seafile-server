package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSQLiteGroupMutationRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "groupmgr.db")
	setup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	if _, err := setup.ExecContext(ctx, `
CREATE TABLE "Group" (group_id INTEGER PRIMARY KEY AUTOINCREMENT, group_name TEXT,
                      creator_name TEXT, timestamp INTEGER, parent_group_id INTEGER);
CREATE TABLE GroupUser (group_id INTEGER, user_name TEXT, is_staff INTEGER);
CREATE TABLE GroupStructure (group_id INTEGER PRIMARY KEY, path TEXT);
CREATE TABLE WriteFault (operation TEXT PRIMARY KEY, enabled INTEGER);
INSERT INTO WriteFault VALUES ('insert', 1), ('delete', 1);
CREATE TRIGGER fail_insert_path BEFORE INSERT ON GroupStructure
WHEN (SELECT enabled FROM WriteFault WHERE operation = 'insert') = 1
BEGIN SELECT RAISE(ABORT, 'fixture failure while writing group path'); END;
CREATE TRIGGER fail_remove_path BEFORE DELETE ON GroupStructure
WHEN (SELECT enabled FROM WriteFault WHERE operation = 'delete') = 1
BEGIN SELECT RAISE(ABORT, 'fixture failure while removing group path'); END;
`); err != nil {
		t.Fatal(err)
	}
	db, err := OpenSQLiteReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler := &GroupManagement{groups: db}
	create := []json.RawMessage{json.RawMessage(`"Team"`), json.RawMessage(`"Owner"`), json.RawMessage("null"), json.RawMessage("-1")}
	remove := []json.RawMessage{json.RawMessage("1")}

	checkState := func(want int) {
		t.Helper()
		var groups, members, paths int
		const query = `SELECT (SELECT COUNT(*) FROM "Group"),
			(SELECT COUNT(*) FROM GroupUser), (SELECT COUNT(*) FROM GroupStructure)`
		if err := db.QueryRowContext(ctx, query).Scan(&groups, &members, &paths); err != nil {
			t.Fatal(err)
		}
		if groups != want || members != want || paths != want {
			t.Fatalf("rows in Group, GroupUser and GroupStructure = (%d, %d, %d), want %d in each", groups, members, paths, want)
		}
	}

	if value, failure := handler.createGroup(ctx, create); value != -1 || failure == nil || failure.Message != "Failed to create group" {
		t.Fatalf("create with trigger returned (%v, %v)", value, failure)
	}
	checkState(0)

	if _, err := setup.ExecContext(ctx, "UPDATE WriteFault SET enabled = 0 WHERE operation = 'insert'"); err != nil {
		t.Fatal(err)
	}
	if value, failure := handler.createGroup(ctx, create); value != int64(1) || failure != nil {
		t.Fatalf("retry create returned (%v, %v)", value, failure)
	}
	checkState(1)

	if value, failure := handler.removeGroup(ctx, remove); value != -1 || failure == nil || failure.Message != "Failed to remove group" {
		t.Fatalf("remove with trigger returned (%v, %v)", value, failure)
	}
	checkState(1)
	if _, err := setup.ExecContext(ctx, "UPDATE WriteFault SET enabled = 0 WHERE operation = 'delete'"); err != nil {
		t.Fatal(err)
	}
	if value, failure := handler.removeGroup(ctx, remove); value != 0 || failure != nil {
		t.Fatalf("retry remove returned (%v, %v)", value, failure)
	}
	checkState(0)
}
