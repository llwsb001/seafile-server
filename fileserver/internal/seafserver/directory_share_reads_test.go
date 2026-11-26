package seafserver

import (
	"context"
	"slices"
	"testing"
)

func TestSubdirShareReadsMatchOriginPathAndSharer(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const first = "a5097f43-19fb-458d-8283-9f47b3b932c2"
	const neighbor = "93485e19-1b4c-4bb4-a4f4-d01ab7c17033"
	_, err := db.Exec(`
		INSERT INTO VirtualRepo(repo_id, origin_repo, path) VALUES
			(?1, ?3, '/docs/team'), (?2, ?3, '/docs/teammate');
		INSERT INTO SharedRepo(repo_id, from_email, to_email, permission) VALUES
			(?1, 'owner', 'First', 'r'), (?1, 'owner', 'Second', 'rw'), (?1, 'another', 'Third', 'r'),
			(?2, 'owner', 'Neighbor', 'rw');
		INSERT INTO RepoGroup(repo_id, group_id, user_name, permission) VALUES
			(?1, 1, 'owner', 'r'), (?1, 2, 'owner', 'rw'), (?1, 3, 'another', 'r'),
			(?2, 4, 'owner', 'rw');`, first, neighbor, testVirtualOrigin)
	if err != nil {
		t.Fatal(err)
	}
	handlers := reader.RPCHandlers(nil)
	users := handlers["seafile_get_shared_users_for_subdir"]
	groups := handlers["seafile_get_shared_groups_for_subdir"]
	checkUsers := func(origin, path, owner string, want ...string) {
		t.Helper()
		result, rpcErr := users(ctx, fileWriteRPCArgs(t, origin, path, owner))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		var got []string
		for _, item := range result.([]sharedUser) {
			got = append(got, item.RepoID+":"+*item.User+":"+*item.Perm)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("shares for %s by %s = %v, want %v", path, owner, got, want)
		}
	}
	checkUsers(testVirtualOrigin, "docs/team///", "owner", first+":Second:rw", first+":First:r")
	checkUsers(testVirtualOrigin, "/docs/team", "another", first+":Third:r")
	checkUsers(testVirtualOrigin, "/docs/teammate", "owner", neighbor+":Neighbor:rw")
	checkUsers(first, "/docs/team", "owner")

	result, rpcErr := groups(ctx, fileWriteRPCArgs(t, testVirtualOrigin, "/docs/team", "owner"))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	items := result.([]sharedGroup)
	if len(items) != 2 || items[0].RepoID != first || items[0].GroupID != 2 || *items[0].Perm != "rw" ||
		items[1].RepoID != first || items[1].GroupID != 1 || *items[1].Perm != "r" {
		t.Fatalf("unexpected groups: %v", items)
	}
	if _, err := db.Exec("UPDATE VirtualRepo SET path = '/docs/new' WHERE repo_id = ?; DELETE FROM SharedRepo WHERE repo_id = ? AND to_email = 'First'", first, first); err != nil {
		t.Fatal(err)
	}
	checkUsers(testVirtualOrigin, "/docs/team", "owner")
	checkUsers(testVirtualOrigin, "/docs/new", "owner", first+":Second:rw")
}
