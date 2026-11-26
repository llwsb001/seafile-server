package seafserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestPublishSQLiteRepairRollsBackBranchAndTokenRevocationTogether(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE TABLE Repo (repo_id TEXT PRIMARY KEY)",
		"CREATE TABLE Branch (repo_id TEXT, name TEXT, commit_id TEXT, PRIMARY KEY (repo_id, name))",
		"CREATE TABLE RepoUserToken (repo_id TEXT, token TEXT)",
		"CREATE TABLE RepoTokenPeerInfo (token TEXT PRIMARY KEY)",
		"INSERT INTO Repo VALUES ('repo')",
		"INSERT INTO Branch VALUES ('repo', 'master', 'original')",
		"INSERT INTO RepoUserToken VALUES ('repo', 'token')",
		"INSERT INTO RepoTokenPeerInfo VALUES ('token')",
		"CREATE TRIGGER fail_token_removal BEFORE DELETE ON RepoUserToken BEGIN SELECT RAISE(ABORT, 'injected'); END",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	if published, err := PublishSQLiteRepair(ctx, db, "repo", "original", "repaired"); published || err == nil {
		t.Fatalf("publish = %v, %v, want injected SQLite failure", published, err)
	}
	var head string
	var tokenCount, peerCount int
	if err := db.QueryRow(`SELECT commit_id, (SELECT count(*) FROM RepoUserToken),
		(SELECT count(*) FROM RepoTokenPeerInfo) FROM Branch WHERE repo_id = 'repo' AND name = 'master'`).Scan(
		&head, &tokenCount, &peerCount); err != nil {
		t.Fatal(err)
	}
	if head != "original" || tokenCount != 1 || peerCount != 1 {
		t.Fatalf("rollback kept head %s and token counts (%d, %d)", head, tokenCount, peerCount)
	}
}
