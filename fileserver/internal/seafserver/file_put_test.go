package seafserver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

func TestFilePutChecksHeadAndPublishesDurableContents(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	config := NewServerConfig(db, []byte("[fileserver]\nfixed_block_size=1\n"))
	quota := NewUserQuota(db, config)
	handlers, stopTasks := reader.FileWriteRPCHandlers(ctx, db, nil, false, quota, NewRepoPasswords(reader), FileMoveLimits{})
	defer stopTasks()

	const owner, viewer = "owner@example.test", "reader@example.test"
	created, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "fixture", "", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := created.(string)
	if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, repoID, "/", "报表.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	head, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO SharedRepo (repo_id, from_email, to_email, permission) VALUES (?, ?, ?, 'r')",
		repoID, owner, viewer); err != nil {
		t.Fatal(err)
	}
	payload := append(bytes.Repeat([]byte("x"), 1<<20), 'y')
	path := filepath.Join(dir, "DAV upload")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	put := handlers["seafile_put_file"]
	putArgs := func(path, file, user string, expected json.RawMessage) []json.RawMessage {
		return append(fileWriteRPCArgs(t, repoID, path, "/", file, user), expected)
	}
	nullableHead := json.RawMessage("null")
	for _, tc := range []struct {
		path, file, user string
		code             int
	}{
		{path, "报表.txt", viewer, errRepoAuth},
		{dir, "报表.txt", owner, errBadArgs},
		{path, "missing.txt", owner, errBadArgs},
	} {
		if _, rpcErr := put(ctx, putArgs(tc.path, tc.file, tc.user, nullableHead)); rpcErr == nil || rpcErr.Code != tc.code {
			t.Fatalf("put %s as %s: got %v, want code %d", tc.file, tc.user, rpcErr, tc.code)
		}
	}
	expected, err := json.Marshal(head.HeadCommitID)
	if err != nil {
		t.Fatal(err)
	}
	id, rpcErr := put(ctx, putArgs(path, "报表.txt", owner, expected))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	current, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := reader.Commit(repoID, current.HeadCommitID)
	if err != nil || commit.ParentID.String != head.HeadCommitID {
		t.Fatalf("PUT did not publish over the expected head: %+v, %v", commit, err)
	}
	root, err := reader.Directory(current, current.RootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Entries) != 1 || root.Entries[0].Name != "报表.txt" || root.Entries[0].ID != id {
		t.Fatalf("PUT wrote an unexpected directory: %+v", root.Entries)
	}
	file, err := reader.File(current, id.(string))
	if err != nil || len(file.BlkIDs) != 2 {
		t.Fatalf("expected a two-block file: %+v, %v", file, err)
	}
	var content bytes.Buffer
	for _, block := range file.BlkIDs {
		if err := reader.ReadBlock(current, block, &content); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(content.Bytes(), payload) {
		t.Fatal("stored bytes differ from the upload")
	}
	jobs, err := PendingFileWrites(ctx, db)
	if err != nil || len(jobs) != 1 || jobs[0] != (FileWriteJob{RepoID: repoID, HeadID: current.HeadCommitID}) {
		t.Fatalf("PUT queue does not contain the new head: %+v, %v", jobs, err)
	}
	if repeated, rpcErr := put(ctx, putArgs(path, "报表.txt", owner, nullableHead)); rpcErr != nil || repeated != id {
		t.Fatalf("repeated file content returned %v, %v", repeated, rpcErr)
	}
	if _, rpcErr := put(ctx, putArgs(path, "报表.txt", owner, expected)); rpcErr == nil || rpcErr.Code != 520 {
		t.Fatalf("stale expected head accepted: %v", rpcErr)
	}
	final, err := reader.Repo(ctx, repoID)
	if err != nil || final.HeadCommitID != current.HeadCommitID {
		t.Fatalf("no-op or rejected PUT altered the head: %+v, %v", final, err)
	}
	if root.Entries[0].Size != int64(len(payload)) || !fsmgr.IsRegular(root.Entries[0].Mode) {
		t.Fatalf("unexpected file entry size or mode: %+v", root.Entries[0])
	}
}
