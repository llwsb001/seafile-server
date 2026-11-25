package seafserver

import (
	"context"
	"encoding/json"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const orgFields = "o.org_id, o.org_name, o.url_prefix, o.creator, COALESCE(o.ctime, 0)"

func scanOrganization(row interface{ Scan(...any) error }) (*organization, error) {
	var org organization
	if err := row.Scan(&org.ID, &org.Name, &org.URLPrefix, &org.Creator, &org.CreatedAt, &org.Email, &org.Staff); err != nil {
		return nil, err
	}
	return &org, nil
}

func (s *Organizations) orgByPrefix(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	prefix := stringArgument(args, 0)
	if prefix == nil {
		return badOrgArgs(nil)
	}
	const query = "SELECT " + orgFields + ", NULL, 0 FROM Organization o WHERE o.url_prefix = ?"
	org, err := scanOrganization(s.identity.orgs.QueryRowContext(ctx, query, *prefix))
	return identityResult("get_org_by_url_prefix", org, err), nil
}

func (s *Organizations) allOrgs(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = "SELECT " + orgFields + ", NULL, 0 FROM Organization o ORDER BY o.org_id LIMIT ? OFFSET ?"
	return s.orgRows(ctx, "get_all_orgs", query, intArgument(args, 1), intArgument(args, 0))
}

func (s *Organizations) countOrgs(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	var count int64
	if err := s.identity.orgs.QueryRowContext(ctx, "SELECT COUNT(*) FROM Organization").Scan(&count); err != nil {
		log.Printf("count_orgs: %v", err)
		return -1, nil
	}
	return count, nil
}

func (s *Organizations) orgsByUser(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = "SELECT " + orgFields + `, member.email, member.is_staff FROM OrgUser member
		JOIN Organization o ON o.org_id = member.org_id WHERE member.email = ? ORDER BY member.rowid`
	return s.orgRows(ctx, "get_orgs_by_user", query, stringArgument(args, 0))
}

func (s *Organizations) orgRows(ctx context.Context, method, query string, args ...any) (any, *searpc.RPCError) {
	rows, err := s.identity.orgs.QueryContext(ctx, query, args...)
	if err != nil {
		return identityResult(method, nil, err), nil
	}
	defer rows.Close()
	var orgs []*organization
	for rows.Next() {
		org, err := scanOrganization(rows)
		if err != nil {
			return identityResult(method, nil, err), nil
		}
		orgs = append(orgs, org)
	}
	return identityResult(method, orgs, rows.Err()), nil
}

func (s *Organizations) orgEmailUsers(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	prefix := stringArgument(args, 0)
	if prefix == nil {
		return badOrgArgs(nil)
	}
	const query = `SELECT member.email FROM OrgUser member JOIN Organization o ON o.org_id = member.org_id
		WHERE o.url_prefix = ? ORDER BY member.email LIMIT ? OFFSET ?`
	rows, err := s.identity.orgs.QueryContext(ctx, query, *prefix, intArgument(args, 2), intArgument(args, 1))
	if err != nil {
		return identityResult("get_org_emailusers", nil, err), nil
	}
	defer rows.Close()
	var users []*emailUser
	for rows.Next() {
		var email *string
		if err := rows.Scan(&email); err != nil {
			return identityResult("get_org_emailusers", nil, err), nil
		}
		if email != nil {
			if user, err := s.identity.emailUser(ctx, *email); err == nil {
				users = append(users, user)
			}
		}
	}
	return identityResult("get_org_emailusers", users, rows.Err()), nil
}

func (s *Organizations) isStaff(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, email := intArgument(args, 0), stringArgument(args, 1)
	if id < 0 || email == nil {
		return badOrgArgs(-1)
	}
	var staff int
	if err := s.identity.orgs.QueryRowContext(ctx,
		"SELECT is_staff FROM OrgUser WHERE org_id = ? AND email = ?", id, *email).Scan(&staff); err != nil {
		return -1, nil
	}
	return staff, nil
}
