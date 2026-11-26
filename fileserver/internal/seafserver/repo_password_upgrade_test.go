package seafserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteUpgradeRepoPasswordHash(t *testing.T) {
	for _, version := range []int{2, 3, 4} {
		p, db, root, original := seedSQLitePasswordRepo(t, version)
		ctx := context.Background()
		repo, err := p.reader.Repo(ctx, original.RepoID)
		if err != nil {
			t.Fatal(err)
		}
		initialKey, initialIV, ok := unlockRepo(repo, oldSyntheticPassword)
		if !ok {
			t.Fatal("fixture does not unlock")
		}
		upgrade := p.MutationRPCHandlers(db)["seafile_upgrade_repo_pwd_hash_algorithm"]
		for _, input := range []struct {
			algo, params any
			message      string
		}{
			{nil, "1", "Invalid pwd hash algorithm"},
			{"pbkdf2", "1000", "Unsupported pwd hash algorithm"},
			{"pbkdf2_sha256", nil, "Invalid pwd hash params"},
			{"pbkdf2_sha256", "10000001", "Invalid pwd hash params"},
			{"argon2id", "1,8,2", "Invalid pwd hash params"},
			{"argon2id", "1,262145,1", "Invalid pwd hash params"},
		} {
			ret, rpcErr := upgrade(ctx, governanceArgs(t, original.RepoID, testVirtualOwner, oldSyntheticPassword, input.algo, input.params))
			if ret != -1 || rpcErr == nil || rpcErr.Code != 503 || rpcErr.Message != input.message {
				t.Fatalf("expected %s, got %v / %v", input.message, ret, rpcErr)
			}
		}

		mustExecPasswordSQL(t, db, "INSERT INTO RepoUserToken VALUES (?, ?, 'example-sync-token')", original.RepoID, testVirtualOwner)
		mustExecPasswordSQL(t, db, `INSERT INTO RepoTokenPeerInfo (token, peer_id) VALUES ('example-sync-token','existing-client')`)
		mustExecPasswordSQL(t, db, "INSERT INTO RepoUserToken VALUES (?, ?, 'other-token')", unknownGovernanceRepo, testVirtualOwner)
		mustExecPasswordSQL(t, db, `INSERT INTO RepoTokenPeerInfo (token, peer_id) VALUES ('other-token','other-client')`)
		if ret, rpcErr := upgrade(ctx, governanceArgs(t, original.RepoID, testVirtualOwner, "wrong credential", "pbkdf2_sha256", "")); ret != -1 || rpcErr == nil || rpcErr.Code != 503 || rpcErr.Message != "Incorrect password" {
			t.Fatalf("rejection using legacy verifier = %v / %v", ret, rpcErr)
		}
		if ret, rpcErr := upgrade(ctx, governanceArgs(t, original.RepoID, testVirtualOwner, oldSyntheticPassword, "pbkdf2_sha256", "")); ret != 0 || rpcErr != nil {
			t.Fatalf("upgrade = %v / %v", ret, rpcErr)
		}
		current, err := p.reader.Repo(ctx, original.RepoID)
		if err != nil || current.HeadCommitID == original.CommitID || current.PwdHashAlgo != "pbkdf2_sha256" ||
			current.PwdHashParams != "" || current.RootID != original.RootID || current.RandomKey != original.RandomKey || current.Salt != original.Salt {
			t.Fatal("upgrade changed the file key, salt or root, or omitted the new verifier")
		}
		key, iv, ok := unlockRepo(current, oldSyntheticPassword)
		if !ok || !bytes.Equal(key, initialKey) || !bytes.Equal(iv, initialIV) || len(p.keys) != 0 {
			t.Fatal("upgrade changed the underlying file key or added a cache entry")
		}
		assertPasswordTokens(t, db, "example-sync-token", 0)
		assertPasswordTokens(t, db, "other-token", 1)
		assertCommitIncludesEmptyParams(t, root, current.ID, current.HeadCommitID)
		currentCommit, err := p.reader.Commit(current.ID, current.HeadCommitID)
		if err != nil || currentCommit.ParentID.String != original.CommitID || currentCommit.Desc != repoPasswordUpgradeMessage {
			t.Fatal("upgrade dropped the parent or created the wrong commit")
		}
		var payload bytes.Buffer
		if err := currentCommit.ToData(&payload); err != nil {
			t.Fatal(err)
		}
		assertEmptyParamsJSON(t, payload.Bytes())

		mustExecPasswordSQL(t, db, "INSERT INTO RepoUserToken VALUES (?, ?, 'later-token')", original.RepoID, testVirtualOwner)
		mustExecPasswordSQL(t, db, `INSERT INTO RepoTokenPeerInfo (token, peer_id) VALUES ('later-token','new-client')`)
		if ret, rpcErr := upgrade(ctx, governanceArgs(t, original.RepoID, testVirtualOwner, "wrong credential", "pbkdf2_sha256", "")); ret != 0 || rpcErr != nil {
			t.Fatalf("same target is a no-op, got %v / %v", ret, rpcErr)
		}
		still, err := p.reader.Repo(ctx, current.ID)
		if err != nil || still.HeadCommitID != current.HeadCommitID {
			t.Fatal("same target changed the head")
		}
		assertPasswordTokens(t, db, "later-token", 1)
		if ret, rpcErr := upgrade(ctx, governanceArgs(t, original.RepoID, testVirtualOwner, "wrong credential", "argon2id", "1,1024,2")); ret != 1 || rpcErr == nil || rpcErr.Code != 503 || rpcErr.Message != "Incorrect password" {
			t.Fatalf("rejection using hash verifier = %v / %v", ret, rpcErr)
		}
		if ret, rpcErr := upgrade(ctx, governanceArgs(t, original.RepoID, testVirtualOwner, oldSyntheticPassword, "argon2id", "1,1024,2")); ret != 0 || rpcErr != nil {
			t.Fatalf("Argon2id upgrade = %v / %v", ret, rpcErr)
		}
		assertPasswordTokens(t, db, "later-token", 0)
		finalRepo, err := p.reader.Repo(ctx, original.RepoID)
		if err != nil || finalRepo.PwdHashAlgo != "argon2id" || finalRepo.PwdHashParams != "1,1024,2" {
			t.Fatal("Argon2id verifier or its params were not saved")
		}
		if key, iv, ok := unlockRepo(finalRepo, oldSyntheticPassword); !ok || !bytes.Equal(key, initialKey) || !bytes.Equal(iv, initialIV) {
			t.Fatal("Argon2id migration changed the file key")
		}
	}
}

