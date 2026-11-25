package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const errShareRead = 500

type sharedUser struct {
	RepoID string  `json:"repo-id"`
	User   *string `json:"user"`
	Perm   *string `json:"perm"`
}

type sharedGroup struct {
	RepoID  string  `json:"repo-id"`
	GroupID int32   `json:"group-id"`
	Perm    *string `json:"perm"`
}

func (r *Reader) listShareRepos(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	email, direction := stringArgument(args, 0), stringArgument(args, 1)
	if direction == nil || (*direction != "from_email" && *direction != "to_email") {
		return nil, rpcError(errBadArgs, "Wrong type argument")
	}
	from, to := "from_email", "to_email"
	if *direction == "to_email" {
		from, to = to, from
	}
	// Apply pagination to stored metadata before reading fallback commits.
	// C writes back fallback timestamps, which can change its subsequent page order.
	query := "SELECT " + shareRepoColumns + ", sh." + to + ", 0, origin.name" +
		" FROM SharedRepo sh " + shareRepoJoins + shareOriginJoin +
		" WHERE sh." + from + " = ? ORDER BY i.update_time DESC, sh.repo_id, sh.rowid LIMIT ? OFFSET ?"
	return r.readSharedRepos(ctx, "personal", query, nil, "", email, intArgument(args, 3), intArgument(args, 2))
}

func shareOwnerAndRepo(args []json.RawMessage) (owner, repoID string, err *searpc.RPCError) {
	from, id := stringArgument(args, 0), stringArgument(args, 1)
	if from == nil || id == nil {
		return "", "", rpcError(errBadArgs, "Missing args")
	}
	if !validRepoID(*id) {
		return "", "", rpcError(errBadArgs, "Invalid repo id")
	}
	return *from, *id, nil
}

func (r *Reader) listRepoSharedTo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	owner, id, rpcErr := shareOwnerAndRepo(args)
	if rpcErr != nil {
		return nil, rpcErr
	}
	const query = `SELECT repo_id, to_email, permission FROM SharedRepo
WHERE from_email = ? AND repo_id = ? ORDER BY rowid DESC`
	return readShareRows(ctx, r.db, "Failed to list repo shared to from db", query, func(row *sql.Rows) (sharedUser, error) {
		user, err := scanSharedUser(row)
		user.User = lowerNullable(user.User)
		return user, err
	}, owner, id)
}

func (r *Reader) sharedUsersByRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return nil, rpcError(errBadArgs, "Arguments error")
	}
	return readShareRows(ctx, r.db, "", `
SELECT repo_id, to_email, permission FROM SharedRepo WHERE repo_id = ? ORDER BY rowid`, scanSharedUser, *id)
}

func scanSharedUser(row *sql.Rows) (sharedUser, error) {
	var user sharedUser
	err := row.Scan(&user.RepoID, &user.User, &user.Perm)
	return user, err
}

func (r *Reader) listRepoSharedGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	owner, id, rpcErr := shareOwnerAndRepo(args)
	if rpcErr != nil {
		return nil, rpcErr
	}
	const query = `SELECT repo_id, CAST(COALESCE(group_id, 0) AS INTEGER), permission FROM RepoGroup
WHERE user_name = ? AND repo_id = ? ORDER BY rowid DESC`
	return readShareRows(ctx, r.db, "Failed to list repo shared group from db", query, func(row *sql.Rows) (sharedGroup, error) {
		var group sharedGroup
		var groupID int64
		err := row.Scan(&group.RepoID, &groupID, &group.Perm)
		group.GroupID = int32(groupID)
		return group, err
	}, owner, id)
}

func (r *Reader) sharedGroupsByRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	if !validRepoID(*id) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	const query = `SELECT CAST(COALESCE(group_id, 0) AS INTEGER) FROM RepoGroup WHERE repo_id = ? ORDER BY rowid`
	groups, rpcErr := readShareRows(ctx, r.db, "", query, func(row *sql.Rows) (string, error) {
		var groupID int64
		err := row.Scan(&groupID)
		return strconv.FormatInt(int64(int32(groupID)), 10), err
	}, *id)
	return shareIDList(groups), rpcErr
}

func (r *Reader) groupRepoIDs(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = `SELECT repo_id FROM RepoGroup WHERE group_id = ? ORDER BY repo_id`
	repos, rpcErr := readShareRows(ctx, r.db, "", query, func(row *sql.Rows) (string, error) {
		var id string
		err := row.Scan(&id)
		return id, err
	}, intArgument(args, 0))
	return shareIDList(repos), rpcErr
}

func shareIDList(ids []string) any {
	if len(ids) == 0 {
		return nil
	}
	return strings.Join(ids, "\n") + "\n"
}

