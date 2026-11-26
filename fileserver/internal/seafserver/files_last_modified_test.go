package seafserver

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

func TestFilesLastModifiedFollowsCurrentNamesAndObjects(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	r := NewReader(db, dir)
	value, rpcErr := r.createRepo(ctx, db, fileWriteRPCArgs(t, "mtime", "", "owner@example.test"))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := value.(string)
	repo, err := r.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	base, changed := strings.Repeat("a", 40), strings.Repeat("b", 40)
	m := func(name, id string) *fsmgr.SeafDirent {
		return &fsmgr.SeafDirent{Name: name, ID: id, Mode: syscall.S_IFREG}
	}
	head := repo.HeadCommitID
	write := func(timestamp int64, dent ...*fsmgr.SeafDirent) {
		t.Helper()
		dir, err := fsmgr.NewSeafdir(1, dent)
		if err != nil {
			t.Fatal(err)
		}
		var object bytes.Buffer
		if err := dir.ToData(&object); err != nil {
			t.Fatal(err)
		}
		if err := r.fs.Write(repoID, dir.DirID, &object, false); err != nil {
			t.Fatal(err)
		}
		commit := commitmgr.NewCanonicalCommit(repoID, head, dir.DirID, "owner@example.test", "change", timestamp)
		commit.RepoName, commit.Version = "mtime", 1
		if err := r.writeInitialRepoCommit(commit); err != nil {
			t.Fatal(err)
		}
		head = commit.CommitID
	}
	first := repo.LastModificationTime + 10
	write(first, m("always", base), m("renameme", base), m("recreated", base), m("modified", base))
	write(first+1, m("always", base), m("renamed", base), m("modified", base))
	write(first+2, m("always", base), m("renamed", base), m("recreated", base), m("modified", changed))
	if _, err := db.Exec("UPDATE Branch SET commit_id = ? WHERE repo_id = ?", head, repoID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO RepoHistoryLimit(repo_id, days) VALUES (?, 0)", repoID); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		limit string
		want  []fileLastModified
	}{
		{"-1", []fileLastModified{
			{"always", first}, {"modified", first + 2}, {"recreated", first + 2}, {"renamed", first + 1},
		}},
		{"1", []fileLastModified{
			{"always", first + 2}, {"modified", first + 2}, {"recreated", first + 2}, {"renamed", first + 2},
		}},
	} {
		args := append(fileWriteRPCArgs(t, repoID, "/"), json.RawMessage(tt.limit))
		got, rpcErr := r.RPCHandlers(nil)["seafile_calc_files_last_modified"](ctx, args)
		if rpcErr != nil || !slices.Equal(got.([]fileLastModified), tt.want) {
			t.Fatalf("limit %s = (%v, %v), want %v", tt.limit, got, rpcErr, tt.want)
		}
	}
}
