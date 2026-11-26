package seafserver

import (
	"context"
	"errors"
	"testing"
)

func TestCopyChecksSourceHeadAndCancellationBeforePublication(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	create := func(name string) string {
		t.Helper()
		value, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, name, "fixture", owner))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		return value.(string)
	}
	source, target := create("source"), create("target")
	quota := NewUserQuota(db, NewServerConfig(db, nil))
	handlers, stop := reader.FileWriteRPCHandlers(ctx, db, nil, false, quota, NewRepoPasswords(reader), FileMoveLimits{})
	defer stop()
	post := func(name string) {
		t.Helper()
		if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, source, "/", name, owner)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	post("transfer.txt")
	writer := &fileWriter{reader: reader, db: db, permissions: &permissionReader{db: db}, quota: quota}
	request := libraryTransfer{fromRepo: source, toRepo: target, user: owner,
		names: []string{"transfer.txt"}, targets: []string{"copied.txt"}}
	prepare := func() (from, to *fileTree, desc string) {
		t.Helper()
		from, to, rpcErr := writer.copyTrees(ctx, request)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		copier := libraryCopy{ctx: ctx, from: from, to: to}
		desc, rpcErr = copier.insert(request, quota)
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		return from, to, desc
	}
	from, to, desc := prepare()
	oldTarget := to.repo.HeadCommitID
	post("added-after-copy.txt")
	if err := writer.publishCopy(ctx, from, to, desc, nil); !errors.Is(err, ErrSQLiteBranchChanged) {
		t.Fatalf("stale source publication returned %v", err)
	}

	from, to, desc = prepare()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancelable, cancel := context.WithCancel(ctx)
	defer cancel()
	published := make(chan error, 1)
	go func() { published <- writer.publishCopy(cancelable, from, to, desc, nil) }()
	cancel()
	publicationErr := <-published
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(publicationErr, context.Canceled) {
		t.Fatalf("canceled publication returned %v", publicationErr)
	}
	repo, err := reader.Repo(ctx, target)
	if err != nil || repo.HeadCommitID != oldTarget {
		t.Fatalf("interrupted copy changed the destination: %v, %v", repo, err)
	}
	if rpcErr := writer.runCopy(ctx, request, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	repo, err = reader.Repo(ctx, target)
	if err != nil || repo.HeadCommitID == oldTarget {
		t.Fatalf("retried copy did not advance the destination: %v, %v", repo, err)
	}
}
