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

func TestAdminRepoSearch(t *testing.T) {
	const (
		nameSearch = "seafile_search_repos_by_name"
		idSearch   = "seafile_get_repos_by_id_prefix"
	)
	reader := adminRepoFixture(t)
	for _, test := range []struct {
		name, method string
		args         []any
		want         []string
	}{
		{"name ASCII folding", nameSearch, []any{"alPHA"}, []string{adminBroken, adminFirst, adminFirst, adminSecond}},
		{"name Unicode", nameSearch, []any{"层"}, []string{adminFirst, adminFirst, adminSecond, adminUnicode}},
		{"name no Unicode folding", nameSearch, []any{"älpha"}, nil},
		{"name wildcard", nameSearch, []any{"a_PH%"}, []string{adminBroken, adminFirst, adminFirst, adminSecond}},
		{"name empty", nameSearch, []any{""}, []string{adminBroken, adminFirst, adminFirst, adminSecond, adminUnicode}},
		{"name unknown", nameSearch, []any{"unknown"}, nil},
		{"ID unpaged", idSearch, []any{"0323AA00", -1, -1}, []string{adminUnicode, adminSecond, adminFirst, adminFirst, adminBroken, adminStale}},
		{"ID page", idSearch, []any{"0323aa00", 1, 3}, []string{adminFirst, adminFirst, adminBroken}},
		{"ID full", idSearch, []any{adminSecond, 0, 1}, []string{adminSecond}},
		{"ID wildcard", idSearch, []any{"0323aa0_", 0, 1}, []string{adminStale}},
		{"ID empty", idSearch, []any{"", 0, 1}, []string{adminStale}},
		{"ID too long", idSearch, []any{"0323aa00-0000-4000-8000-0000000000001", 0, 10}, nil},
		{"ID past end", idSearch, []any{"0323aa00", 1000, 10}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkAdminRepoIDs(t, listAdminRepoRPC(t, reader, test.method, test.args...), test.want)
		})
	}
	if _, rpcErr := callAdminRepoRPC(t, reader, idSearch, nil, -1, -1); rpcErr == nil ||
		rpcErr.Code != errBadArgs || rpcErr.Message != "Argument should not be null" {
		t.Fatalf("null ID prefix error = %v", rpcErr)
	}
	names := listAdminRepoRPC(t, reader, nameSearch, "duplicate")
	identifiers := listAdminRepoRPC(t, reader, idSearch, adminSecond, 0, 1)
	if names[0]["file-count"] != float64(1) || identifiers[0]["file-count"] != float64(0) {
		t.Fatalf("file counts from name and ID lookup = %v and %v", names[0]["file-count"], identifiers[0]["file-count"])
	}
}

func TestAdminSystemDefaultRepo(t *testing.T) {
	reader := adminRepoFixture(t)
	checkDefault := func(want string) {
		t.Helper()
		value, rpcErr := callAdminRepoRPC(t, reader, "get_system_default_repo_id")
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		valueJSON, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(valueJSON) != want {
			t.Fatalf("default repository = %s, want %s", valueJSON, want)
		}
	}
	checkDefault("null")
	if _, err := reader.db.Exec("INSERT INTO SystemInfo VALUES ('unrelated', ?), ('default_repo_id', ?)",
		adminSecond, adminFirst); err != nil {
		t.Fatal(err)
	}
	checkDefault(`"` + adminFirst + `"`)
	if _, err := reader.db.Exec("DELETE FROM Repo WHERE repo_id = ?", adminFirst); err != nil {
		t.Fatal(err)
	}
	checkDefault(`"` + adminFirst + `"`)
}

func TestAdminRepoStorageAndOrphans(t *testing.T) {
	reader := adminRepoFixture(t)
	const (
		sizeRPC   = "seafile_server_repo_size"
		orphanRPC = "seafile_get_orphan_repo_list"
	)
	for _, test := range []struct {
		id        any
		size      int64
		errorText string
	}{
		{adminFirst, 5, ""},
		{adminVirtual, 99, ""},
		{adminMissing, 0, ""},
		{adminStale, 100, ""},
		{"12345678-aaaa-4000-8000-000000000099", 0, ""},
		{nil, -1, "Bad repo id"},
		{"short", -1, "Bad repo id"},
		{"12345678-aaaa-4000-8000-0000000000ZZ", -1, "Invalid repo id"},
	} {
		value, rpcErr := callAdminRepoRPC(t, reader, sizeRPC, test.id)
		if value != test.size ||
			(test.errorText == "" && rpcErr != nil) ||
			(test.errorText != "" && (rpcErr == nil || rpcErr.Code != errBadArgs || rpcErr.Message != test.errorText)) {
			t.Fatalf("size(%v) = %v, %v", test.id, value, rpcErr)
		}
	}

	const owner = "admin@example.test"
	created, rpcErr := reader.createRepo(context.Background(), reader.db, fileWriteRPCArgs(t, "Orphan library", "From the head", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	id := created.(string)
	checkAdminRepoIDs(t, listAdminRepoRPC(t, reader, orphanRPC), nil)
	if _, err := reader.db.Exec("DELETE FROM RepoOwner WHERE repo_id = ?", id); err != nil {
		t.Fatal(err)
	}
	repos := listAdminRepoRPC(t, reader, orphanRPC)
	checkAdminRepoIDs(t, repos, []string{id})
	if got := repos[0]; got["repo-name"] != "Orphan library" || got["repo-desc"] != "From the head" ||
		got["store-id"] != id || got["root"] != "0000000000000000000000000000000000000000" {
		t.Fatalf("orphan repository object = %+v", got)
	}
}