func TestSQLiteUpgradeRepoPasswordHashRollsBackTokenRevocation(t *testing.T) {
	p, db, root, original := seedSQLitePasswordRepo(t, 3)
	ctx := context.Background()
	mustExecPasswordSQL(t, db, "INSERT INTO RepoUserToken VALUES (?, ?, 'example-sync-token')", original.RepoID, testVirtualOwner)
	mustExecPasswordSQL(t, db, `INSERT INTO RepoTokenPeerInfo (token, peer_id) VALUES ('example-sync-token','existing-client')`)
	mustExecPasswordSQL(t, db, `CREATE TRIGGER refuse_token_removal BEFORE DELETE ON RepoUserToken
		BEGIN SELECT RAISE(ABORT, 'forced token failure'); END`)
	before, err := filepath.Glob(filepath.Join(root, "storage", "commits", original.RepoID, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if ret, rpcErr := p.upgradeRepoPasswordHash(ctx, db, governanceArgs(t, original.RepoID, testVirtualOwner,
		oldSyntheticPassword, "pbkdf2_sha256", "1000")); ret != -1 || rpcErr == nil || rpcErr.Message != "Internal server error" {
		t.Fatalf("failure = %v / %v", ret, rpcErr)
	}
	assertPasswordTokens(t, db, "example-sync-token", 1)
	still, err := p.reader.Repo(ctx, original.RepoID)
	if err != nil || still.HeadCommitID != original.CommitID {
		t.Fatal("failed token revocation changed the head")
	}
	after, err := filepath.Glob(filepath.Join(root, "storage", "commits", original.RepoID, "*", "*"))
	if err != nil || len(after) != len(before) {
		t.Fatal("failed token revocation stored a new commit")
	}
}

func mustExecPasswordSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func assertPasswordTokens(t *testing.T, db *sql.DB, token string, expected int) {
	t.Helper()
	var tokens, peers int
	err := db.QueryRow(`SELECT (SELECT count(*) FROM RepoUserToken WHERE token = ?),
		(SELECT count(*) FROM RepoTokenPeerInfo WHERE token = ?)`, token, token).Scan(&tokens, &peers)
	if err != nil || tokens != expected || peers != expected {
		t.Fatalf("token and peer row counts = %d / %d, want %d for both", tokens, peers, expected)
	}
}

func assertCommitIncludesEmptyParams(t *testing.T, root, repo, commit string) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, "storage", "commits", repo, commit[:2], commit[2:]))
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyParamsJSON(t, payload)
}

func assertEmptyParamsJSON(t *testing.T, payload []byte) {
	t.Helper()
	var fields struct {
		Params *string `json:"pwd_hash_params"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil || fields.Params == nil || *fields.Params != "" {
		t.Fatal("empty password hash params must be stored explicitly")
	}
}
