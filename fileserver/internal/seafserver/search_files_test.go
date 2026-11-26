package seafserver

import (
	"context"
	"slices"
	"testing"
)

func TestFileSearchStaysInRequestedSubtree(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	value, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "search", "", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	origin := value.(string)
	writes, closeTasks := reader.FileWriteRPCHandlers(ctx, db, nil, false,
		NewUserQuota(db, NewServerConfig(db, nil)), NewRepoPasswords(reader), FileMoveLimits{})
	defer closeTasks()
	for _, path := range []string{"shared/nested", "shared-other"} {
		if _, rpcErr := writes["seafile_mkdir_with_parents"](ctx,
			fileWriteRPCArgs(t, origin, "/", path, owner)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if _, rpcErr := writes["seafile_post_empty_file"](ctx,
			fileWriteRPCArgs(t, origin, "/"+path, "Needle.txt", owner)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	if _, rpcErr := writes["seafile_post_empty_file"](ctx,
		fileWriteRPCArgs(t, origin, "/", "Façade.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	viewID, rpcErr := reader.createVirtualRepo(ctx, db,
		fileWriteRPCArgs(t, origin, "/shared/nested", "nested", "", owner, ""))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	view := viewID.(string)

	reads := reader.RPCHandlers(nil)
	for _, tt := range []struct {
		method string
		args   []string
		want   []string
	}{
		{"search_files", []string{origin, "nEeDlE"}, []string{"/shared/nested/Needle.txt", "/shared-other/Needle.txt"}},
		{"search_files_by_path", []string{origin, "/shared", "needle"}, []string{"/shared/nested/Needle.txt"}},
		{"search_files_by_path", []string{origin, "/shared-other/", "needle"}, []string{"/shared-other//Needle.txt"}},
		{"search_files_by_path", []string{origin, "/shared/not-found", ""}, nil},
		{"search_files", []string{origin, "ç"}, []string{"/Façade.txt"}},
		{"search_files", []string{origin, "Ç"}, nil},
		{"search_files", []string{view, "needle"}, []string{"/Needle.txt"}},
		{"search_files_by_path", []string{view, "/shared-other", "needle"}, nil},
	} {
		result, rpcErr := reads[tt.method](ctx, fileWriteRPCArgs(t, tt.args...))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		var paths []string
		if result != nil {
			for _, item := range result.([]fileSearchResult) {
				paths = append(paths, item.Path)
			}
		}
		if !slices.Equal(paths, tt.want) {
			t.Fatalf("%s(%v) = %v, want %v", tt.method, tt.args, paths, tt.want)
		}
	}
}
