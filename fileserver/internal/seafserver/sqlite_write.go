package seafserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
)

const sqliteBusyTimeout = "_pragma=busy_timeout(250)"

// OpenSQLiteReadWrite prepares a write pool for an existing SQLite database.
// SQLite opens the file on the first operation.
func OpenSQLiteReadWrite(dbPath string) (*sql.DB, error) {
	uri, err := sqliteFileURL(dbPath)
	if err != nil {
		return nil, err
	}
	uri.RawQuery = "mode=rw&" + sqliteBusyTimeout
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// withSQLiteWrite holds the SQLite write lock from the first read through
// commit. A callback that returns false leaves the transaction uncommitted.
func withSQLiteWrite(ctx context.Context, db *sql.DB, write func(*sql.Conn) (bool, error)) (bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// A late cancellation must not hide whether SQLite began or committed the transaction.
	transaction := context.WithoutCancel(ctx)
	if _, err := conn.ExecContext(transaction, "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// A failed SQLite COMMIT can leave the transaction active.
		if _, err := conn.ExecContext(transaction, "ROLLBACK"); err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if ok, err := write(conn); err != nil || !ok {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(transaction, "COMMIT"); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}
