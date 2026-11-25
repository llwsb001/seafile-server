package seafserver

import (
	"context"
	"database/sql"
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
	db, err := OpenSQLiteReadWrite(dbPath)
	if err != nil {
		return nil, err
	}
	return &ShareWriter{db: db}, nil
}

func (s *ShareWriter) Close() error {
	return s.db.Close()
}

// RPCHandlers exposes share mutations when the caller enables SQLite writes.
func (s *ShareWriter) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_add_share":          s.addShare,
		"set_share_permission":       s.setSharePermission,
		"seafile_remove_share":       s.removeShare,
		"seafile_group_share_repo":   s.addGroupShare,
		"set_group_repo_permission":  s.setGroupPermission,
		"seafile_group_unshare_repo": s.removeGroupShare,
		"set_inner_pub_repo":         s.setPublicShare,
		"unset_inner_pub_repo":       s.unsetPublicShare,
	}
}

func invalidShareArguments(message string) (any, *searpc.RPCError) {
	return -1, rpcError(errBadArgs, message)
}

func validSharePermission(permission string) bool {
	return permission == "r" || permission == "rw"
}

const insertPersonalShareSQL = `
INSERT INTO SharedRepo (repo_id, from_email, to_email, permission)
SELECT ?1, ?2, ?3, ?4
 WHERE NOT EXISTS (SELECT 1 FROM SharedRepo WHERE repo_id = ?1 AND from_email = ?2 AND to_email = ?3)`

const insertGroupShareSQL = "INSERT INTO RepoGroup (repo_id, group_id, user_name, permission) VALUES (?, ?, ?, ?)"

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
	return withSQLiteWrite(ctx, s.db, func(conn *sql.Conn) (bool, error) {
		const scope = `
SELECT EXISTS (SELECT 1 FROM Repo WHERE repo_id = ?)
   AND NOT EXISTS (SELECT 1 FROM VirtualRepo WHERE repo_id = ?)`
		var eligible bool
		if err := conn.QueryRowContext(ctx, scope, repoID, repoID).Scan(&eligible); err != nil || !eligible {
			return false, err
		}
		_, err := conn.ExecContext(ctx, query, args...)
		return err == nil, err
	})
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
	return s.write(ctx, *repoID, nil, insertPersonalShareSQL, *repoID, asciiLower(*from), asciiLower(*to), *permission)
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

func (s *ShareWriter) addGroupShare(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, groupID := stringArgument(args, 0), intArgument(args, 1)
	user, permission := stringArgument(args, 2), stringArgument(args, 3)
	if repoID == nil || groupID <= 0 || user == nil || permission == nil {
		return invalidShareArguments("Bad input argument")
	}
	if !validRepoID(*repoID) {
		return invalidShareArguments("Invalid repo id")
	}
	if !validSharePermission(*permission) {
		return invalidShareArguments("Invalid permission parameter")
	}
	return s.write(ctx, *repoID, nil, insertGroupShareSQL, *repoID, groupID, *user, *permission)
}

func (s *ShareWriter) setGroupPermission(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	groupID, repoID, permission := intArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if repoID == nil || permission == nil {
		return invalidShareArguments("Arguments should not be empty")
	}
	if !validRepoID(*repoID) {
		return invalidShareArguments("Invalid repo id")
	}
	if !validSharePermission(*permission) {
		return invalidShareArguments("Invalid permission parameter")
	}
	const update = `UPDATE RepoGroup SET permission = ? WHERE repo_id = ? AND group_id = ?`
	return s.write(ctx, *repoID, nil, update, *permission, *repoID, groupID)
}

func (s *ShareWriter) removeGroupShare(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, groupID, user := stringArgument(args, 0), intArgument(args, 1), stringArgument(args, 2)
	if repoID == nil || user == nil {
		return invalidShareArguments("User name and repo id can not be NULL")
	}
	if !validRepoID(*repoID) {
		return invalidShareArguments("Invalid repo id")
	}
	const remove = `DELETE FROM RepoGroup WHERE group_id = ? AND repo_id = ?`
	return s.write(ctx, *repoID, nil, remove, groupID, *repoID)
}

const errPublicShareWrite = 500

func (s *ShareWriter) setPublicShare(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil {
		return invalidShareArguments("Bad args")
	}
	if !validRepoID(*repoID) {
		return invalidShareArguments("Invalid repo id")
	}
	// The C public-share RPC binds a missing or null permission as SQL NULL.
	const replace = `REPLACE INTO InnerPubRepo (repo_id, permission) VALUES (?, ?)`
	return s.write(ctx, *repoID, rpcError(errPublicShareWrite, "Internal error"), replace, *repoID, stringArgument(args, 1))
}

func (s *ShareWriter) unsetPublicShare(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID := stringArgument(args, 0)
	if repoID == nil {
		return invalidShareArguments("Bad args")
	}
	if !validRepoID(*repoID) {
		return invalidShareArguments("Invalid repo id")
	}
	const remove = `DELETE FROM InnerPubRepo WHERE repo_id = ?`
	return s.write(ctx, *repoID, rpcError(errPublicShareWrite, "Internal error"), remove, *repoID)
}
