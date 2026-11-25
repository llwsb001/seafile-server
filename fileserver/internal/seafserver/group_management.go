package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"path/filepath"
	"strconv"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// GroupManagement writes group and membership data through the C SQLite schema.
type GroupManagement struct {
	groups *sql.DB
	shares *sql.DB
}

// OpenGroupManagement opens the ccnet group database for maintenance operations.
func OpenGroupManagement(ccnetDir string, shares *sql.DB) (*GroupManagement, error) {
	groups, err := OpenSQLiteReadWrite(filepath.Join(ccnetDir, "GroupMgr", "groupmgr.db"))
	if err != nil {
		return nil, err
	}
	return &GroupManagement{groups: groups, shares: shares}, nil
}

func (s *GroupManagement) Close() error {
	return s.groups.Close()
}

func (s *GroupManagement) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"create_group":              s.createGroup,
		"remove_group":              s.removeGroup,
		"group_add_member":          s.addMember,
		"group_remove_member":       s.removeMember,
		"group_set_admin":           s.setAdmin(1),
		"group_unset_admin":         s.setAdmin(0),
		"set_group_name":            s.renameGroup,
		"quit_group":                s.quitGroup,
		"remove_group_user":         s.removeGroupUser,
		"set_group_creator":         s.setCreator,
		"seafile_remove_repo_group": s.removeRepoGroup,
	}
}

func (s *GroupManagement) createGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	name, user := stringArgument(args, 0), stringArgument(args, 1)
	if name == nil || user == nil {
		return -1, rpcError(errCCNetInternal, "Group name and user name can not be NULL")
	}
	parent := intArgument(args, 3)
	var id int64
	ok, err := withSQLiteWrite(ctx, s.groups, func(conn *sql.Conn) (bool, error) {
		const insert = `INSERT INTO "Group" (group_name, creator_name, timestamp, parent_group_id) VALUES (?, ?, ?, ?)`
		creator := asciiLower(*user)
		result, err := conn.ExecContext(ctx, insert, *name, creator, time.Now().UnixMicro(), parent)
		if err != nil {
			return false, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return false, err
		}
		if *user != "system admin" {
			if _, err := conn.ExecContext(ctx, "INSERT INTO GroupUser (group_id, user_name, is_staff) VALUES (?, ?, 1)", id, creator); err != nil {
				return false, err
			}
		}

		path := strconv.FormatInt(id, 10)
		if parent > 0 {
			var parentPath string
			if err := conn.QueryRowContext(ctx, "SELECT path FROM GroupStructure WHERE group_id = ?", parent).Scan(&parentPath); err != nil {
				return false, err
			}
			path = parentPath + ", " + path
		}
		if parent == -1 || parent > 0 {
			if _, err := conn.ExecContext(ctx, "INSERT INTO GroupStructure (group_id, path) VALUES (?, ?)", id, path); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	if !ok {
		return groupWriteFailure("Failed to create group", err)
	}
	return id, nil
}

func (s *GroupManagement) removeGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := intArgument(args, 0)
	if id <= 0 {
		return -1, rpcError(errCCNetInternal, "Invalid group_id parameter")
	}
	ok, err := withSQLiteWrite(ctx, s.groups, func(conn *sql.Conn) (bool, error) {
		var children bool
		const hasChildren = `SELECT EXISTS (SELECT 1 FROM "Group" WHERE parent_group_id = ?)`
		if err := conn.QueryRowContext(ctx, hasChildren, id).Scan(&children); err != nil || children {
			return false, err
		}
		for _, query := range []string{
			`DELETE FROM "Group" WHERE group_id = ?`,
			"DELETE FROM GroupUser WHERE group_id = ?",
			"DELETE FROM GroupStructure WHERE group_id = ?",
		} {
			if _, err := conn.ExecContext(ctx, query, id); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	if err != nil {
		return groupWriteFailure("Failed to remove group", err)
	}
	if !ok {
		return -1, nil
	}
	return 0, nil
}

func (s *GroupManagement) addMember(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user, member := intArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if id <= 0 || user == nil || member == nil {
		return -1, rpcError(errCCNetInternal, "Group id and user name and member name can not be NULL")
	}
	const query = "INSERT INTO GroupUser (group_id, user_name, is_staff) VALUES (?, ?, 0)"
	return s.changeMember(ctx, id, false, "Failed to add member to group", query, asciiLower(*member))
}

func (s *GroupManagement) removeMember(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user, member := intArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if user == nil || member == nil {
		return -1, rpcError(errCCNetInternal, "User name and member name can not be NULL")
	}
	const query = "DELETE FROM GroupUser WHERE group_id = ? AND user_name = ?"
	return s.changeMember(ctx, id, *user == *member, "Failed to remove member from group", query, *member)
}

func (s *GroupManagement) quitGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user := intArgument(args, 0), stringArgument(args, 1)
	if id <= 0 || user == nil {
		return -1, rpcError(errCCNetInternal, "Group id and user name can not be NULL")
	}
	const query = "DELETE FROM GroupUser WHERE group_id = ? AND user_name = ?"
	return s.changeMember(ctx, id, false, "Failed to quit group", query, *user)
}

func (s *GroupManagement) changeMember(ctx context.Context, id int, removeSelf bool, message, query, member string) (any, *searpc.RPCError) {
	var missing bool
	ok, err := withSQLiteWrite(ctx, s.groups, func(conn *sql.Conn) (bool, error) {
		const exists = `SELECT NOT EXISTS (SELECT 1 FROM "Group" WHERE group_id = ?)`
		if err := conn.QueryRowContext(ctx, exists, id).Scan(&missing); err != nil || missing || removeSelf {
			return false, err
		}
		_, err := conn.ExecContext(ctx, query, id, member)
		return err == nil, err
	})
	if err != nil {
		return groupWriteFailure(message, err)
	}
	if missing {
		return -1, rpcError(0, "Group not exists")
	}
	if !ok {
		return -1, rpcError(0, "Can not remove myself")
	}
	return 0, nil
}

func (s *GroupManagement) setAdmin(staff int) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, member := intArgument(args, 0), stringArgument(args, 1)
		if id <= 0 || member == nil {
			return -1, rpcError(errCCNetInternal, "Bad arguments")
		}
		const query = "UPDATE GroupUser SET is_staff = ? WHERE group_id = ? AND user_name = ?"
		return execGroupMutation(ctx, s.groups, "Failed to update group administrator", query, staff, id, *member)
	}
}

func (s *GroupManagement) renameGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, name := intArgument(args, 0), stringArgument(args, 1)
	if id <= 0 || name == nil {
		return -1, rpcError(errCCNetInternal, "Bad arguments")
	}
	const query = `UPDATE "Group" SET group_name = ? WHERE group_id = ?`
	return execGroupMutation(ctx, s.groups, "Failed to rename group", query, *name, id)
}

