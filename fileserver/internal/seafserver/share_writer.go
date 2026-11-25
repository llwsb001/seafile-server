package seafserver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// ShareWriter updates ordinary library shares in an existing SQLite database.
type ShareWriter struct {
	db *sql.DB
}

// OpenShareWriter prepares the write pool. SQLite opens the file on the first write.
func OpenShareWriter(dbPath string) (*ShareWriter, error) {
	uri, err := sqliteFileURL(dbPath)
	if err != nil {
		return nil, err
	}
	uri.RawQuery = "mode=rw&_pragma=busy_timeout(250)"
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &ShareWriter{db: db}, nil
}

func (s *ShareWriter) Close() error {
	return s.db.Close()
}

// RPCHandlers exposes share mutations when the caller enables SQLite writes.
func (s *ShareWriter) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_add_share":    s.addShare,
		"set_share_permission": s.setSharePermission,
		"seafile_remove_share": s.removeShare,
	}
}

func invalidShareArguments(message string) (any, *searpc.RPCError) {
	return -1, rpcError(errBadArgs, message)
}

func validSharePermission(permission string) bool {
	return permission == "r" || permission == "rw"
}

func (s *ShareWriter) write(ctx context.Context, repoID string, failed *searpc.RPCError, query string, args ...any) (any, *searpc.RPCError) {
	eligible, err := s.exec(ctx, repoID, query, args...)
	if err != nil {
		log.Printf("SQLite share write: %v", err)
		return -1, failed
	}
	if !eligible {
		return invalidShareArguments("Sharing writes require an existing ordinary library")
	}
	return 0, nil
}

func (s *ShareWriter) exec(ctx context.Context, repoID, query string, args ...any) (bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	// Hold the writer lock from the scope check through the mutation.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// A failed SQLite COMMIT can leave the transaction active.
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	const scope = `
SELECT EXISTS (SELECT 1 FROM Repo WHERE repo_id = ?)
   AND NOT EXISTS (SELECT 1 FROM VirtualRepo WHERE repo_id = ?)`
	var eligible bool
	if err := conn.QueryRowContext(ctx, scope, repoID, repoID).Scan(&eligible); err != nil || !eligible {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

func (s *ShareWriter) addShare(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, from, to, permission := stringArgument(args, 0), stringArgument(args, 1),
		stringArgument(args, 2), stringArgument(args, 3)
	if repoID == nil || from == nil || to == nil || permission == nil {
		return invalidShareArguments("Missing args")
	}
	if !validRepoID(*repoID) {
		return invalidShareArguments("Invalid repo_id parameter")
	}
	if *from == *to {
		return invalidShareArguments("Can not share repo to myself")
	}
	if !validSharePermission(*permission) {
		return invalidShareArguments("Invalid permission parameter")
	}
	const insert = `
INSERT INTO SharedRepo (repo_id, from_email, to_email, permission)
SELECT ?1, ?2, ?3, ?4
 WHERE NOT EXISTS (SELECT 1 FROM SharedRepo WHERE repo_id = ?1 AND from_email = ?2 AND to_email = ?3)`
	return s.write(ctx, *repoID, nil, insert, *repoID, asciiLower(*from), asciiLower(*to), *permission)
}

func (s *ShareWriter) setSharePermission(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, from, to, permission := stringArgument(args, 0), stringArgument(args, 1),
		stringArgument(args, 2), stringArgument(args, 3)
	if repoID == nil || from == nil || to == nil || permission == nil {
		return invalidShareArguments("Arguments should not be empty")
	}
	if !validRepoID(*repoID) {
		return invalidShareArguments("Invalid repo_id parameter")
	}
	if !validSharePermission(*permission) {
		return invalidShareArguments("Invalid permission parameter")
	}
	const update = `UPDATE SharedRepo SET permission = ? WHERE repo_id = ? AND from_email = ? AND to_email = ?`
	return s.write(ctx, *repoID, nil, update, *permission, *repoID, asciiLower(*from), asciiLower(*to))
}

func (s *ShareWriter) removeShare(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, from, to := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if repoID == nil || from == nil || to == nil {
		return invalidShareArguments("Missing args")
	}
	// C accepts a malformed ID as a successful no-op when no row matches.
	if !validRepoID(*repoID) {
		return 0, nil
	}
	const remove = `DELETE FROM SharedRepo WHERE repo_id = ? AND from_email = ? AND to_email = ?`
	return s.write(ctx, *repoID, nil, remove, *repoID, *from, *to)
}
