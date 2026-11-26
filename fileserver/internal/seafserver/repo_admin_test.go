package seafserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const (
	adminFirst   = "0323aa00-0000-4000-8000-000000000001"
	adminSecond  = "0323aa00-0000-4000-8000-000000000002"
	adminVirtual = "0323aa00-0000-4000-8000-000000000003"
	adminMissing = "0323aa00-0000-4000-8000-000000000004"
	adminBroken  = "0323aa00-0000-4000-8000-000000000005"
	adminStale   = "0323aa00-0000-4000-8000-000000000006"
	adminUnicode = "0323aa00-0000-4000-8000-000000000007"
	adminHead    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	adminBackup  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func adminRepoFixture(t *testing.T) *Reader {
	t.Helper()
	db, root := openFileWriteFixture(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, repo := range []struct {
		id, name              string
		modified, size, files int
	}{
		{adminFirst, "Alpha 层", 200, 5, 2},
		{adminSecond, "aLPHa 层 duplicate", 200, 20, 1},
		{adminVirtual, "Alpha virtual", 300, 99, 22},
		{adminMissing, "", 0, 0, 0},
		{adminBroken, "Alpha no head", 500, 50, 18},
		{adminStale, "Alpha vanished", 800, 100, 20},
		{adminUnicode, "ÄLPHa 层 deprecated", 100, 20, 25},
	} {
		if repo.id != adminStale {
			exec("INSERT INTO Repo(repo_id) VALUES (?)", repo.id)
		}
		if repo.name != "" {
			exec("INSERT INTO RepoInfo VALUES (?, ?, ?, 1, 1, 'Fixture@Example.com', 1, 'wiki')",
				repo.id, repo.name, repo.modified)
			exec("INSERT INTO RepoSize(repo_id, size) VALUES (?, ?)", repo.id, repo.size)
			exec("INSERT INTO RepoFileCount(repo_id, file_count) VALUES (?, ?)", repo.id, repo.files)
		}
		if repo.id != adminBroken {
			exec("INSERT INTO Branch VALUES ('master', ?, ?)", repo.id, adminHead)
		}
	}
	exec("INSERT INTO Branch VALUES ('backup', ?, ?)", adminFirst, adminBackup)
	exec("INSERT INTO VirtualRepo(repo_id, origin_repo, path) VALUES (?, ?, '/Archive')", adminVirtual, adminFirst)
	return NewReader(db, root)
}

func callAdminRepoRPC(t *testing.T, reader *Reader, method string, args ...any) (any, *searpc.RPCError) {
	t.Helper()
	params := make([]json.RawMessage, len(args))
	for i, arg := range args {
		payload, err := json.Marshal(arg)
		if err != nil {
			t.Fatal(err)
		}
		params[i] = payload
	}
	handler := reader.AdminRepoReadRPCHandlers()[method]
	if handler == nil {
		t.Fatalf("%s is missing", method)
	}
	return handler(context.Background(), params)
}

func listAdminRepoRPC(t *testing.T, reader *Reader, method string, args ...any) []map[string]any {
	t.Helper()
	value, rpcErr := callAdminRepoRPC(t, reader, method, args...)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func checkAdminRepoIDs(t *testing.T, rows []map[string]any, want []string) {
	t.Helper()
	var got []string
	for _, row := range rows {
		got = append(got, fmt.Sprint(row["id"]))
	}
	if !slices.Equal(got, want) || (want == nil && rows != nil) {
		t.Fatalf("repository rows = %v, want %v (empty lists encode as null)", got, want)
	}
}

func TestAdminRepoInventory(t *testing.T) {
	reader := adminRepoFixture(t)
	value, rpcErr := callAdminRepoRPC(t, reader, "seafile_count_repos")
	if rpcErr != nil || value != int64(6) {
		t.Fatalf("count = %v, %v", value, rpcErr)
	}

	const list = "seafile_get_repo_list"
	for _, test := range []struct {
		name string
		args []any
		want []string
	}{
		{"all", []any{-1, -1, nil, 0}, []string{adminBroken, adminFirst, adminFirst, adminSecond, adminUnicode}},
		{"virtual", []any{-1, -1, nil, 1}, []string{adminBroken, adminVirtual, adminFirst, adminFirst, adminSecond, adminUnicode}},
		{"fallback sort", []any{-1, -1, "SIZE", 0}, []string{adminBroken, adminFirst, adminFirst, adminSecond, adminUnicode}},
		{"size", []any{0, 4, "size", 0}, []string{adminBroken, adminSecond, adminUnicode, adminFirst}},
		{"file count", []any{0, 4, "file_count", 0}, []string{adminUnicode, adminBroken, adminFirst, adminFirst}},
		{"page one", []any{0, 2, nil, 0}, []string{adminBroken, adminFirst}},
		{"page two", []any{2, 2, nil, 0}, []string{adminFirst, adminSecond}},
		{"empty page", []any{99, 2, nil, 0}, nil},
		{"zero limit", []any{0, 0, nil, 0}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkAdminRepoIDs(t, listAdminRepoRPC(t, reader, list, test.args...), test.want)
		})
	}
	rows := listAdminRepoRPC(t, reader, list, -1, -1, nil, 1)
	if got := rows[0]; got["is-corrupted"] != true || got["head-cmmt-id"] != nil ||
		got["name"] != nil || got["size"] != float64(0) || got["file-count"] != float64(0) {
		t.Fatalf("corrupted shallow row = %+v", got)
	}
	if got := rows[1]; got["is-virtual"] != false || got["origin-repo-id"] != nil || got["store-id"] != "" {
		t.Fatalf("virtual shallow row = %+v", got)
	}
	if got := rows[2]; got["name"] != "Alpha 层" || got["last-modifier"] != "Fixture@Example.com" ||
		got["root"] != "" || got["head-cmmt-id"] != adminBackup || got["repo-type"] != "wiki" ||
		got["encrypted"] != true || got["status"] != float64(1) || got["size"] != float64(5) ||
		got["file-count"] != float64(2) || rows[3]["head-cmmt-id"] != adminHead {
		t.Fatalf("shallow repository row = %+v, next head = %v", got, rows[3]["head-cmmt-id"])
	}
}
