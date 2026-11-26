package seafserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestPostMultiFilesCommitsEverySourceInOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	if err := os.Mkdir(reader.stagedImportRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	const owner = "owner@example.test"
	created, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "fixture", "", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := created.(string)
	config := NewServerConfig(db, nil)
	handlers, stop := reader.FileWriteRPCHandlers(ctx, db, nil, false, NewUserQuota(db, config), NewRepoPasswords(reader), FileMoveLimits{})
	defer stop()
	post := handlers["seafile_post_multi_files"]
	staged := make([]string, 2)
	for i, name := range []string{"first", "second"} {
		staged[i] = filepath.Join(reader.stagedImportRoot, name)
		if err := os.WriteFile(staged[i], []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	args := func(names, paths []string, replace bool) []json.RawMessage {
		t.Helper()
		nameList, err := json.Marshal(names)
		if err != nil {
			t.Fatal(err)
		}
		pathList, err := json.Marshal(paths)
		if err != nil {
			t.Fatal(err)
		}
		arguments := fileWriteRPCArgs(t, repoID, "/", string(nameList), string(pathList), owner)
		flag := json.RawMessage("0")
		if replace {
			flag = json.RawMessage("1")
		}
		return append(arguments, flag)
	}
	current := func() string {
		t.Helper()
		repo, err := reader.Repo(ctx, repoID)
		if err != nil {
			t.Fatal(err)
		}
		return repo.HeadCommitID
	}
	original := current()
	for _, list := range [][][]string{
		{{"a.txt", "b.txt"}, {staged[0], filepath.Join(reader.stagedImportRoot, "absent")}},
		{{"a.txt"}, staged},
		{{"a.txt", ".."}, staged},
	} {
		if result, rpcErr := post(ctx, args(list[0], list[1], false)); rpcErr == nil || result != nil || current() != original {
			t.Fatalf("invalid batch changed branch for names %v: %v %v", list[0], result, rpcErr)
		}
	}
	if err := EnsureFileWriteQueue(ctx, db); err != nil {
		t.Fatal(err)
	}
	if jobs, err := PendingFileWrites(ctx, db); err != nil || len(jobs) != 0 {
		t.Fatalf("a rejected batch published queue work: %v, %v", jobs, err)
	}
	names := []string{"same.txt", "same.txt"}
	result, rpcErr := post(ctx, args(names, staged, false))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var files []importedFile
	if err := json.Unmarshal([]byte(result.(string)), &files); err != nil || len(files) != 2 {
		t.Fatalf("invalid result: %v, %v", result, err)
	}
	if files[0].Name != "same.txt" || files[1].Name != "same (1).txt" || files[0].Size != 5 || files[1].Size != 6 {
		t.Fatalf("unexpected batch order or size: %+v", files)
	}
	beforeReplace := current()
	replace := args(names, staged, true)
	result, rpcErr = post(ctx, replace)
	if rpcErr != nil || current() == beforeReplace {
		t.Fatalf("ordered overwrite did not commit: %v, %v", result, rpcErr)
	}
	var replaced []importedFile
	if err := json.Unmarshal([]byte(result.(string)), &replaced); err != nil || len(replaced) != 2 ||
		replaced[0] != files[0] || replaced[1] != (importedFile{Name: "same.txt", ID: files[1].ID, Size: files[1].Size}) {
		t.Fatalf("ordered overwrite changed the result contract: %v, %v", result, err)
	}
	if _, err := db.Exec(`INSERT INTO UserQuota (user, quota) VALUES (?, 2)`, owner); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := post(ctx, args(names[:1], staged[:1], true)); rpcErr != nil {
		t.Fatalf("a replacement that shrinks the file should not consume quota: %v", rpcErr)
	}
	afterShrink := current()
	if _, rpcErr := post(ctx, args([]string{"new.txt"}, staged[:1], false)); rpcErr == nil || rpcErr.Code != 519 || current() != afterShrink {
		t.Fatalf("batch unexpectedly published beyond quota: %v", rpcErr)
	}
	if _, err := db.Exec(`DELETE FROM UserQuota WHERE user = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_batch BEFORE UPDATE ON GoFileWriteQueue
		BEGIN SELECT RAISE(ABORT, 'fixture queue unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	freed := current()
	if _, rpcErr := post(ctx, args([]string{"w1", "w2"}, staged, false)); rpcErr == nil || current() != freed {
		t.Fatalf("failed queue update moved the head: %v", rpcErr)
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_batch`); err != nil {
		t.Fatal(err)
	}
	type concurrentResult struct {
		value any
		err   *searpc.RPCError
	}
	parallel := make(chan concurrentResult, 2)
	ready := make(chan struct{})
	for i := range 2 {
		arguments := args([]string{"concurrent.txt"}, staged[i:i+1], false)
		go func() {
			<-ready
			value, rpcErr := post(ctx, arguments)
			parallel <- concurrentResult{value: value, err: rpcErr}
		}()
	}
	close(ready)
	namesSeen := make(map[string]bool)
	for range 2 {
		item := <-parallel
		if item.err != nil {
			t.Fatal(item.err)
		}
		var imported []importedFile
		if err := json.Unmarshal([]byte(item.value.(string)), &imported); err != nil || len(imported) != 1 {
			t.Fatalf("invalid concurrent result: %v, %v", item.value, err)
		}
		namesSeen[imported[0].Name] = true
	}
	if !namesSeen["concurrent.txt"] || !namesSeen["concurrent (1).txt"] {
		t.Fatalf("concurrent imports lost name allocations: %v", namesSeen)
	}
}

func TestStagedImportKeepsEarlierCommitWithSameCanonicalHash(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	w := &fileWriter{reader: reader, db: db}
	const user = "owner@example.test"
	created, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "fixture", "", user))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repoID := created.(string)
	var heads []string
	var firstParent string
	for i, root := range []string{strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("1", 40)} {
		repo, err := reader.Repo(ctx, repoID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstParent = repo.HeadCommitID
		}
		tree := &fileTree{reader: reader, repo: repo, root: root, user: user, now: 1700000000}
		id, err := w.publishStagedImport(ctx, tree, `Added "file".`)
		if err != nil {
			t.Fatal(err)
		}
		heads = append(heads, id)
		commit, err := reader.Commit(repoID, id)
		if err != nil || commit.ParentID.String != repo.HeadCommitID {
			t.Fatalf("unexpected parent after replacement: %+v, %v", commit, err)
		}
	}
	first, err := reader.Commit(repoID, heads[0])
	if err != nil || first.ParentID.String != firstParent || heads[0] == heads[2] {
		t.Fatalf("revisiting a root rewrote an earlier commit: %+v, %v", first, err)
	}
}
