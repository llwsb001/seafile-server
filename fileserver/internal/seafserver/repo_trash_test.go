package seafserver

import (
	"context"
	"testing"
)

func TestMoveRepoToTrashRollsBackQueueAndBranch(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	id, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "trash", "fixture", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := id.(string)
	fileHandlers, closeMoves := reader.FileWriteRPCHandlers(ctx, db, nil, false, nil, FileMoveLimits{})
	defer closeMoves()
	if _, rpcErr := fileHandlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, repoID, "/", "file", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repo, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_trash AFTER DELETE ON GoFileWriteQueue
		BEGIN SELECT RAISE(ABORT, 'fixture trash failure'); END`); err != nil {
		t.Fatal(err)
	}
	destroy := reader.RepoTrashWriteRPCHandlers(db, false)["seafile_destroy_repo"]
	if _, rpcErr := destroy(ctx, fileWriteRPCArgs(t, repoID)); rpcErr == nil {
		t.Fatal("metadata failure unexpectedly committed the soft delete")
	}
	active, err := reader.Repo(ctx, repoID)
	if err != nil || active.HeadCommitID != repo.HeadCommitID {
		t.Fatalf("failed delete changed active head: %v, %v", active, err)
	}
	pending, err := PendingFileWrites(ctx, db)
	if err != nil || len(pending) != 1 || pending[0].HeadID != repo.HeadCommitID {
		t.Fatalf("failed delete changed queue: %v, %v", pending, err)
	}
	var trashed int
	err = db.QueryRow("SELECT COUNT(*) FROM RepoTrash WHERE repo_id = ?", repoID).Scan(&trashed)
	if err != nil || trashed != 0 {
		t.Fatalf("failed delete left a trash row: %d, %v", trashed, err)
	}

	if _, err := db.Exec("DROP TRIGGER fail_trash"); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := destroy(ctx, fileWriteRPCArgs(t, repoID)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	pending, err = PendingFileWrites(ctx, db)
	if err != nil || len(pending) != 0 {
		t.Fatalf("deleted library stayed queued: %v, %v", pending, err)
	}
	if _, err := reader.Commit(repoID, repo.HeadCommitID); err != nil {
		t.Fatalf("deleted library's head became unreadable: %v", err)
	}
}
