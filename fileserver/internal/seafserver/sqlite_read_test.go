package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestGetRepoWaitsForContentionBeyondWriteBusyTimeout(t *testing.T) {
	_, writer, root := virtualTestOrigin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := OpenSQLiteReadOnly(ctx, filepath.Join(root, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	reader := NewReader(db, root)
	args := []json.RawMessage{json.RawMessage(`"` + testVirtualOrigin + `"`)}
	started := make(chan struct{})
	read := make(chan bool, 1)
	go func() {
		close(started)
		result, rpcErr := reader.getRepo(ctx, args)
		read <- result != nil && rpcErr == nil
	}()
	<-started
	select {
	case result := <-read:
		t.Fatalf("repo lookup returned %v while the database was locked", result)
	case <-time.After(500 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if result := <-read; !result {
		t.Fatal("repo lookup did not return the existing repo after the write committed")
	}

	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if result, rpcErr := reader.getRepo(ctx, args); result != nil || rpcErr == nil ||
		rpcErr.Code != errInternal || rpcErr.Message != "Repository database is busy" {
		t.Fatalf("busy repo lookup returned %v, %v", result, rpcErr)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	missing := []json.RawMessage{json.RawMessage(`"00000000-0000-4000-a000-000000000000"`)}
	if result, rpcErr := reader.getRepo(ctx, missing); result != nil || rpcErr != nil {
		t.Fatalf("missing repo lookup returned %v, %v", result, rpcErr)
	}
}

func TestSQLiteReadOnlyWaitsForWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "seafile.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, "CREATE TABLE stats (size INTEGER)"); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenSQLiteReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO stats VALUES (42)"); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	read := make(chan error, 1)
	var size int64
	go func() {
		close(started)
		read <- reader.QueryRowContext(ctx, "SELECT size FROM stats").Scan(&size)
	}()
	<-started
	select {
	case err := <-read:
		t.Fatalf("reader returned during write transaction: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil || size != 42 {
		t.Fatalf("size = %d, err = %v, want 42", size, err)
	}
}
