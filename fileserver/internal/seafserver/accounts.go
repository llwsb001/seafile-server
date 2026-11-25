package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// Accounts administers local users and their references in the SQLite databases.
type Accounts struct {
	db                           *sql.DB
	usersPath, coreURI, groupURI string
}

// OpenAccounts opens the writable local account database.
func OpenAccounts(ccnetDir, corePath string) (*Accounts, error) {
	usersPath := filepath.Join(ccnetDir, "PeerMgr", "usermgr.db")
	core, err := sqliteFileURL(corePath)
	if err != nil {
		return nil, err
	}
	core.RawQuery = "mode=rw"
	group, err := sqliteFileURL(filepath.Join(ccnetDir, "GroupMgr", "groupmgr.db"))
	if err != nil {
		return nil, err
	}
	group.RawQuery = "mode=rw"
	db, err := OpenSQLiteReadWrite(usersPath)
	if err != nil {
		return nil, err
	}
	return &Accounts{db: db, usersPath: usersPath, coreURI: core.String(), groupURI: group.String()}, nil
}

func (a *Accounts) Close() error {
	return a.db.Close()
}

func (a *Accounts) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"add_emailuser":             a.add,
		"remove_emailuser":          a.remove,
		"validate_emailuser":        a.validate,
		"update_emailuser":          a.update,
		"update_role_emailuser":     a.updateRole,
		"update_emailuser_id":       a.rename,
		"get_emailuser_with_import": a.getWithImport,
		"get_emailuser_by_id":       a.getByID,
		"get_emailusers":            a.list,
		"search_emailusers":         a.search,
		"get_superusers":            a.superusers,
		"count_emailusers":          a.count(1),
		"count_inactive_emailusers": a.count(0),
		"get_emailusers_in_list":    a.inList,
	}
}

func (a *Accounts) add(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email, password := stringArgument(args, 0), stringArgument(args, 1)
	if email == nil || password == nil {
		return -1, rpcError(errCCNetInternal, "Email and passwd can not be NULL")
	}
	stored, err := newAccountPassword(*password)
	if err != nil {
		return -1, nil
	}
	_, err = a.db.ExecContext(ctx, `INSERT INTO EmailUser (email, passwd, is_staff, is_active, ctime)
		VALUES (?, ?, ?, ?, ?)`, asciiLower(*email), stored, intArgument(args, 2), intArgument(args, 3), time.Now().UnixMicro())
	return accountWriteResult(err)
}

func (a *Accounts) validate(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email, password := stringArgument(args, 0), stringArgument(args, 1)
	if email == nil || password == nil {
		return -1, rpcError(errCCNetInternal, "Email and passwd can not be NULL")
	}
	var id int
	var stored string
	err := a.db.QueryRowContext(ctx, `SELECT id, passwd FROM EmailUser
		WHERE email IN (?, ?) ORDER BY email = ? DESC LIMIT 1`, *email, asciiLower(*email), *email).Scan(&id, &stored)
	if err != nil {
		return -1, nil
	}
	matches, upgrade := checkAccountPassword(*password, stored)
	if !matches {
		return -1, nil
	}
	if upgrade {
		replacement, err := newAccountPassword(*password)
		if err == nil {
			_, err = a.db.ExecContext(ctx, "UPDATE EmailUser SET passwd = ? WHERE id = ? AND passwd = ?", replacement, id, stored)
		}
		if err != nil {
			log.Print("validate_emailuser: password upgrade failed")
		}
	}
	return 0, nil
}

func (a *Accounts) update(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	source, password := stringArgument(args, 0), stringArgument(args, 2)
	if source == nil || *source != "DB" || password == nil {
		return -1, nil
	}
	id, staff, active := intArgument(args, 1), intArgument(args, 3), intArgument(args, 4)
	if *password == "!" {
		_, err := a.db.ExecContext(ctx, "UPDATE EmailUser SET is_staff = ?, is_active = ? WHERE id = ?", staff, active, id)
		return accountWriteResult(err)
	}
	stored, err := newAccountPassword(*password)
	if err != nil {
		return -1, nil
	}
	_, err = a.db.ExecContext(ctx, "UPDATE EmailUser SET passwd = ?, is_staff = ?, is_active = ? WHERE id = ?", stored, staff, active, id)
	return accountWriteResult(err)
}

