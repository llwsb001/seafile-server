package seafserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

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
