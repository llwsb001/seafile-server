package seafserver

import (
	"context"
	"testing"
	"time"
)

func TestPruneExpiredTrashUsesInclusiveBoundaryAndDatabaseOverride(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const boundary = "33333333-3333-4333-8333-333333333333"
	const newer = "44444444-4444-4444-8444-444444444444"
	const older = "55555555-5555-4555-8555-555555555555"
	const now = 1_700_000_000
	cutoff := int64(now) - int64(oneDay/time.Second)
	for _, fixture := range []struct {
		id      string
		deleted int64
	}{
		{older, cutoff - 1},
		{boundary, cutoff},
		{newer, cutoff + 1},
	} {
		if _, err := db.Exec(`INSERT INTO RepoTrash (repo_id, head_id, del_time) VALUES (?, ?, ?)`,
			fixture.id, emptyObjectID, fixture.deleted); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO SeafileConf (cfg_group, cfg_key, value) VALUES ('library_trash', 'expire_days', '1')`); err != nil {
		t.Fatal(err)
	}
	config := NewServerConfig(db, []byte("[library_trash]\nexpire_days=5\n"))
	if err := reader.pruneExpiredTrash(ctx, db, config, now); err != nil {
		t.Fatal(err)
	}
	var marked, kept int
	err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM GarbageRepos WHERE repo_id IN (?, ?)),
		(SELECT COUNT(*) FROM RepoTrash WHERE repo_id = ?)`, older, boundary, newer).Scan(&marked, &kept)
	if err != nil || marked != 2 || kept != 1 {
		t.Fatalf("unexpected expiry: %d marked, %d newer retained, %v", marked, kept, err)
	}
}

func TestEmptyTrashRollsBackAllMarksWhenMetadataDeleteFails(t *testing.T) {
	ctx := context.Background()
	db, dir := openFileWriteFixture(t)
	reader := NewReader(db, dir)
	const owner = "owner@example.test"
	handlers := reader.RepoTrashWriteRPCHandlers(db, false)
	var lastID string
	for _, name := range []string{"first", "second"} {
		id, rpcErr := reader.createRepo(ctx, db, fileWriteRPCArgs(t, name, "fixture", owner))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		lastID = id.(string)
		if _, rpcErr := handlers["seafile_destroy_repo"](ctx, fileWriteRPCArgs(t, lastID)); rpcErr != nil {
			t.Fatal(rpcErr)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_prune BEFORE DELETE ON RepoTrash
		WHEN OLD.repo_id = '` + lastID + `'
		BEGIN SELECT RAISE(ABORT, 'fixture metadata failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := handlers["empty_repo_trash"](ctx, nil); rpcErr == nil {
		t.Fatal("metadata failure unexpectedly emptied trash")
	}
	var trashed, marked int
	err := db.QueryRow("SELECT (SELECT COUNT(*) FROM RepoTrash), (SELECT COUNT(*) FROM GarbageRepos)").Scan(&trashed, &marked)
	if err != nil || trashed != 2 || marked != 0 {
		t.Fatalf("failed empty changed one of the libraries: %d trashed, %d marked, %v", trashed, marked, err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_prune"); err != nil {
		t.Fatal(err)
	}
	if _, rpcErr := handlers["empty_repo_trash"](ctx, nil); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	err = db.QueryRow("SELECT (SELECT COUNT(*) FROM RepoTrash), (SELECT COUNT(*) FROM GarbageRepos)").Scan(&trashed, &marked)
	if err != nil || trashed != 0 || marked != 2 {
		t.Fatalf("retry left inconsistent trash: %d trashed, %d marked, %v", trashed, marked, err)
	}
}