func (s *GroupManagement) setCreator(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, creator := intArgument(args, 0), stringArgument(args, 1)
	if id < 0 || creator == nil {
		return -1, rpcError(errCCNetInternal, "Bad arguments")
	}
	const query = `UPDATE "Group" SET creator_name = ? WHERE group_id = ?`
	return execGroupMutation(ctx, s.groups, "Failed to set group creator", query, *creator, id)
}

func (s *GroupManagement) removeGroupUser(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	user := stringArgument(args, 0)
	if user == nil {
		return -1, rpcError(errCCNetInternal, "Bad arguments")
	}
	return execGroupMutation(ctx, s.groups, "Failed to remove group user", "DELETE FROM GroupUser WHERE user_name = ?", *user)
}

func (s *GroupManagement) removeRepoGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := intArgument(args, 0)
	if id <= 0 {
		return -1, rpcError(errBadArgs, "Wrong group id argument")
	}
	user := stringArgument(args, 1)
	const query = "DELETE FROM RepoGroup WHERE group_id = ?1 AND (?2 IS NULL OR user_name = ?2)"
	_, err := withSQLiteWrite(ctx, s.shares, func(conn *sql.Conn) (bool, error) {
		_, err := conn.ExecContext(ctx, query, id, user)
		return err == nil, err
	})
	if err != nil {
		log.Printf("SQLite group share cleanup: %v", err)
		return -1, nil
	}
	return 0, nil
}

func execGroupMutation(ctx context.Context, db *sql.DB, message, query string, args ...any) (any, *searpc.RPCError) {
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		_, err := conn.ExecContext(ctx, query, args...)
		return err == nil, err
	})
	if err != nil {
		return groupWriteFailure(message, err)
	}
	return 0, nil
}

func groupWriteFailure(message string, err error) (any, *searpc.RPCError) {
	log.Printf("SQLite group write: %v", err)
	return -1, rpcError(0, message)
}