func (a *Accounts) updateRole(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email, role := stringArgument(args, 0), stringArgument(args, 1)
	if email == nil || role == nil {
		return -1, nil
	}
	_, err := withSQLiteWrite(ctx, a.db, func(conn *sql.Conn) (bool, error) {
		update, err := conn.ExecContext(ctx, "UPDATE UserRole SET role = ? WHERE email = ?", *role, *email)
		if err != nil {
			return false, err
		}
		count, err := update.RowsAffected()
		if err != nil || count != 0 {
			return err == nil, err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO UserRole (role, email) VALUES (?, ?)", *role, *email)
		return err == nil, err
	})
	return accountWriteResult(err)
}

func (a *Accounts) remove(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	source, email := stringArgument(args, 0), stringArgument(args, 1)
	if email == nil {
		return -1, rpcError(errCCNetInternal, "Email can not be NULL")
	}
	_, err := withSQLiteWrite(ctx, a.db, func(conn *sql.Conn) (bool, error) {
		if _, err := conn.ExecContext(ctx, "DELETE FROM UserRole WHERE email = ?", *email); err != nil {
			return false, err
		}
		if source != nil && *source == "DB" {
			_, err := conn.ExecContext(ctx, "DELETE FROM EmailUser WHERE email = ?", *email)
			return err == nil, err
		}
		return true, nil
	})
	if source == nil || *source != "DB" {
		return -1, nil
	}
	return accountWriteResult(err)
}

func (a *Accounts) rename(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	oldEmail, newEmail := stringArgument(args, 0), stringArgument(args, 1)
	if oldEmail == nil || newEmail == nil {
		return -1, rpcError(errCCNetInternal, "Bad arguments")
	}
	// This short-lived pool confines the attached schemas to this operation.
	db, err := OpenSQLiteReadWrite(a.usersPath)
	if err != nil {
		return -1, nil
	}
	defer db.Close()
	_, err = withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS core", a.coreURI); err != nil {
			return false, err
		}
		if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS groups", a.groupURI); err != nil {
			return false, err
		}
		for _, query := range []string{
			"UPDATE core.RepoOwner SET owner_id = ? WHERE owner_id = ?",
			"UPDATE core.SharedRepo SET from_email = ? WHERE from_email = ?",
			"UPDATE core.SharedRepo SET to_email = ? WHERE to_email = ?",
			"UPDATE groups.GroupUser SET user_name = ? WHERE user_name = ?",
			"UPDATE core.RepoUserToken SET email = ? WHERE email = ?",
			"UPDATE core.FolderUserPerm SET user = ? WHERE user = ?",
			"UPDATE main.EmailUser SET email = ? WHERE email = ?",
			"UPDATE core.UserQuota SET user = ? WHERE user = ?",
		} {
			if _, err := conn.ExecContext(ctx, query, *newEmail, *oldEmail); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return accountWriteResult(err)
}

func accountWriteResult(err error) (any, *searpc.RPCError) {
	if err != nil {
		return -1, nil
	}
	return 0, nil
}

const realAccounts = "e.email NOT LIKE '%@seafile_group'"

func (a *Accounts) getByID(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	user, err := scanAccount(a.db.QueryRowContext(ctx,
		accountColumns+"e.reference_id"+accountTables+" WHERE e.id = ?", intArgument(args, 0)))
	return identityResult("get_emailuser_by_id", user, err), nil
}

func (a *Accounts) getWithImport(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email := stringArgument(args, 0)
	if email == nil {
		return nil, rpcError(errCCNetInternal, "Email can not be NULL")
	}
	user, err := queryEmailUser(ctx, a.db, *email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, rpcError(errCCNetInternal, "Database error")
	}
	return user, nil
}

func (a *Accounts) list(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	source, status := stringArgument(args, 0), stringArgument(args, 3)
	if source == nil || *source != "DB" {
		return nil, nil
	}
	// The C implementation produces an invalid double-WHERE query for these filters.
	if status != nil && (*status == "active" || *status == "inactive") {
		return nil, nil
	}
	return a.page(ctx, realAccounts, intArgument(args, 1), intArgument(args, 2), nil), nil
}

func (a *Accounts) search(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	source, keyword := stringArgument(args, 0), stringArgument(args, 1)
	if source == nil || *source != "DB" || keyword == nil {
		return nil, nil
	}
	return a.page(ctx, realAccounts+" AND e.email LIKE ?", intArgument(args, 2), intArgument(args, 3), []any{"%" + *keyword + "%"}), nil
}

func (a *Accounts) superusers(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	return a.page(ctx, realAccounts+" AND e.is_staff = 1", -1, -1, nil), nil
}

func (a *Accounts) inList(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	source, emails := stringArgument(args, 0), stringArgument(args, 1)
	if source == nil || emails == nil {
		return nil, rpcError(errCCNetInternal, "Bad arguments")
	}
	var decoded any
	if err := json.Unmarshal([]byte(*emails), &decoded); err != nil {
		return nil, rpcError(0, "Bad args.")
	}
	switch decoded.(type) {
	case map[string]any:
		return nil, nil
	case []any:
	default:
		return nil, rpcError(0, "Bad args.")
	}
	values := decoded.([]any)
	const maxUsers = 20
	if len(values) > maxUsers {
		return nil, rpcError(0, "Number of users exceeds 20.")
	}
	if *source != "DB" {
		return nil, nil
	}
	params := make([]any, maxUsers)
	for i := range params {
		if i >= len(values) {
			params[i] = ""
		} else if value, ok := values[i].(string); ok {
			params[i] = value
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", maxUsers), ",")
	query := accountColumns + "NULL" + accountTables + " WHERE e.email IN (" + placeholders + ")"
	users := a.queryList(ctx, query, params)
	slices.Reverse(users)
	return users, nil
}

func (a *Accounts) page(ctx context.Context, condition string, start, limit int, args []any) []*emailUser {
	query := accountColumns + "NULL" + accountTables + " WHERE " + condition + " ORDER BY e.id"
	if start != -1 || limit != -1 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, start)
	}
	return a.queryList(ctx, query, args)
}

func (a *Accounts) queryList(ctx context.Context, query string, args []any) []*emailUser {
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var users []*emailUser
	for rows.Next() {
		user, err := scanAccount(rows)
		if err != nil {
			return nil
		}
		users = append(users, user)
	}
	if rows.Err() != nil {
		return nil
	}
	return users
}

func (a *Accounts) count(active int) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		source := stringArgument(args, 0)
		if source == nil || *source != "DB" {
			return -1, nil
		}
		var count int64
		if err := a.db.QueryRowContext(ctx, "SELECT COUNT(id) FROM EmailUser WHERE is_active = ?", active).Scan(&count); err != nil {
			return -1, nil
		}
		return count, nil
	}
}
