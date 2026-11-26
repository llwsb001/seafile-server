package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSetVirtualBaseCommitPreservesMappingAndQueuedWork(t *testing.T) {
	ctx := context.Background()
	reader, db, root := virtualTestOrigin(t)
	created, rpcErr := reader.createVirtualRepo(ctx, db, virtualTestArgs("/team"))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	id := created.(string)
	view, err := reader.Repo(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := reader.Repo(ctx, testVirtualOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureFileWriteQueue(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO GoFileWriteQueue (repo_id, head_id) VALUES (?, ?)", id, view.HeadCommitID); err != nil {
		t.Fatal(err)
	}
	queue := []FileWriteJob{{RepoID: id, HeadID: view.HeadCommitID}}
	assertState := func(pool *sql.DB, expectedBase string) {
		t.Helper()
		current := NewReader(pool, root)
		gotView, err := current.Repo(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		info := *view.VirtualInfo
		info.BaseCommitID = expectedBase
		if gotView.VirtualInfo == nil || *gotView.VirtualInfo != info || gotView.HeadCommitID != view.HeadCommitID {
			t.Fatalf("changed view outside its base: %+v, head %s", gotView.VirtualInfo, gotView.HeadCommitID)
		}
		gotOrigin, err := current.Repo(ctx, origin.ID)
		if err != nil {
			t.Fatal(err)
		}
		if gotOrigin.HeadCommitID != origin.HeadCommitID {
			t.Fatalf("changed origin head: %s", gotOrigin.HeadCommitID)
		}
		pending, err := PendingFileWrites(ctx, pool)
		if err != nil || !slices.Equal(pending, queue) {
			t.Fatalf("changed pending publications: %v, %v", pending, err)
		}
	}

	handler := SetVirtualBaseCommitRPC(db)
	for _, base := range []string{strings.Repeat("a", 40), emptyObjectID, emptyObjectID} {
		if result, err := handler(ctx, virtualBaseCommitArgs(id, base)); result != 0 || err != nil {
			t.Fatalf("set %s: %v, %v", base, result, err)
		}
		assertState(db, base)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteReadWrite(filepath.Join(root, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertState(reopened, emptyObjectID)
	if result, err := SetVirtualBaseCommitRPC(reopened)(ctx, virtualBaseCommitArgs(id, origin.HeadCommitID)); result != 0 || err != nil {
		t.Fatalf("restore actual base: %v, %v", result, err)
	}
	assertState(reopened, origin.HeadCommitID)
}

func TestSetVirtualBaseCommitRejectsInvalidArgumentsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	reader, db, _ := virtualTestOrigin(t)
	created, rpcErr := reader.createVirtualRepo(ctx, db, virtualTestArgs("/team"))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	id := created.(string)
	handler := SetVirtualBaseCommitRPC(db)
	cases := []struct {
		name, id, base, message string
	}{
		{"missing ID", "", emptyObjectID, "Invalid arguments"},
		{"short commit", id, "abcdef", "Invalid arguments"},
		{"uppercase commit", id, strings.Repeat("A", 40), "Invalid arguments"},
		{"ordinary repo", testVirtualOrigin, emptyObjectID, "Virtual repo not found"},
		{"unknown repo", "a0000000-b000-4000-a000-000000000000", emptyObjectID, "Virtual repo not found"},
	}
	checkBase := func() string {
		t.Helper()
		var current string
		if err := db.QueryRow("SELECT base_commit FROM VirtualRepo WHERE repo_id = ?", id).Scan(&current); err != nil {
			t.Fatal(err)
		}
		return current
	}
	before := checkBase()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result, err := handler(ctx, virtualBaseCommitArgs(test.id, test.base))
			if result != -1 || err == nil || err.Code != errBadArgs || err.Message != test.message {
				t.Fatalf("set base: %v, %v", result, err)
			}
			if after := checkBase(); after != before {
				t.Fatalf("invalid request changed base to %s", after)
			}
		})
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_base_commit BEFORE UPDATE OF base_commit ON VirtualRepo
		BEGIN SELECT RAISE(ABORT, 'injected update failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if result, err := handler(ctx, virtualBaseCommitArgs(id, emptyObjectID)); result != -1 || err == nil || err.Code != errGeneral {
		t.Fatalf("update failure: %v, %v", result, err)
	}
	if after := checkBase(); after != before {
		t.Fatalf("failed update changed base to %s", after)
	}
}

func virtualBaseCommitArgs(id, base string) []json.RawMessage {
	idJSON, _ := json.Marshal(id)
	baseJSON, _ := json.Marshal(base)
	return []json.RawMessage{idJSON, baseJSON}
}
