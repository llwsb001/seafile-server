package seafserver

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestListDirWithPermission(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	groupSchema, err := os.ReadFile("../../../scripts/sql/sqlite/groupmgr.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(groupSchema)); err != nil {
		t.Fatal(err)
	}
	const owner, recipient, outsider = "owner@example.test", "reader@example.test", "outsider@example.test"
	value, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "origin", "", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	origin := value.(string)
	handlers, closeTasks := reader.FileWriteRPCHandlers(ctx, db, nil, false,
		NewUserQuota(db, NewServerConfig(db, nil)), NewRepoPasswords(reader), FileMoveLimits{})
	defer closeTasks()
	for _, path := range []string{"docs/team/Inbox", "docset/Private"} {
		if _, rpcErr := handlers["seafile_mkdir_with_parents"](ctx, fileWriteRPCArgs(t, origin, "/", path, owner)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, origin, "/", "A.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, origin, "/docs/team", "draft.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}

	sharedRepo, rpcErr := reader.createDirectoryShare(ctx, db, directoryShare{
		repo: origin, path: "/docs/team", owner: owner, user: recipient, permission: "r",
	}, "", false)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	view := sharedRepo.(string)

	if _, rpcErr := reader.createDirectoryShare(ctx, db, directoryShare{
		repo: origin, path: "/docs", owner: owner, group: 1, permission: "r",
	}, "", true); rpcErr != nil {
		t.Fatal(rpcErr)
	}

	dirID := func(repo, path string) string {
		t.Helper()
		result, rpcErr := reader.dirIDByPath(ctx, fileWriteRPCArgs(t, repo, path))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		return result.(string)
	}
	type item struct {
		name, permission string
		shared           bool
		modifier         any
	}
	root, shared := dirID(origin, "/"), dirID(view, "/")
	handler := reader.PermissionRPCHandlers(&Identity{groups: db}, false)["list_dir_with_perm"]
	for _, tt := range []struct {
		name, repo, user, path, id string
		offset, limit              string
		want                       []item
		errorCode                  int
	}{
		{"owner root", origin, owner, "/", root, "-1", "0", []item{
			{"docs", "rw", true, nil}, {"docset", "rw", false, nil}, {"A.txt", "rw", false, owner},
		}, 0},
		{"owner page", origin, owner, "/", root, "1", "1", []item{{"docset", "rw", false, nil}}, 0},
		{"empty page", origin, owner, "/", root, "15", "-1", nil, 0},
		{"nested readonly", view, recipient, "/", shared, "0", "-1", []item{
			{"Inbox", "r", false, nil}, {"draft.txt", "r", false, owner},
		}, 0},
		{"denied", view, outsider, "/", shared, "0", "1", nil, errBadArgs},
		{"origin tree", view, recipient, "/", root, "0", "1", nil, errBadDirID},
		{"neighbor subtree", view, recipient, "/", dirID(origin, "/docset"), "0", "1", nil, errBadDirID},
		{"incorrect path", view, recipient, "/Private", shared, "0", "1", nil, errBadDirID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := fileWriteRPCArgs(t, tt.repo, tt.path, tt.id, tt.user)
			result, rpcErr := handler(ctx, append(args, json.RawMessage(tt.offset), json.RawMessage(tt.limit)))
			if tt.errorCode != 0 {
				if result != nil || rpcErr == nil || rpcErr.Code != tt.errorCode {
					t.Fatalf("got (%v, %v), want error %d", result, rpcErr, tt.errorCode)
				}
				return
			}
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			var got []item
			for _, entry := range result.([]map[string]any) {
				got = append(got, item{
					entry["obj-name"].(string), entry["permission"].(string),
					entry["is-shared"].(bool), entry["modifier"],
				})
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
