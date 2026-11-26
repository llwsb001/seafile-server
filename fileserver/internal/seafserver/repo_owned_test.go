package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListOwnedReposRPC(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema, err := os.ReadFile("../../../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(string(schema))
	const (
		owner       = "owner@example.test"
		broken      = "10000000-0000-4000-8000-000000000001"
		first       = "10000000-0000-4000-8000-000000000002"
		second      = "10000000-0000-4000-8000-000000000003"
		virtual     = "10000000-0000-4000-8000-000000000004"
		other       = "10000000-0000-4000-8000-000000000005"
		missing     = "10000000-0000-4000-8000-000000000006"
		missingHead = "1000000000000000000000000000000000000000"
	)
	reader := NewReader(db, root)
	created, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "fallback from commit", "fixture", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	fallback := created.(string)
	exec("DELETE FROM RepoInfo WHERE repo_id = ?", fallback)
	for _, repo := range []struct {
		id, owner, name string
		modified        int64
	}{
		{broken, owner, "missing branch", 300},
		{first, owner, "first", 200},
		{second, owner, "second", 200},
		{virtual, owner, "exclude virtual", 400},
		{other, "other@example.test", "exclude other owner", 400},
		{missing, owner, "missing object", 100},
	} {
		exec("INSERT INTO RepoOwner(repo_id, owner_id) VALUES (?, ?)", repo.id, repo.owner)
		exec(`INSERT INTO RepoInfo(repo_id, name, update_time, version, is_encrypted, last_modifier, status, type)
		VALUES (?, ?, ?, 1, 1, 'MixedCase@example.test', 1, 'wiki')`, repo.id, repo.name, repo.modified)
		if repo.id != broken {
			exec("INSERT INTO Branch(repo_id, name, commit_id) VALUES (?, 'master', ?)", repo.id, missingHead)
		}
	}
	exec("INSERT INTO VirtualRepo(repo_id, origin_repo, path) VALUES (?, ?, '/child')", virtual, first)
	exec("INSERT INTO RepoSize(repo_id, size) VALUES (?, 123)", first)
	exec("INSERT INTO RepoFileCount(repo_id, file_count) VALUES (?, 42)", first)
	exec("UPDATE RepoInfo SET last_modifier = NULL WHERE repo_id = ?", missing)

	type listEntry struct {
		ID          string `json:"id"`
		Name        string `json:"repo-name"`
		Modifier    string `json:"last-modifier"`
		Head        string `json:"head-cmmt-id"`
		Root        string `json:"root"`
		Store       string `json:"store-id"`
		Size        int64  `json:"size"`
		FileCount   int64  `json:"file-count"`
		IsCorrupted bool   `json:"is-corrupted"`
		Encrypted   bool   `json:"encrypted"`
	}
	handler := reader.RPCHandlers(nil)["seafile_list_owned_repos"]
	query := func(user any, corrupted, start, limit int) []listEntry {
		t.Helper()
		encoded, err := json.Marshal([]any{user, corrupted, start, limit})
		if err != nil {
			t.Fatal(err)
		}
		var args []json.RawMessage
		if err := json.Unmarshal(encoded, &args); err != nil {
			t.Fatal(err)
		}
		value, rpcErr := handler(ctx, args)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		encoded, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var result []listEntry
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	full := query(owner, 1, -1, -1)
	var ids []string
	for _, repo := range full {
		ids = append(ids, repo.ID)
	}
	if want := []string{broken, first, second, missing, fallback}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("owner listing order = %v, want %v", ids, want)
	}
	if got := full[0]; !got.IsCorrupted || got.Head != "" || got.Name != "" || got.Encrypted {
		t.Fatalf("missing head row: %+v", got)
	}
	if got := full[1]; got.Modifier != "MixedCase@example.test" || !got.Encrypted || got.Size != 123 || got.FileCount != 0 || got.Store != "" || got.Root != "" {
		t.Fatalf("shallow list object: %+v", got)
	}
	if got := full[3]; !got.IsCorrupted || got.Head != missingHead || got.Name != "missing object" {
		t.Fatalf("missing commit row: %+v", got)
	}
	if got := full[4]; got.IsCorrupted || got.Name != "fallback from commit" || got.Modifier != owner {
		t.Fatalf("legacy metadata: %+v", got)
	}
	if got := query(owner, 0, 0, 1); got != nil {
		t.Fatalf("first page should be empty after omitting broken row: %+v", got)
	}
	if got := query(owner, 0, 1, 1); len(got) != 1 || got[0].ID != first {
		t.Fatalf("second page: %+v", got)
	}
	if got := query(nil, 1, -1, -1); got != nil {
		t.Fatalf("null owner: %+v", got)
	}
}
