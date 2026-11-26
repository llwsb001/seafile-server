package seafserver

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
)

const (
	oldSyntheticPassword = "old SQLite sample credential"
	newSyntheticPassword = "new SQLite sample credential"
)

func seedSQLitePasswordRepo(t *testing.T, version int) (*RepoPasswords, *sql.DB, string, *commitmgr.Commit) {
	t.Helper()
	reader, db, root := virtualTestOrigin(t)
	ctx := context.Background()
	original, err := reader.Repo(ctx, testVirtualOrigin)
	if err != nil {
		t.Fatal(err)
	}
	material, rpcErr := encryptionBootstrap(ctx, governanceArgs(t, version, original.ID, oldSyntheticPassword))
	if rpcErr != nil {
		t.Fatal("could not generate fixture material")
	}
	fields := material.(map[string]any)
	commit := commitmgr.NewCanonicalCommit(original.ID, original.HeadCommitID, original.RootID,
		testVirtualOwner, "Seed encrypted test library", 100)
	commit.RepoName = original.Name
	commit.RepoDesc = original.Desc
	commit.Version = original.Version
	commit.Encrypted = "true"
	commit.EncVersion = version
	commit.Magic = fields["magic"].(string)
	commit.RandomKey = fields["random-key"].(string)
	commit.Salt, _ = fields["salt"].(string)
	if err := commitmgr.SaveCanonicalToStore(reader.commits, commit); err != nil {
		t.Fatal(err)
	}
	if advanced, err := PublishSQLiteBranch(ctx, db, original.HeadCommitID, commit); err != nil || !advanced {
		t.Fatalf("seed encrypted fixture: %v, %v", advanced, err)
	}
	return NewRepoPasswords(reader), db, root, commit
}

func TestSQLiteChangeRepoPasswordPreservesFileKeyAndHistory(t *testing.T) {
	for version := 2; version <= 4; version++ {
		p, db, _, original := seedSQLitePasswordRepo(t, version)
		ctx := context.Background()
		const peer = "peer@example.test"
		for _, user := range []string{testVirtualOwner, peer} {
			if _, err := p.set(ctx, governanceArgs(t, original.RepoID, user, oldSyntheticPassword)); err != nil {
				t.Fatal("can't initialize key cache")
			}
		}
		initialKey, initialIV := p.copyKey(original.RepoID, testVirtualOwner)
		actor := repoPasswordID{repo: original.RepoID, user: testVirtualOwner}
		other := repoPasswordID{repo: original.RepoID, user: peer}
		for _, id := range []repoPasswordID{actor, other} {
			cached := p.keys[id]
			cached.expiresAt--
			p.keys[id] = cached
		}
		firstExpiry, peerExpiry := p.keys[actor].expiresAt, p.keys[other].expiresAt
		change := p.MutationRPCHandlers(db)["seafile_change_repo_passwd"]
		for _, input := range []struct {
			id, old, next, user any
			message             string
		}{
			{original.RepoID, oldSyntheticPassword, newSyntheticPassword, nil, "No user given"},
			{original.RepoID, "", newSyntheticPassword, testVirtualOwner, "Empty passwd"},
			{nil, oldSyntheticPassword, newSyntheticPassword, testVirtualOwner, "Invalid repo id"},
			{unknownGovernanceRepo, oldSyntheticPassword, newSyntheticPassword, testVirtualOwner, "No such library"},
			{original.RepoID, "wrong sample", newSyntheticPassword, testVirtualOwner, "Incorrect password"},
		} {
			ret, err := change(ctx, governanceArgs(t, input.id, input.old, input.next, input.user))
			if ret != -1 || err == nil || err.Code != 503 || err.Message != input.message {
				t.Fatalf("expected %s, got %v / %v", input.message, ret, err)
			}
		}
		if _, err := db.Exec("INSERT INTO RepoUserToken VALUES (?, ?, 'fixture-token')", original.RepoID, testVirtualOwner); err != nil {
			t.Fatal(err)
		}
		if ret, err := change(ctx, governanceArgs(t, original.RepoID, oldSyntheticPassword, newSyntheticPassword, testVirtualOwner)); ret != 0 || err != nil {
			t.Fatalf("change returned %v / %v", ret, err)
		}
		if p.keys[actor].expiresAt <= firstExpiry || p.keys[other].expiresAt != peerExpiry {
			t.Fatal("only the already unlocked actor's deadline should be refreshed")
		}
		repo, err := p.reader.Repo(ctx, original.RepoID)
		if err != nil {
			t.Fatal(err)
		}
		latest, err := p.reader.Commit(repo.ID, repo.HeadCommitID)
		if err != nil || latest.ParentID.String != original.CommitID || latest.RootID != original.RootID ||
			latest.Desc != repoPasswordChangeMessage || latest.Salt != original.Salt || latest.RandomKey == original.RandomKey {
			t.Fatal("password update changed the file tree or did not link the prior head")
		}
		old, err := p.reader.Commit(repo.ID, original.CommitID)
		if err != nil || old.RandomKey != original.RandomKey || old.Magic != original.Magic {
			t.Fatal("historical password material was modified")
		}
		if _, _, ok := unlockRepo(repo, oldSyntheticPassword); ok {
			t.Fatal("old password still unlocks the current head")
		}
		if key, iv, ok := unlockRepo(repo, newSyntheticPassword); !ok || !bytes.Equal(key, initialKey) || !bytes.Equal(iv, initialIV) {
			t.Fatal("new password cannot recover the existing file key")
		}
		var tokens int
		if err := db.QueryRow("SELECT count(*) FROM RepoUserToken WHERE repo_id=?", repo.ID).Scan(&tokens); err != nil || tokens != 1 {
			t.Fatal("password change revoked the existing sync token")
		}
		stale := commitmgr.NewCanonicalCommit(repo.ID, original.CommitID, original.RootID, testVirtualOwner, "Staged file write", 101)
		if _, err := PublishSQLiteBranch(ctx, db, original.CommitID, stale); !errors.Is(err, ErrSQLiteBranchChanged) {
			t.Fatal("a file publication based on the old head clobbered the new credential")
		}
	}
}

func TestSQLiteChangeRepoPasswordRollsBack(t *testing.T) {
	p, db, root, original := seedSQLitePasswordRepo(t, 3)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TRIGGER refuse_password_update BEFORE UPDATE ON RepoInfo
		BEGIN SELECT RAISE(ABORT, 'forced metadata failure'); END`); err != nil {
		t.Fatal(err)
	}
	before, err := filepath.Glob(filepath.Join(root, "storage", "commits", original.RepoID, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if ret, err := p.changeRepoPassword(ctx, db, governanceArgs(t, original.RepoID, oldSyntheticPassword,
		newSyntheticPassword, testVirtualOwner)); ret != -1 || err == nil || err.Message != "Internal server error" {
		t.Fatalf("injected failure = %v / %v", ret, err)
	}
	repo, err := p.reader.Repo(ctx, original.RepoID)
	if err != nil || repo.HeadCommitID != original.CommitID {
		t.Fatal("failed publication changed the head")
	}
	if _, _, ok := unlockRepo(repo, oldSyntheticPassword); !ok {
		t.Fatal("failed publication changed the credential")
	}
	after, err := filepath.Glob(filepath.Join(root, "storage", "commits", original.RepoID, "*", "*"))
	if err != nil || len(after) != len(before) || len(p.keys) != 0 {
		t.Fatal("failed publication changed stored objects or warmed the cache")
	}
}
