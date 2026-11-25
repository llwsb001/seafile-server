package seafserver

import (
	"context"
	"database/sql"
	"testing"
)

func TestRepoWithoutOwnerDeniesSharing(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const repoID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const user = "reader@example.test"
	_, err = db.ExecContext(ctx, `
CREATE TABLE VirtualRepo (repo_id TEXT, origin_repo TEXT, path TEXT);
CREATE TABLE RepoOwner (repo_id TEXT, owner_id TEXT);
CREATE TABLE SharedRepo (repo_id TEXT, to_email TEXT, permission TEXT);
INSERT INTO SharedRepo VALUES (?, ?, 'rw');`, repoID, user)
	if err != nil {
		t.Fatal(err)
	}

	read := &permissionReader{db: db}
	permission, err := read.check(ctx, repoID, user)
	if err != nil || permission != nil {
		t.Fatalf("unexpected permission for an ownerless repo: %v (err: %v)", permission, err)
	}
}
