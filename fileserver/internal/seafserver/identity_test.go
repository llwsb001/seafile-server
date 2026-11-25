package seafserver

import (
	"context"
	"database/sql"
	"slices"
	"testing"
)

func TestGroupMembersPreservesSQLitePageOrder(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// When all memberships are in one group, SQLite can choose a table scan.
	_, err = db.ExecContext(ctx, `
CREATE TABLE GroupUser (group_id INTEGER, user_name TEXT, is_staff INTEGER);
CREATE UNIQUE INDEX groupid_username_indx ON GroupUser (group_id, user_name);
CREATE INDEX username_indx ON GroupUser (user_name);
INSERT INTO GroupUser VALUES (1, 'z@example.test', 0), (1, 'a@example.test', 0), (1, 'm@example.test', 0);
ANALYZE;
`)
	if err != nil {
		t.Fatal(err)
	}

	identity := &Identity{groups: db}
	members, err := identity.groupMembers(ctx, 1, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, member := range members {
		got = append(got, *member.User)
	}
	want := []string{"a@example.test", "z@example.test"}
	if !slices.Equal(got, want) {
		t.Fatalf("group members: got %v, want %v", got, want)
	}
}
