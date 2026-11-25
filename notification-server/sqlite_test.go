package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenSQLiteGroupDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "group #fixture.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE GroupUser (group_id INTEGER, user_name TEXT);
		INSERT INTO GroupUser VALUES (7, 'member@example.test');`); err != nil {
		t.Fatal(err)
	}

	groupDB, err := openSQLiteGroupDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer groupDB.Close()

	old := ccnetDB
	ccnetDB = groupDB
	t.Cleanup(func() { ccnetDB = old })
	if got := getGroupMembers(7); len(got) != 1 || !needToNotif(got, "member@example.test") {
		t.Fatalf("unexpected group members %v", got)
	}
	if _, err := groupDB.Exec("INSERT INTO GroupUser VALUES (7, 'other@example.test')"); err == nil {
		t.Fatal("group database accepted a write")
	}
}
