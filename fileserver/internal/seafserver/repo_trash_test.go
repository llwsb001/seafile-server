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
	fileHandlers, closeMoves := reader.FileWriteRPCHandlers(ctx, db, nil, false, nil, nil, FileMoveLimits{})
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

func TestRestoreRepoFromTrashRollsBackAndCanBeRetried(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	id, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "restore", "fixture", "owner@example.test"))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := id.(string)
	repo, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	handlers := reader.RepoTrashWriteRPCHandlers(db, false)
	args := fileWriteRPCArgs(t, repoID)
	if _, rpcErr := handlers["seafile_destroy_repo"](ctx, args); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_restore BEFORE DELETE ON RepoTrash
		BEGIN SELECT RAISE(ABORT, 'fixture restore failure'); END`); err != nil {
		t.Fatal(err)
	}
	restore := handlers["restore_repo_from_trash"]
	if _, rpcErr := restore(ctx, args); rpcErr == nil {
		t.Fatal("metadata failure unexpectedly committed the restore")
	}
	var published int
	err = db.QueryRow(`SELECT (SELECT COUNT(*) FROM Repo WHERE repo_id = ?) +
		(SELECT COUNT(*) FROM Branch WHERE repo_id = ?)`, repoID, repoID).Scan(&published)
	if err != nil || published != 0 {
		t.Fatalf("failed restore left active references: %d, %v", published, err)
	}
	var trashHead string
	err = db.QueryRow("SELECT head_id FROM RepoTrash WHERE repo_id = ?", repoID).Scan(&trashHead)
	if err != nil || trashHead != repo.HeadCommitID {
		t.Fatalf("failed restore lost trash reference: %s, %v", trashHead, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_restore"); err != nil {
		t.Fatal(err)
	}
	if result, rpcErr := restore(ctx, args); rpcErr != nil || result != 0 {
		t.Fatalf("restore retry: %v, %v", result, rpcErr)
	}
	restored, err := reader.Repo(ctx, repoID)
	if err != nil || restored.HeadCommitID != repo.HeadCommitID {
		t.Fatalf("retry did not recover the original head: %v, %v", restored, err)
	}
}
