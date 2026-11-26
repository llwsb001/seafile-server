package seafserver

import (
	"context"
	"os"
	"testing"
)

func TestVirtualRepoReadsUsePersistedOwnerAndPath(t *testing.T) {
	ctx := context.Background()
	r, db, _ := virtualTestOrigin(t)
	schema, err := os.ReadFile("../../../scripts/sql/sqlite/groupmgr.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	const recipient = "reader@example.test"
	handlers, closeTasks := r.FileWriteRPCHandlers(ctx, db, nil, false,
		NewUserQuota(db, NewServerConfig(db, nil)), NewRepoPasswords(r), FileMoveLimits{})
	defer closeTasks()
	if _, rpcErr := handlers["seafile_mkdir_with_parents"](ctx,
		fileWriteRPCArgs(t, testVirtualOrigin, "/", "another", testVirtualOwner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	first, rpcErr := r.createVirtualRepo(ctx, db, virtualTestArgs("/team"))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	second, rpcErr := r.createDirectoryShare(ctx, db, directoryShare{
		repo: testVirtualOrigin, path: "/another", owner: testVirtualOwner,
		user: recipient, permission: "r",
	}, "", false)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}

	// Historical installations can hold multiple owned views for one path.
	if _, err := db.Exec("UPDATE RepoOwner SET owner_id = ? WHERE repo_id = ?", recipient, second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE VirtualRepo SET path = '/team' WHERE repo_id = ?", second); err != nil {
		t.Fatal(err)
	}

	list := r.PermissionRPCHandlers(&Identity{groups: db}, false)["get_virtual_repos_by_owner"]
	lookup := r.RPCHandlers(nil)["get_virtual_repo"]
	checkLookup := func(path, owner string, want any) {
		t.Helper()
		result, rpcErr := lookup(ctx, fileWriteRPCArgs(t, testVirtualOrigin, path, owner))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		var got any
		if result != nil {
			got = result.(map[string]any)["id"]
		}
		if got != want {
			t.Fatalf("lookup (%q, %q) = %v, want %v", path, owner, got, want)
		}
	}
	checkLookup("team///", testVirtualOwner, first)
	checkLookup("/team", recipient, second)
	checkLookup("/team", "absent@example.test", nil)
	result, rpcErr := list(ctx, fileWriteRPCArgs(t, recipient))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repos := result.([]map[string]any)
	if len(repos) != 1 || repos[0]["id"] != second || repos[0]["origin-repo-name"] != "origin" ||
		repos[0]["origin-path"] != "/team" || repos[0]["is-original-owner"] != false {
		t.Fatalf("unexpected virtual libraries: %v", repos)
	}
	if permission, _ := repos[0]["virtual-perm"].(*string); permission == nil || *permission != "r" {
		t.Fatalf("unexpected virtual permission: %v", permission)
	}
	if _, err := db.Exec("UPDATE VirtualRepo SET path = '/renamed' WHERE repo_id = ?", second); err != nil {
		t.Fatal(err)
	}
	checkLookup("/team", recipient, nil)
	checkLookup("/renamed", recipient, second)
}
