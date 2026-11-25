package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const orgGroupSchema = "org_groups"

func (s *Organizations) createOrgGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	org, parent := intArgument(args, 0), intArgument(args, 3)
	name, user := stringArgument(args, 1), stringArgument(args, 2)
	if org < 0 || name == nil || user == nil {
		return -1, rpcError(errCCNetInternal, "Bad args")
	}
	const failure = "Failed to create org group."
	message := failure
	var id int64
	err := s.writeWithGroupDB(ctx, false, func(conn *sql.Conn) error {
		if parent > 0 {
			var sameOrg bool
			const query = "SELECT EXISTS (SELECT 1 FROM main.OrgGroup WHERE org_id = ? AND group_id = ?)"
			if err := conn.QueryRowContext(ctx, query, org, parent).Scan(&sameOrg); err != nil {
				return err
			}
			if !sameOrg {
				return errors.New("parent group is outside the organization")
			}
		}
		var err error
		id, err = insertGroup(ctx, conn, orgGroupSchema, *name, *user, parent)
		if err != nil {
			message = "Failed to create group"
			return err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO main.OrgGroup (org_id, group_id) VALUES (?, ?)", org, id)
		return err
	})
	if err != nil {
		return orgWriteFailure(message, err)
	}
	return id, nil
}

func (s *Organizations) removeOrg(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := intArgument(args, 0)
	if id < 0 {
		return badOrgArgs(-1)
	}
	err := s.writeWithGroupDB(ctx, true, func(conn *sql.Conn) error {
		const exclusiveUsers = `SELECT email FROM main.OrgUser member WHERE org_id = ?1 AND NOT EXISTS (
			SELECT 1 FROM main.OrgUser shared WHERE shared.org_id <> ?1 AND shared.email = member.email COLLATE NOCASE
		)`
		const exclusiveGroups = `SELECT group_id FROM main.OrgGroup membership WHERE org_id = ?1 AND NOT EXISTS (
			SELECT 1 FROM main.OrgGroup shared WHERE shared.org_id <> ?1 AND shared.group_id = membership.group_id
		)`
		const hasExternalChildren = `SELECT EXISTS (SELECT 1 FROM org_groups."Group"
			WHERE parent_group_id IN (` + exclusiveGroups + `) AND group_id NOT IN (` + exclusiveGroups + `))`
		var hasChildren bool
		if err := conn.QueryRowContext(ctx, hasExternalChildren, id).Scan(&hasChildren); err != nil {
			return err
		}
		if hasChildren {
			return errors.New("organization group has children outside the deletion")
		}
		for _, query := range []string{
			`DELETE FROM org_users.UserRole WHERE email IN (` + exclusiveUsers + `)`,
			`DELETE FROM org_users.EmailUser WHERE email IN (` + exclusiveUsers + `)`,
			`DELETE FROM org_groups.GroupUser WHERE group_id IN (` + exclusiveGroups + `)`,
			`DELETE FROM org_groups.GroupStructure WHERE group_id IN (` + exclusiveGroups + `)`,
			`DELETE FROM org_groups."Group" WHERE group_id IN (` + exclusiveGroups + `)`,
			"DELETE FROM main.OrgGroup WHERE org_id = ?",
			"DELETE FROM main.OrgUser WHERE org_id = ?",
			"DELETE FROM main.Organization WHERE org_id = ?",
		} {
			if _, err := conn.ExecContext(ctx, query, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return orgWriteFailure("Failed to remove organization", err)
	}
	return 0, nil
}

func (s *Organizations) writeWithGroupDB(ctx context.Context, attachUsers bool, write func(*sql.Conn) error) error {
	// Closing this short-lived pool also closes the attached schemas.
	db, err := OpenSQLiteReadWrite(s.orgPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		if attachUsers {
			if err := attachOrgDB(ctx, conn, "org_users", s.userPath, "rw"); err != nil {
				return false, err
			}
		}
		if err := attachOrgDB(ctx, conn, orgGroupSchema, s.groupPath, "rw"); err != nil {
			return false, err
		}
		err := write(conn)
		return err == nil, err
	})
	return err
}

func attachOrgDB(ctx context.Context, conn *sql.Conn, schema, path, mode string) error {
	uri, err := sqliteFileURL(path)
	if err != nil {
		return err
	}
	uri.RawQuery = "mode=" + mode
	// Both schema aliases come from static call sites.
	_, err = conn.ExecContext(ctx, "ATTACH DATABASE ? AS "+schema, uri.String())
	return err
}

func (s *Organizations) isOrgGroup(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	group := intArgument(args, 0)
	if group <= 0 {
		return badOrgArgs(-1)
	}
	var exists int
	_ = s.identity.orgs.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM OrgGroup WHERE group_id = ?)", group).Scan(&exists)
	return exists, nil
}

func (s *Organizations) groupOrgID(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	group := intArgument(args, 0)
	if group <= 0 {
		return badOrgArgs(-1)
	}
	var org int
	if err := s.identity.orgs.QueryRowContext(ctx, "SELECT org_id FROM OrgGroup WHERE group_id = ? ORDER BY rowid LIMIT 1", group).Scan(&org); err != nil {
		return -1, nil
	}
	return org, nil
}

const orgGroupColumns = "SELECT " + groupColumns + ` FROM OrgGroup o JOIN org_groups."Group" g ON g.group_id = o.group_id`

func (s *Organizations) orgGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	org, start, limit := intArgument(args, 0), intArgument(args, 1), intArgument(args, 2)
	if org < 0 {
		return badOrgArgs(nil)
	}
	if limit == -1 {
		start = 0
	}
	const query = orgGroupColumns + " WHERE o.org_id = ? ORDER BY g.group_id LIMIT ? OFFSET ?"
	return s.orgGroupRows(ctx, "get_org_groups", query, org, limit, start)
}

func (s *Organizations) orgTopGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	org := intArgument(args, 0)
	if org < 0 {
		return badOrgArgs(nil)
	}
	const query = orgGroupColumns + " WHERE o.org_id = ? AND g.parent_group_id = -1 ORDER BY g.timestamp DESC, g.group_id"
	return s.orgGroupRows(ctx, "get_org_top_groups", query, org)
}

func (s *Organizations) orgGroupsByUser(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	user, org := stringArgument(args, 0), intArgument(args, 1)
	if org < 0 || user == nil {
		return badOrgArgs(nil)
	}
	const query = orgGroupColumns + ` WHERE o.org_id = ? AND EXISTS (
		SELECT 1 FROM org_groups.GroupUser WHERE group_id = g.group_id AND user_name = ?
	) ORDER BY g.group_id`
	return s.orgGroupRows(ctx, "get_org_groups_by_user", query, org, *user)
}

func (s *Organizations) orgGroupRows(ctx context.Context, method, query string, args ...any) (any, *searpc.RPCError) {
	db, err := OpenSQLiteReadOnly(ctx, s.orgPath)
	if err != nil {
		return identityResult(method, nil, err), nil
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return identityResult(method, nil, err), nil
	}
	defer conn.Close()
	if err := attachOrgDB(ctx, conn, orgGroupSchema, s.groupPath, "ro"); err != nil {
		return identityResult(method, nil, err), nil
	}
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return identityResult(method, nil, err), nil
	}
	defer rows.Close()
	var groups []groupInfo
	for rows.Next() {
		var group groupInfo
		if err := group.scan(rows); err != nil {
			return identityResult(method, nil, err), nil
		}
		groups = append(groups, group)
	}
	return identityResult(method, groups, rows.Err()), nil
}
