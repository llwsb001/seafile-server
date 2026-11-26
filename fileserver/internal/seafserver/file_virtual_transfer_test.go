package seafserver

import (
	"context"
	"testing"
)

func TestSiblingViewMovePublishesBothHeadsAndQueueEntriesTogether(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	value, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "origin", "fixture", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	origin := value.(string)
	quota := NewUserQuota(db, NewServerConfig(db, nil))
	passwords := NewRepoPasswords(reader)
	handlers, closeTasks := reader.FileWriteRPCHandlers(ctx, db, nil, false, quota, passwords, FileMoveLimits{})
	defer closeTasks()
	for _, path := range []string{"source", "target"} {
		if _, rpcErr := handlers["seafile_mkdir_with_parents"](ctx, fileWriteRPCArgs(t, origin, "/", path, owner)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, origin, "/source", "move.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	view := func(path string) string {
		t.Helper()
		value, rpcErr := reader.createVirtualRepo(ctx, db, fileWriteRPCArgs(t, origin, "/"+path, path, "fixture", owner, ""))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		return value.(string)
	}
	source, target := view("source"), view("target")
	head := func(repoID string) string {
		t.Helper()
		repo, err := reader.Repo(ctx, repoID)
		if err != nil {
			t.Fatal(err)
		}
		return repo.HeadCommitID
	}
	viewJobs := func() int {
		t.Helper()
		var count int
		if err := db.QueryRow("SELECT count(*) FROM GoFileWriteQueue WHERE repo_id IN (?, ?)", source, target).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	originHead, sourceHead, targetHead := head(origin), head(source), head(target)
	if _, err := db.Exec(`CREATE TABLE VirtualMoveFault (repo_id TEXT);
		CREATE TRIGGER fail_virtual_move BEFORE UPDATE OF commit_id ON Branch
		WHEN NEW.repo_id IN (SELECT repo_id FROM VirtualMoveFault)
		BEGIN SELECT RAISE(ABORT, 'fixture branch failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO VirtualMoveFault VALUES (?)", source); err != nil {
		t.Fatal(err)
	}
	writer := &fileWriter{reader: reader, db: db, permissions: &permissionReader{db: db}, quota: quota, passwords: passwords}
	request := libraryTransfer{fromRepo: source, toRepo: target, user: owner,
		names: []string{"move.txt"}, targets: []string{"moved.txt"}}
	if rpcErr := writer.runCrossMove(ctx, request, nil); rpcErr == nil {
		t.Fatal("injected publication failure returned success")
	}
	if head(origin) != originHead || head(source) != sourceHead || head(target) != targetHead || viewJobs() != 0 {
		t.Fatal("failed move published a head or queue entry")
	}
	if _, err := db.Exec("DROP TRIGGER fail_virtual_move"); err != nil {
		t.Fatal(err)
	}
	if rpcErr := writer.runCrossMove(ctx, request, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if head(origin) != originHead || head(source) == sourceHead || head(target) == targetHead || viewJobs() != 2 {
		t.Fatal("retry did not publish both views and queue both origin merges")
	}
}

func TestCrossViewMoveRejectsOverwritingItsSource(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	value, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, "origin", "fixture", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	origin := value.(string)
	handlers, closeTasks := reader.FileWriteRPCHandlers(ctx, db, nil, false, NewUserQuota(db, NewServerConfig(db, nil)), NewRepoPasswords(reader), FileMoveLimits{})
	defer closeTasks()
	if _, rpcErr := handlers["seafile_mkdir_with_parents"](ctx, fileWriteRPCArgs(t, origin, "/", "shared", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, origin, "/shared", "keep.txt", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	value, rpcErr = reader.createVirtualRepo(ctx, db, fileWriteRPCArgs(t, origin, "/shared", "shared", "fixture", owner, ""))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	view := value.(string)
	for _, tc := range []struct{ from, fromPath, to, toPath string }{
		{origin, "/shared", view, "/"},
		{view, "/", origin, "/shared"},
	} {
		args := fileWriteRPCArgs(t, tc.from, tc.fromPath, `["keep.txt"]`, tc.to, tc.toPath, `["keep.txt"]`, "", owner, "", "")
		args[6], args[8], args[9] = []byte("1"), []byte("0"), []byte("1")
		_, rpcErr := handlers["seafile_move_file"](ctx, args)
		if rpcErr == nil || rpcErr.Code != errBadArgs {
			t.Fatalf("overlapping move returned %v", rpcErr)
		}
	}
}
