package seafserver

import (
	"context"
	"testing"
)

func TestFileWriteQueueRollsBackAndKeepsNewerWork(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	id, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "file write queue", "fixture", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := id.(string)
	handlers, closeMoves := reader.FileWriteRPCHandlers(ctx, db, nil, false, nil, NewRepoPasswords(reader), FileMoveLimits{})
	defer closeMoves()
	write := handlers["seafile_post_empty_file"]
	if _, rpcErr := write(ctx, fileWriteRPCArgs(t, repoID, "/", "first.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	pending, err := PendingFileWrites(ctx, db)
	if err != nil || len(pending) != 1 || pending[0].RepoID != repoID {
		t.Fatalf("unexpected queued write %v, error %v", pending, err)
	}
	old := pending[0]

	if _, err := db.Exec(`CREATE TRIGGER fail_queue_update BEFORE UPDATE ON GoFileWriteQueue
		BEGIN SELECT RAISE(ABORT, 'fixture queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := write(ctx, fileWriteRPCArgs(t, repoID, "/", "rejected.txt", owner)); rpcErr == nil {
		t.Fatal("queue failure unexpectedly committed the write")
	}
	repo, err := reader.Repo(ctx, repoID)
	if err != nil || repo.HeadCommitID != old.HeadID {
		t.Fatalf("queue failure changed the head: %v, error %v", repo, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_queue_update"); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := write(ctx, fileWriteRPCArgs(t, repoID, "/", "second.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if err := FinishFileWrite(ctx, db, old); err != nil {
		t.Fatal(err)
	}
	pending, err = PendingFileWrites(ctx, db)
	if err != nil || len(pending) != 1 || pending[0].RepoID != repoID || pending[0].HeadID == old.HeadID {
		t.Fatalf("acknowledging an older head removed newer work: %v, error %v", pending, err)
	}
	if err := FinishFileWrite(ctx, db, pending[0]); err != nil {
		t.Fatal(err)
	}
	pending, err = PendingFileWrites(ctx, db)
	if err != nil || len(pending) != 0 {
		t.Fatalf("processed write stayed queued: %v, error %v", pending, err)
	}
}