func (r *Reader) groupRepoOwner(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil || !validRepoID(*id) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	const query = `SELECT COALESCE(user_name, '') FROM RepoGroup WHERE repo_id = ? ORDER BY rowid LIMIT 1`
	var owner string
	if err := r.db.QueryRowContext(ctx, query, *id).Scan(&owner); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", shareReadError("", err)
	}
	return asciiLower(owner), nil
}

func (r *Reader) listReposByGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := intArgument(args, 0)
	if id < 0 {
		return nil, rpcError(errBadArgs, "Invalid group id.")
	}
	const query = groupRepoSelect + " WHERE sh.group_id = ? ORDER BY sh.repo_id"
	return r.readSharedRepos(ctx, "group", query, nil, "Failed to get repos by group from db.", id)
}

func (r *Reader) listGroupReposByOwner(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	owner := stringArgument(args, 0)
	if owner == nil {
		return nil, rpcError(errBadArgs, "user name can not be NULL")
	}
	// The C wrapper reverses the manager's result in SQLite row order.
	const query = groupRepoSelect + " WHERE sh.user_name = ? ORDER BY sh.rowid DESC"
	return r.readSharedRepos(ctx, "group", query, nil, "", *owner)
}

func (r *Reader) listGroupReposByUser(identity *Identity, organizational bool) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		user := stringArgument(args, 0)
		if user == nil {
			return nil, rpcError(errBadArgs, "Arguments error")
		}
		groups, err := identity.groupsByUser(ctx, *user, true)
		if err != nil {
			return nil, shareReadError("", err)
		}
		if len(groups) == 0 {
			return nil, nil
		}
		names := make(map[int]*string, len(groups))
		for _, group := range groups {
			names[group.ID] = group.Name
		}
		ids := groupInfoIDs(groups)
		query := groupRepoSelect + " WHERE " + groupReposByIDs
		params := []any{encodeGroupIDs(ids)}
		orgID := intArgument(args, 1)
		if organizational && orgID >= 0 {
			ids, err = identity.groupsInOrg(ctx, orgID, ids)
			if err != nil {
				return nil, shareReadError("", err)
			}
			if len(ids) == 0 {
				return nil, nil
			}
			query = orgGroupRepoSelect + " WHERE sh.org_id = ? AND " + groupReposByIDs
			params = []any{orgID, encodeGroupIDs(ids)}
		}
		return r.readSharedRepos(ctx, "group", query, names, "Failed to get user group repos from db.", params...)
	}
}

func (r *Reader) repoHasBeenShared(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return 0, rpcError(errBadArgs, "Arguments error")
	}
	const query = `SELECT EXISTS (SELECT 1 FROM SharedRepo WHERE repo_id = ?1)
   OR (?2 <> 0 AND EXISTS (SELECT 1 FROM RepoGroup WHERE repo_id = ?1))`
	var exists int
	if err := r.db.QueryRowContext(ctx, query, *id, intArgument(args, 1)).Scan(&exists); err != nil {
		return 0, shareReadError("", err)
	}
	return exists, nil
}

func (r *Reader) listInnerPublicRepos(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	// The SQLite C query walks RepoOwner and looks up public shares for each row.
	const query = publicRepoSelect + " ORDER BY owner.rowid"
	return r.readSharedRepos(ctx, "public", query, nil, "")
}

func (r *Reader) listInnerPublicReposByOwner(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	owner := stringArgument(args, 0)
	if owner == nil {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	const query = publicRepoSelect + " WHERE owner.owner_id = ? ORDER BY owner.rowid"
	return r.readSharedRepos(ctx, "public", query, nil, "", *owner)
}

func (r *Reader) countInnerPublicRepos(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	var count int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM InnerPubRepo").Scan(&count); err != nil {
		return int64(-1), shareReadError("", err)
	}
	return count, nil
}

func (r *Reader) isInnerPublicRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil {
		return -1, rpcError(errBadArgs, "Bad arguments")
	}
	if !validRepoID(*id) {
		return -1, rpcError(errBadArgs, "Invalid repo id")
	}
	var exists int
	if err := r.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM InnerPubRepo WHERE repo_id = ?)", *id).Scan(&exists); err != nil {
		return 0, shareReadError("", err)
	}
	return exists, nil
}

func lowerNullable(value *string) *string {
	if value == nil {
		return nil
	}
	lower := asciiLower(*value)
	return &lower
}

func shareReadError(message string, err error) *searpc.RPCError {
	log.Printf("SQLite share read: %v", err)
	if message != "" {
		return rpcError(errShareRead, message)
	}
	return nil
}

func readShareRows[T any](ctx context.Context, db *sql.DB, message, query string, scan func(*sql.Rows) (T, error), args ...any) ([]T, *searpc.RPCError) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, shareReadError(message, err)
	}
	defer rows.Close()
	var result []T
	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return nil, shareReadError(message, err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, shareReadError(message, err)
	}
	return result, nil
}
