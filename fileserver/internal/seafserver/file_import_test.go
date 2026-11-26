package seafserver

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPostFileRestrictsStagingAndPublishesOnlyAuthorizedWrites(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	staging := reader.stagedImportRoot
	if err := os.MkdirAll(filepath.Join(staging, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := NewServerConfig(db, []byte("[fileserver]\nfixed_block_size=1\n"))
	handlers, stop := reader.FileWriteRPCHandlers(ctx, db, nil, false, NewUserQuota(db, config), NewRepoPasswords(reader), FileMoveLimits{})
	defer stop()
	const owner, viewer = "owner@example.test", "reader@example.test"
	created, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "fixture", "", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := created.(string)
	if _, err := db.Exec(`INSERT INTO SharedRepo (repo_id, from_email, to_email, permission) VALUES (?, ?, ?, 'r')`,
		repoID, owner, viewer); err != nil {
		t.Fatal(err)
	}
	post := handlers["seafile_post_file"]
	current := func() string {
		t.Helper()
		repo, err := reader.Repo(ctx, repoID)
		if err != nil {
			t.Fatal(err)
		}
		return repo.HeadCommitID
	}
	original := current()
	out := filepath.Join(dir, "outside")
	inside := filepath.Join(staging, "private", "content")
	for _, source := range []string{inside, out} {
		if err := os.WriteFile(source, []byte("first payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	leafLink := filepath.Join(staging, "link")
	if err := os.Symlink(inside, leafLink); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(staging, "linked-dir")
	if err := os.Symlink(filepath.Dir(inside), dirLink); err != nil {
		t.Fatal(err)
	}
	hardLink := filepath.Join(staging, "hard-link")
	if err := os.Link(out, hardLink); err != nil {
		t.Fatal(err)
	}
	publicDir := filepath.Join(staging, "shared-dir")
	if err := os.Mkdir(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	publicFile := filepath.Join(publicDir, "source")
	if err := os.WriteFile(publicFile, []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(staging, "pipe")
	if err := unix.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{out, leafLink, filepath.Join(dirLink, "content"), hardLink, publicFile, pipe, "relative"} {
		_, rpcErr := post(ctx, fileWriteRPCArgs(t, repoID, source, "/", "out.txt", owner))
		if rpcErr == nil || rpcErr.Code != errBadArgs || current() != original {
			t.Fatalf("invalid staging source %q altered the branch: %v", source, rpcErr)
		}
	}
	_, rpcErr = post(ctx, fileWriteRPCArgs(t, repoID, inside, "/", "out.txt", viewer))
	if rpcErr == nil || rpcErr.Code != errRepoAuth || current() != original {
		t.Fatalf("read-only share could import: %v", rpcErr)
	}
	if _, err := db.Exec(`INSERT INTO UserQuota (user, quota) VALUES (?, 2)`, owner); err != nil {
		t.Fatal(err)
	}
	_, rpcErr = post(ctx, fileWriteRPCArgs(t, repoID, inside, "/", "quota.txt", owner))
	if rpcErr == nil || rpcErr.Code != 519 || current() != original {
		t.Fatalf("quota was not enforced: %v", rpcErr)
	}
	if _, err := db.Exec(`DELETE FROM UserQuota WHERE user = ?`, owner); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		ret, rpcErr := post(ctx, fileWriteRPCArgs(t, repoID, inside, "/", "out.txt", owner))
		if ret != 0 || rpcErr != nil {
			t.Fatalf("staged import did not succeed: %v, %v", ret, rpcErr)
		}
	}
	repo, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reader.Directory(repo, repo.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries.Entries) != 2 || findEntry(entries.Entries, "out.txt") == -1 || findEntry(entries.Entries, "out (1).txt") == -1 {
		t.Fatalf("unexpected imported names: %v", entries.Entries)
	}
	file, err := reader.File(repo, entries.Entries[0].ID)
	if err != nil || len(file.BlkIDs) != 1 {
		t.Fatalf("unexpected imported object: %+v, %v", file, err)
	}
	var payload bytes.Buffer
	if err := reader.ReadBlock(repo, file.BlkIDs[0], &payload); err != nil || payload.String() != "first payload" {
		t.Fatalf("unexpected imported bytes: %q, %v", payload.Bytes(), err)
	}
	jobs, err := PendingFileWrites(ctx, db)
	if err != nil || len(jobs) != 1 || jobs[0].HeadID != repo.HeadCommitID {
		t.Fatalf("missing durable import job: %+v, %v", jobs, err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_staged_update BEFORE UPDATE ON GoFileWriteQueue
		BEGIN SELECT RAISE(ABORT, 'fixture queue unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	_, rpcErr = post(ctx, fileWriteRPCArgs(t, repoID, inside, "/", "blocked.txt", owner))
	if rpcErr == nil || current() != repo.HeadCommitID {
		t.Fatalf("failed queue update changed head: %v", rpcErr)
	}
}

func TestStagedSourceDetectsInPlaceModification(t *testing.T) {
	root := t.TempDir()
	reader := &Reader{stagedImportRoot: root}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source")
	if err := os.WriteFile(path, []byte("first payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := reader.openStagedSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if !source.unchanged() {
		t.Fatal("opened source was not stable")
	}
	if err := os.WriteFile(path, []byte("different data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if source.unchanged() {
		t.Fatal("in-place source replacement was accepted")
	}
}
