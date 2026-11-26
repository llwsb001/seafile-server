package seafserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestFileRestoreWithNewParentsPublishesAtomically(t *testing.T) {
	t.Setenv("ENABLE_NOTIFICATION_SERVER", "false")
	ctx := context.Background()
	db, dataDir := openFileWriteFixture(t)
	reader := NewReader(db, dataDir)
	const owner = "owner@example.test"
	id, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "restore fixture", "", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := id.(string)
	handlers, closeMoves := reader.FileWriteRPCHandlers(ctx, db, nil, false, nil, nil, FileMoveLimits{})
	defer closeMoves()
	call := func(method string, values ...string) (any, *searpc.RPCError) {
		return handlers[method](ctx, fileWriteRPCArgs(t, values...))
	}
	mustCall := func(method string, values ...string) {
		t.Helper()
		if ret, rpcErr := call(method, values...); ret != 0 || rpcErr != nil {
			t.Fatalf("%s = %v, %v", method, ret, rpcErr)
		}
	}
	head := func() string {
		t.Helper()
		repo, err := reader.Repo(ctx, repoID)
		if err != nil {
			t.Fatal(err)
		}
		return repo.HeadCommitID
	}
	mustCall("seafile_mkdir_with_parents", repoID, "/", "one/two", owner)
	mustCall("seafile_post_empty_file", repoID, "/one/two", "empty.txt", owner)
	historical := head()
	mustCall("seafile_del_file", repoID, "/", `["one"]`, owner)
	before := head()

	if _, err := db.Exec(`CREATE TRIGGER fail_restore_queue BEFORE UPDATE ON GoFileWriteQueue
		BEGIN SELECT RAISE(ABORT, 'restore queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ret, rpcErr := call("seafile_revert_file", repoID, historical, "/one/two/empty.txt", owner); ret != -1 || rpcErr == nil {
		t.Fatalf("restore with failing queue = %v, %v", ret, rpcErr)
	}
	if after := head(); after != before {
		t.Fatalf("failed restore published %s, want previous head %s", after, before)
	}
	if queued, err := PendingFileWrites(ctx, db); err != nil || len(queued) != 1 || queued[0].HeadID != before {
		t.Fatalf("failed restore replaced queue work: %v, %v", queued, err)
	}
	popEvent := reader.EventRPCHandlers()["pop_event"]
	eventArgs := fileWriteRPCArgs(t, "seaf_server.event")
	if value, rpcErr := popEvent(ctx, eventArgs); value != nil || rpcErr != nil {
		t.Fatalf("failed restore emitted an event: %v, %v", value, rpcErr)
	}
	if _, err := db.Exec("DROP TRIGGER fail_restore_queue"); err != nil {
		t.Fatal(err)
	}
	mustCall("seafile_revert_file", repoID, historical, "/one/two/empty.txt", owner)
	restored, err := reader.Commit(repoID, head())
	if err != nil {
		t.Fatal(err)
	}
	mkdir, err := reader.Commit(repoID, restored.ParentID.String)
	if err != nil || mkdir.Desc != `Added directory "one/two"` || mkdir.ParentID.String != before {
		t.Fatalf("restore mkdir ancestry = %v, %v", mkdir, err)
	}
	for _, commit := range []string{mkdir.CommitID, restored.CommitID} {
		value, rpcErr := popEvent(ctx, eventArgs)
		message, ok := value.(event)
		if !ok || rpcErr != nil {
			t.Fatalf("missing committed restore event: %v, %v", value, rpcErr)
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(message.Content), &payload); err != nil || payload["commit_id"] != commit {
			t.Fatalf("restore event = %s, want commit %s", message.Content, commit)
		}
	}
	value, rpcErr := reader.fileIDByPath(ctx, fileWriteRPCArgs(t, repoID, "/one/two/empty.txt"))
	if value != emptyObjectID || rpcErr != nil {
		t.Fatalf("restored file = %v, %v", value, rpcErr)
	}
}
