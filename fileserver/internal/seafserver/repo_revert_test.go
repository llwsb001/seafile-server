package seafserver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRepoRevertTimeWithFixedPOSIXOffset(t *testing.T) {
	for _, test := range []struct {
		tz, want string
	}{
		{"UTC-8", "1970-01-01 08:00:00"},
		{"GMT+05:30:15", "1969-12-31 18:29:45"},
		{"UTC+0", "1970-01-01 00:00:00"},
	} {
		t.Run(test.tz, func(t *testing.T) {
			t.Setenv("TZ", test.tz)
			if got := repoRevertTime(0).Format(time.DateTime); got != test.want {
				t.Fatalf("rollback timestamp = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRepoRevertPreservesConcurrentPublicationAndRequiresWriteAccess(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	const readOnly = "reader@example.test"
	const outsider = "outsider@example.test"
	id, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "rollback", "fixture", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := id.(string)
	if _, err := db.Exec(`
		CREATE TABLE "Group" (group_id INTEGER PRIMARY KEY, group_name TEXT,
			creator_name TEXT, timestamp INTEGER, parent_group_id INTEGER);
		CREATE TABLE GroupUser (group_id INTEGER, user_name TEXT);
		CREATE TABLE GroupStructure (group_id INTEGER PRIMARY KEY, path TEXT)
	`); err != nil {
		t.Fatal(err)
	}
	writer := &fileWriter{reader: reader, db: db, permissions: &permissionReader{db: db, identity: &Identity{groups: db}}}
	initial, rpcErr := writer.writableTree(ctx, repoID, owner, true)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	args := fileWriteRPCArgs(t, repoID, initial.repo.HeadCommitID, owner)
	if _, err := db.Exec("INSERT INTO SharedRepo (repo_id, from_email, to_email, permission) VALUES (?, ?, ?, 'r')", repoID, owner, readOnly); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{readOnly, outsider} {
		if _, rpcErr := writer.revertRepo(ctx, fileWriteRPCArgs(t, repoID, initial.repo.HeadCommitID, user)); rpcErr == nil || rpcErr.Code != errRepoAuth {
			t.Fatalf("non-writable user returned %v", rpcErr)
		}
	}
	if _, err := db.Exec("UPDATE RepoInfo SET status = 1 WHERE repo_id = ?", repoID); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := writer.revertRepo(ctx, args); rpcErr == nil || rpcErr.Code != errRepoLocked {
		t.Fatalf("read-only repository returned %v", rpcErr)
	}
	if _, err := db.Exec("UPDATE RepoInfo SET status = 0 WHERE repo_id = ?", repoID); err != nil {
		t.Fatal(err)
	}
	if rpcErr := writer.update(ctx, repoID, owner, []string{repoID, "/", "upload.txt", owner}, createFileEntry(false)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	uploaded, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.publish(ctx, initial, "stale repository revert"); !errors.Is(err, ErrSQLiteBranchChanged) {
		t.Fatalf("stale revert publication error %v", err)
	}
	if _, rpcErr := writer.revertRepo(ctx, args); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	restored, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := reader.Commit(repoID, restored.HeadCommitID)
	if err != nil {
		t.Fatal(err)
	}
	if commit.ParentID.ValueOrZero() != uploaded.HeadCommitID || commit.RootID != initial.root {
		t.Fatalf("revert parent or root changed: %v", commit)
	}
	pending, err := PendingFileWrites(ctx, db)
	if err != nil || len(pending) != 1 || pending[0].HeadID != restored.HeadCommitID {
		t.Fatalf("revert did not queue its published head: %v, %v", pending, err)
	}
}
