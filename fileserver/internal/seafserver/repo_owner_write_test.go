package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func TestSQLiteRepoOwnerTransferRollsBackAndCanRetry(t *testing.T) {
	ctx := context.Background()
	r, db, _ := virtualTestOrigin(t)
	create := r.RepoCreationRPCHandlers(db)["create_virtual_repo"]
	value, rpcErr := create(ctx, virtualTestArgs("/team"))
	child, ok := value.(string)
	if !ok || rpcErr != nil {
		t.Fatalf("create child: %v, %v", value, rpcErr)
	}
	const nextOwner = "new-owner@example.test"
	seed := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO SharedRepo VALUES (?, ?, 'user@example.test', 'r')", []any{testVirtualOrigin, testVirtualOwner}},
		{"INSERT INTO RepoGroup VALUES (?, 1, ?, 'rw')", []any{child, testVirtualOwner}},
		{"INSERT INTO InnerPubRepo VALUES (?, 'r'), (?, 'r')", []any{testVirtualOrigin, child}},
		{"INSERT INTO RepoUserToken VALUES (?, ?, 'origin-token'), (?, ?, 'virtual-token')", []any{testVirtualOrigin, testVirtualOwner, child, testVirtualOwner}},
		{"INSERT INTO RepoTokenPeerInfo (token) VALUES ('virtual-token')", nil},
		{"UPDATE RepoFileCount SET file_count=9 WHERE repo_id=?", []any{child}},
		{`CREATE TRIGGER refuse_view_removal BEFORE DELETE ON RepoInfo
			BEGIN SELECT RAISE(ABORT, 'forced virtual cleanup failure'); END`, nil},
	}
	for _, item := range seed {
		if _, err := db.Exec(item.query, item.args...); err != nil {
			t.Fatal(err)
		}
	}
	setOwner := SetRepoOwnerRPC(db, false)
	args := []json.RawMessage{json.RawMessage(`"` + testVirtualOrigin + `"`), json.RawMessage(`"` + nextOwner + `"`)}
	if result, err := setOwner(ctx, args); result != -1 || err == nil || err.Code != errGeneral {
		t.Fatalf("forced cleanup failure: %v, %v", result, err)
	}
	checkOwner := func(id, want string) {
		t.Helper()
		var got string
		err := db.QueryRow("SELECT owner_id FROM RepoOwner WHERE repo_id=?", id).Scan(&got)
		if err != nil || got != want {
			t.Fatalf("owner for %s: %v, %v, want %q", id, got, err, want)
		}
	}
	checkRows := func(table, id string, want int) {
		t.Helper()
		var got int
		err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE repo_id=?", id).Scan(&got)
		if err != nil || got != want {
			t.Fatalf("%s for %s: %v, %v, want %d", table, id, got, err, want)
		}
	}
	checkOwner(testVirtualOrigin, testVirtualOwner)
	checkRows("SharedRepo", testVirtualOrigin, 1)
	checkRows("RepoInfo", child, 1)
	checkRows("GarbageRepos", child, 0)
	if _, err := db.Exec("DROP TRIGGER refuse_view_removal"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if result, err := setOwner(ctx, args); result != 0 || err != nil {
			t.Fatalf("successful retry: %v, %v", result, err)
		}
	}
	checkOwner(testVirtualOrigin, nextOwner)
	checkRows("SharedRepo", testVirtualOrigin, 0)
	checkRows("InnerPubRepo", testVirtualOrigin, 0)
	checkRows("RepoUserToken", testVirtualOrigin, 1)
	for _, table := range []string{"Repo", "RepoHead", "Branch", "RepoOwner", "RepoGroup", "InnerPubRepo",
		"RepoUserToken", "RepoInfo", "VirtualRepo"} {
		checkRows(table, child, 0)
	}
	checkRows("RepoFileCount", child, 1)
	checkRows("GarbageRepos", child, 1)
	var token string
	if err := db.QueryRow("SELECT token FROM RepoTokenPeerInfo").Scan(&token); err != nil || token != "virtual-token" {
		t.Fatalf("legacy peer row after transfer: %q, %v", token, err)
	}
}

func TestSQLiteRepoOwnerInitialAssignmentAndCloudPublicSharing(t *testing.T) {
	ctx := context.Background()
	_, db, _ := virtualTestOrigin(t)
	const id = "99999999-9999-4999-8999-999999999999"
	if _, err := db.Exec("INSERT INTO InnerPubRepo VALUES (?, 'r')", id); err != nil {
		t.Fatal(err)
	}
	set := SetRepoOwnerRPC(db, true)
	for _, owner := range []string{"OLD@example.test", "old@example.test", "new@example.test"} {
		args := []json.RawMessage{json.RawMessage(`"` + id + `"`), json.RawMessage(`"` + owner + `"`)}
		if result, err := set(ctx, args); result != 0 || err != nil {
			t.Fatalf("assign %q: %v, %v", owner, result, err)
		}
	}
	var owner string
	if err := db.QueryRow("SELECT owner_id FROM RepoOwner WHERE repo_id=?", id).Scan(&owner); err != nil || owner != "new@example.test" {
		t.Fatalf("owner = %q, %v", owner, err)
	}
	var policy sql.NullString
	if err := db.QueryRow("SELECT permission FROM InnerPubRepo WHERE repo_id=?", id).Scan(&policy); err != nil || policy.String != "r" {
		t.Fatalf("cloud public permission = %v, %v", policy, err)
	}
}
