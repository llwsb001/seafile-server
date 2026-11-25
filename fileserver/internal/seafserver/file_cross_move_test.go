package seafserver

import (
	"context"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestCrossLibraryMovePublishesBothHeadsOrNeither(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	create := func(name, owner string) string {
		t.Helper()
		value, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, name, "fixture", owner))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		return value.(string)
	}
	source := create("source", owner)
	target := create("target", owner)
	other := create("other", "other@example.test")
	config := NewServerConfig(db, nil)
	handlers, stop := reader.FileWriteRPCHandlers(ctx, db, nil, false, NewUserQuota(db, config), FileMoveLimits{})
	defer stop()
	if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, source, "/", "transfer.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, err := db.Exec("INSERT INTO SharedRepo (repo_id, from_email, to_email, permission) VALUES (?, ?, ?, 'rw')",
		other, "other@example.test", owner); err != nil {
		t.Fatal(err)
	}
	head := func(id string) string {
		t.Helper()
		repo, err := reader.Repo(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return repo.HeadCommitID
	}
	writer := &fileWriter{reader: reader, db: db, permissions: &permissionReader{db: db}, quota: NewUserQuota(db, config)}
	req := crossLibraryMove{fromRepo: source, toRepo: target, user: owner,
		names: []string{"transfer.txt"}, targets: []string{"transfer.txt"}}
	foreign := req
	foreign.toRepo = other
	if rpcErr := writer.runCrossMove(ctx, foreign, nil); rpcErr == nil || rpcErr.Code != errRepoAuth {
		t.Fatalf("cross-library move with different destination owner returned %v", rpcErr)
	}
	oldSource, oldTarget := head(source), head(target)
	if _, err := db.Exec(`CREATE TABLE MoveWriteFault (repo_id TEXT);
		CREATE TRIGGER fail_source_move BEFORE UPDATE OF commit_id ON Branch
		WHEN NEW.repo_id IN (SELECT repo_id FROM MoveWriteFault)
		BEGIN SELECT RAISE(ABORT, 'fixture branch failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO MoveWriteFault VALUES (?)", source); err != nil {
		t.Fatal(err)
	}
	if rpcErr := writer.runCrossMove(ctx, req, nil); rpcErr == nil {
		t.Fatal("injected branch update unexpectedly succeeded")
	}
	if head(source) != oldSource || head(target) != oldTarget {
		t.Fatal("failed cross-library move published a partial transfer")
	}
	if _, err := db.Exec("DROP TRIGGER fail_source_move"); err != nil {
		t.Fatal(err)
	}
	if rpcErr := writer.runCrossMove(ctx, req, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if head(source) == oldSource || head(target) == oldTarget {
		t.Fatal("successful retry did not move both heads")
	}
}

func TestBackgroundMoveCancellationRetiresTask(t *testing.T) {
	ctx := context.Background()
	queue := newFileMoveQueue(ctx)
	defer queue.close()
	started := make(chan struct{})
	value, rpcErr := queue.submit(true, func(ctx context.Context, _ *fileMoveTask) *searpc.RPCError {
		close(started)
		<-ctx.Done()
		return rpcError(errGeneral, "Internal error when copy or move")
	})
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	<-started
	id := value.(map[string]any)["task-id"].(string)
	args := fileWriteRPCArgs(t, id)
	if status, _ := queue.cancel(ctx, args); status != 0 {
		t.Fatalf("unexpected cancel status %v", status)
	}
	state, _ := queue.get(ctx, args)
	if task := state.(map[string]any); task["canceled"] != true || task["successful"] != false {
		t.Fatalf("unexpected canceled state %v", task)
	}
	if state, _ := queue.get(ctx, args); state != nil {
		t.Fatalf("terminal task remained visible %v", state)
	}
}
