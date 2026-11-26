package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestSQLiteSyncPreservesExplicitEmptyHashParameters(t *testing.T) {
	rotated := commitmgr.Commit{Encrypted: "true", EncVersion: 2, PwdHashAlgo: "pbkdf2_sha256", HasPwdHashParams: true}
	client := rotated
	client.HasPwdHashParams = false
	if sameCommitEncryption(&rotated, &client) {
		t.Fatal("a client that omits parameters could overwrite the explicitly empty migration target")
	}
}

func TestSQLiteBranchPublication(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	root := t.TempDir()
	path := filepath.Join(root, "seafile.db")
	setup, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { setup.Close() })
	schema, err := os.ReadFile("../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := setup.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(string(schema))
	const repo = "9b3fbe48-1075-45c2-a49a-d13a44959a68"
	oldHead := strings.Repeat("1", 40)
	seed("INSERT INTO Repo VALUES (?)", repo)
	seed("INSERT INTO Branch VALUES ('master', ?, ?)", repo, oldHead)
	seed("INSERT INTO RepoInfo (repo_id, name, status, type) VALUES (?, 'before', 2, 'draft')", repo)

	db, err := seafserver.OpenSQLiteReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	previousDB, previousTimeout, previousRPC := seafileDB, option.DBOpTimeout, rpcclient
	seafileDB, option.DBOpTimeout = db, 2*time.Second
	repomgr.Init(db)
	t.Cleanup(func() {
		seafileDB, option.DBOpTimeout, rpcclient = previousDB, previousTimeout, previousRPC
		repomgr.Init(previousDB)
	})
	commitmgr.Init("", root)
	if current, err := repomgr.GetCurrentGCID(repo); err != nil || current != "" {
		t.Fatalf("SQLite GC generation = %q, %v", current, err)
	}
	if err := repomgr.SetLastGCID(repo, "client", "generation"); err != nil {
		t.Fatal(err)
	}
	if found, err := repomgr.HasLastGCID(repo, "client"); err != nil || found {
		t.Fatalf("SQLite last GC generation exists = %v, %v", found, err)
	}

	socket := filepath.Join(root, "rpc")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- searpc.Serve(ctx, listener, seafserver.RPCService, seafserver.EventRPCHandlers()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	rpcclient = searpc.Init(socket, seafserver.RPCService, 0)

	next := commitmgr.NewCommit(repo, oldHead, strings.Repeat("0", 40), "owner@example.test", "Updated fixture")
	next.RepoName, next.Version, next.Encrypted = "after", 1, "true"
	check := func(wantHead, wantName string) {
		t.Helper()
		var head, name, kind string
		var status int
		err := setup.QueryRow(`SELECT b.commit_id, i.name, i.status, i.type FROM Branch b
			JOIN RepoInfo i USING (repo_id) WHERE b.repo_id = ? AND b.name = 'master'`, repo).
			Scan(&head, &name, &status, &kind)
		if err != nil || head != wantHead || name != wantName || status != 2 || kind != "draft" {
			t.Fatalf("branch and metadata = %q %q %d %q (%v), want %q %q 2 draft",
				head, name, status, kind, err, wantHead, wantName)
		}
	}
	takeEvent := func(wantCommit string) {
		t.Helper()
		value, err := rpcclient.Call("pop_event", seafileServerChannelEvent)
		if err != nil {
			t.Fatal(err)
		}
		if wantCommit == "" {
			if value != nil {
				t.Fatalf("unexpected branch event: %v", value)
			}
			return
		}
		event, ok := value.(map[string]any)
		if !ok {
			t.Fatal("updated branch did not publish an event")
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(event["content"].(string)), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["commit_id"] != wantCommit || payload["repo_id"] != repo {
			t.Fatalf("unexpected branch event: %v", payload)
		}
	}

	if _, err := updateBranch(repo, repo, next.CommitID, oldHead, "", true, ""); err == nil {
		t.Fatal("missing commit was published")
	}
	check(oldHead, "before")
	takeEvent("")

	if err := commitmgr.Save(next); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;
		CREATE TABLE Parent (id INTEGER PRIMARY KEY);
		CREATE TABLE Child (parent_id INTEGER REFERENCES Parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_branch_commit AFTER UPDATE ON RepoInfo
		BEGIN INSERT INTO Child VALUES (1); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := updateBranch(repo, repo, next.CommitID, oldHead, "", true, ""); err == nil {
		t.Fatal("commit should have failed")
	}
	check(oldHead, "before")
	takeEvent("")

	seed("DROP TRIGGER fail_branch_commit")
	lock, err := setup.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	_, writeErr := updateBranch(repo, repo, next.CommitID, oldHead, "", true, "")
	_, unlockErr := lock.ExecContext(ctx, "ROLLBACK")
	lock.Close()
	if writeErr == nil || unlockErr != nil {
		t.Fatalf("lock injection did not fail and release: %v %v", writeErr, unlockErr)
	}
	check(oldHead, "before")
	takeEvent("")

	if _, err := updateBranch(repo, repo, next.CommitID, oldHead, "", true, ""); err != nil {
		t.Fatal(err)
	}
	check(next.CommitID, "after")
	takeEvent(next.CommitID)
	if _, err := updateBranch(repo, repo, next.CommitID, next.CommitID, "", false, ""); err != nil {
		t.Fatal(err)
	}
	takeEvent("")
	current := repomgr.Get(repo)
	if current == nil {
		t.Fatal("failed to reload the published repository")
	}
	base := &commitmgr.Commit{CommitID: oldHead}
	var repeated string
	if retry, err := genCommitNeedRetry(current, base, next, next.RootID, next.CreatorName, true, &repeated, "", false); retry || err != nil || repeated != next.CommitID {
		t.Fatalf("repeat = %q, retry %v, error %v", repeated, retry, err)
	}
	takeEvent("")
	if _, err := updateBranch(repo, repo, next.CommitID, oldHead, "", false, ""); !errors.Is(err, seafserver.ErrSQLiteBranchChanged) {
		t.Fatalf("stale branch update error = %v", err)
	}

	otherDB, err := seafserver.OpenSQLiteReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	type result struct {
		commit *commitmgr.Commit
		ok     bool
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i, pool := range []*sql.DB{db, otherDB} {
		desc := "contender " + string('2'+rune(i))
		commit := commitmgr.NewCommit(repo, next.CommitID, next.RootID, next.CreatorName, desc)
		commit.RepoName, commit.Version = desc, next.Version
		if err := commitmgr.Save(commit); err != nil {
			t.Fatal(err)
		}
		go func() {
			<-start
			ok, err := seafserver.PublishSQLiteBranch(ctx, pool, next.CommitID, commit)
			results <- result{commit, ok, err}
		}()
	}
	close(start)
	var winner *commitmgr.Commit
	for range 2 {
		got := <-results
		if got.ok && got.err == nil && winner == nil {
			winner = got.commit
		} else if got.ok || !errors.Is(got.err, seafserver.ErrSQLiteBranchChanged) {
			t.Fatalf("unexpected result from competing update: %v %v", got.ok, got.err)
		}
	}
	if winner == nil {
		t.Fatal("no branch update succeeded")
	}
	check(winner.CommitID, winner.RepoName)
	takeEvent("") // The competing publications used the direct SQLite storage API.

	rotated := commitmgr.NewCanonicalCommit(repo, winner.CommitID, next.RootID, next.CreatorName, "Changed library password", 301)
	rotated.RepoName, rotated.Version, rotated.Encrypted, rotated.EncVersion = winner.RepoName, 1, "true", 3
	rotated.Magic = strings.Repeat("3", 64)
	rotated.RandomKey = strings.Repeat("4", 96)
	rotated.Salt = strings.Repeat("5", 64)
	if err := commitmgr.Save(rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := updateBranch(repo, repo, rotated.CommitID, winner.CommitID, "", false, ""); err != nil {
		t.Fatal(err)
	}
	takeEvent(rotated.CommitID)

	client := commitmgr.NewCanonicalCommit(repo, rotated.CommitID, strings.Repeat("6", 40), rotated.CreatorName, "File upload", 302)
	client.RepoName, client.Version, client.Encrypted, client.EncVersion = rotated.RepoName, 1, "true", 3
	client.Magic = strings.Repeat("1", 64)
	client.RandomKey = strings.Repeat("2", 96)
	client.Salt = rotated.Salt
	if err := commitmgr.Save(client); err != nil {
		t.Fatal(err)
	}
	var accepted string
	for range 2 {
		current := repomgr.Get(repo)
		if current == nil {
			t.Fatal("failed to reload rotated repository")
		}
		var id string
		if retry, err := genCommitNeedRetry(current, rotated, client, client.RootID, client.CreatorName, true, &id, "", false); retry || err != nil {
			t.Fatalf("upload with stale encryption metadata: retry %v, error %v", retry, err)
		}
		if accepted != "" {
			if id != accepted {
				t.Fatal("retry did not reuse the credential-preserving merge")
			}
			takeEvent("")
			continue
		}
		accepted = id
		takeEvent(client.CommitID)
		takeEvent(accepted)
	}
	merged, err := commitmgr.Load(repo, accepted)
	if err != nil {
		t.Fatal(err)
	}
	if merged.RootID != client.RootID || merged.ParentID.String != rotated.CommitID ||
		merged.SecondParentID.String != client.CommitID || !sameCommitEncryption(rotated, merged) {
		t.Fatal("merge did not preserve both the client's files and the current credentials")
	}
}
