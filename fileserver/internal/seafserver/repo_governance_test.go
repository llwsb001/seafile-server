package seafserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

const unknownGovernanceRepo = "99999999-9999-4999-8999-999999999999"

func governanceArgs(t *testing.T, values ...any) []json.RawMessage {
	t.Helper()
	args := make([]json.RawMessage, len(values))
	for i, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		args[i] = data
	}
	return args
}

func TestEditSQLiteRepo(t *testing.T) {
	r, db, root := virtualTestOrigin(t)
	ctx := context.Background()
	edit := r.RepoGovernanceRPCHandlers(db)["seafile_edit_repo"]
	for _, tc := range []struct {
		id, name any
		message  string
	}{
		{testVirtualOrigin, nil, "At least one argument should be non-null"},
		{nil, "name", "Invalid repo id"},
		{unknownGovernanceRepo, "name", "No such library"},
	} {
		if result, err := edit(ctx, governanceArgs(t, tc.id, tc.name, nil, nil)); result != -1 ||
			err == nil || err.Code != errBadArgs || err.Message != tc.message {
			t.Fatalf("edit %v: result %v, error %v", tc.id, result, err)
		}
	}
	original, err := r.Repo(ctx, testVirtualOrigin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE RepoInfo SET status=1, type='custom' WHERE repo_id=?", testVirtualOrigin); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_edit BEFORE UPDATE OF name ON RepoInfo
		BEGIN SELECT RAISE(ABORT, 'forced metadata failure'); END`); err != nil {
		t.Fatal(err)
	}
	if result, err := edit(ctx, governanceArgs(t, testVirtualOrigin, "failed", nil, nil)); result != -1 || err != nil {
		t.Fatalf("failed edit: result %v, error %v", result, err)
	}
	unmodified, err := r.Repo(ctx, testVirtualOrigin)
	if err != nil || unmodified.HeadCommitID != original.HeadCommitID {
		t.Fatalf("branch after rollback: %+v, %v", unmodified, err)
	}
	if paths, err := filepath.Glob(filepath.Join(root, "storage", "commits", testVirtualOrigin, "*", "*")); err != nil || len(paths) != 1 {
		t.Fatalf("commit objects after rollback: %v, %v", paths, err)
	}
	pop := r.EventRPCHandlers()["pop_event"]
	eventArgs := governanceArgs(t, "seaf_server.event")
	if message, err := pop(ctx, eventArgs); message != nil || err != nil {
		t.Fatalf("event after rollback: %v, %v", message, err)
	}
	if _, err := db.Exec("DROP TRIGGER refuse_edit"); err != nil {
		t.Fatal(err)
	}
	if result, err := edit(ctx, governanceArgs(t, testVirtualOrigin, "renamed", "updated description", nil)); result != 0 || err != nil {
		t.Fatalf("rename: result %v, error %v", result, err)
	}
	repo, err := r.Repo(ctx, testVirtualOrigin)
	if err != nil {
		t.Fatal(err)
	}
	head, err := r.Commit(repo.ID, repo.HeadCommitID)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Name != "renamed" || repo.Desc != "updated description" || repo.Status != 1 || repo.Type.String != "custom" ||
		head.ParentID.String != original.HeadCommitID || head.RootID != original.RootID || head.CreatorName != testVirtualOwner ||
		head.Desc != repoEditMessage || head.NoLocalHistory != 1 {
		t.Fatalf("repo after edit: %+v, head: %+v", repo, head)
	}
	var name, modifier string
	var updateTime int64
	if err := db.QueryRow("SELECT name, last_modifier, update_time FROM RepoInfo WHERE repo_id=?", repo.ID).
		Scan(&name, &modifier, &updateTime); err != nil || name != repo.Name || modifier != head.CreatorName || updateTime != head.Ctime {
		t.Fatalf("RepoInfo = %q, %q, %d, %v", name, modifier, updateTime, err)
	}
	message, rpcErr := pop(ctx, eventArgs)
	var notification struct {
		Kind   string `json:"msg_type"`
		Repo   string `json:"repo_id"`
		Commit string `json:"commit_id"`
	}
	value, ok := message.(event)
	if rpcErr != nil || !ok || json.Unmarshal([]byte(value.Content), &notification) != nil ||
		notification.Kind != "repo-update" || notification.Repo != repo.ID || notification.Commit != head.CommitID {
		t.Fatalf("edit notification: %v, %v", message, rpcErr)
	}
}

func TestConcurrentSQLiteRepoEditsPreserveBothChanges(t *testing.T) {
	r, db, _ := virtualTestOrigin(t)
	ctx := context.Background()
	before, err := r.Repo(ctx, testVirtualOrigin)
	if err != nil {
		t.Fatal(err)
	}
	edit := r.RepoGovernanceRPCHandlers(db)["seafile_edit_repo"]
	var group sync.WaitGroup
	for _, args := range [][]json.RawMessage{
		governanceArgs(t, testVirtualOrigin, "parallel name", nil, testVirtualOwner),
		governanceArgs(t, testVirtualOrigin, nil, "parallel description", testVirtualOwner),
	} {
		group.Add(1)
		go func() {
			defer group.Done()
			if result, err := edit(ctx, args); result != 0 || err != nil {
				t.Errorf("concurrent edit: result %v, error %v", result, err)
			}
		}()
	}
	group.Wait()
	repo, err := r.Repo(ctx, testVirtualOrigin)
	if err != nil {
		t.Fatal(err)
	}
	body, err := r.Commit(repo.ID, repo.HeadCommitID)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := r.Commit(repo.ID, body.ParentID.String)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Name != "parallel name" || repo.Desc != "parallel description" ||
		body.CommitID == parent.CommitID || parent.ParentID.String != before.HeadCommitID {
		t.Fatalf("concurrent edits: repo %+v, head %+v, parent %+v", repo, body, parent)
	}
}
