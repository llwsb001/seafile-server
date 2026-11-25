package seafserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
)

func TestSharedRepoMetadataFallbackKeepsSQLPageBoundaries(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `
CREATE TABLE SharedRepo (repo_id TEXT, from_email TEXT, to_email TEXT, permission TEXT);
CREATE TABLE VirtualRepo (repo_id TEXT, origin_repo TEXT, path TEXT);
CREATE TABLE RepoInfo (repo_id TEXT, name TEXT, update_time INTEGER, version INTEGER,
                       is_encrypted INTEGER, last_modifier TEXT, status INTEGER, type TEXT);
CREATE TABLE RepoSize (repo_id TEXT, size INTEGER);
CREATE TABLE Branch (repo_id TEXT, name TEXT, commit_id TEXT);
INSERT INTO SharedRepo VALUES
  ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'owner', 'reader', 'r'),
  ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'owner', 'reader', 'rw'),
  ('cccccccc-cccc-cccc-cccc-cccccccccccc', 'owner', 'reader', 'r');
INSERT INTO Branch SELECT repo_id, 'master', '0123456789012345678901234567890123456789' FROM SharedRepo;
INSERT INTO RepoInfo VALUES ('cccccccc-cccc-cccc-cccc-cccccccccccc', 'Cached', 10, 1, 0, 'OWNER', 1, 'test');
INSERT INTO RepoSize VALUES ('cccccccc-cccc-cccc-cccc-cccccccccccc', 4096);
`); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(db, t.TempDir())
	commit := commitmgr.Commit{
		CommitID:    "0123456789012345678901234567890123456789",
		RepoID:      "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		RootID:      strings.Repeat("0", 40),
		CreatorID:   strings.Repeat("0", 40),
		CreatorName: "READER",
		RepoName:    "From Commit",
		Ctime:       20,
		Version:     1,
		Encrypted:   "true",
		EncVersion:  2,
		Magic:       "fixture-only",
	}
	var data bytes.Buffer
	if err := commit.ToData(&data); err != nil {
		t.Fatal(err)
	}
	if err := reader.commits.Write(commit.RepoID, commit.CommitID, &data, false); err != nil {
		t.Fatal(err)
	}
	call := func(page string) []map[string]any {
		t.Helper()
		args := []json.RawMessage{json.RawMessage(`"reader"`), json.RawMessage(`"to_email"`), json.RawMessage(page), json.RawMessage("1")}
		value, rpcErr := reader.listShareRepos(ctx, args)
		if rpcErr != nil {
			t.Fatalf("page %s: %v", page, rpcErr)
		}
		buffer, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var repos []map[string]any
		if err := json.Unmarshal(buffer, &repos); err != nil {
			t.Fatal(err)
		}
		return repos
	}

	if repos := call("0"); len(repos) != 1 || repos[0]["name"] != "Cached" ||
		repos[0]["last-modifier"] != "OWNER" || repos[0]["size"] != float64(4096) ||
		repos[0]["status"] != float64(1) || repos[0]["repo-type"] != "test" {
		t.Fatalf("cached metadata: %+v", repos)
	}
	if repos := call("1"); repos != nil {
		t.Fatalf("page with missing commit: %+v", repos)
	}
	if repos := call("2"); len(repos) != 1 || repos[0]["name"] != "From Commit" ||
		repos[0]["last-modifier"] != "reader" || repos[0]["last-modified"] != float64(20) ||
		repos[0]["encrypted"] != true || repos[0]["enc-version"] != float64(0) || repos[0]["magic"] != nil {
		t.Fatalf("commit metadata: %+v", repos)
	}
}
