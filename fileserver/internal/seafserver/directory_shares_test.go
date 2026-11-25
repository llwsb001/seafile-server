package seafserver

import (
	"context"
	"testing"
)

func TestDirectoryShareAtomicPublication(t *testing.T) {
	ctx := context.Background()
	reader, db, _ := virtualTestOrigin(t)

	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	share := directoryShare{repo: testVirtualOrigin, path: "/team", owner: testVirtualOwner,
		user: "reader@example.test", group: 2, permission: "r"}

	exec(`CREATE TRIGGER reject_first_share BEFORE INSERT ON SharedRepo
		BEGIN SELECT RAISE(ABORT, 'fixture write failure'); END;`)
	if result, rpcErr := reader.createDirectoryShare(ctx, db, share, "", false); result != nil || rpcErr == nil {
		t.Fatal("first share did not report the injected failure")
	}
	if state := virtualTestState(t, db); state != [6]int{1, 1, 1, 1, 1, 0} {
		t.Fatalf("first share failure published a virtual repo: %v", state)
	}

	exec("DROP TRIGGER reject_first_share")
	virtual, rpcErr := reader.createDirectoryShare(ctx, db, share, "", false)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var head string
	if err := db.QueryRow("SELECT commit_id FROM Branch WHERE repo_id = ?", virtual).Scan(&head); err != nil {
		t.Fatal(err)
	}

	exec(`CREATE TRIGGER reject_group_share BEFORE INSERT ON RepoGroup
		BEGIN SELECT RAISE(ABORT, 'fixture group write failure'); END;`)
	if result, rpcErr := reader.createDirectoryShare(ctx, db, share, "", true); result != nil || rpcErr == nil {
		t.Fatal("reused share did not report the injected failure")
	}
	var resultHead, permission string
	var groups int
	err := db.QueryRow(`SELECT b.commit_id, s.permission, (SELECT COUNT(*) FROM RepoGroup WHERE repo_id = b.repo_id)
		FROM Branch b JOIN SharedRepo s ON b.repo_id = s.repo_id
		WHERE b.repo_id = ? AND s.to_email = ?`, virtual, share.user).Scan(&resultHead, &permission, &groups)
	if err != nil || resultHead != head || permission != "r" || groups != 0 {
		t.Fatalf("failed reuse changed existing access or virtual head: %v", err)
	}
	if state := virtualTestState(t, db); state != [6]int{2, 2, 2, 2, 2, 1} {
		t.Fatalf("failed reuse changed the published rows: %v", state)
	}

	exec("DROP TRIGGER reject_group_share")
	result, rpcErr := reader.createDirectoryShare(ctx, db, share, "", true)
	if rpcErr != nil || result != virtual {
		t.Fatal("retry did not reuse the existing virtual repository")
	}
}
