package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"path/filepath"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// Organizations manages organization membership in the existing ccnet stores.
type Organizations struct {
	identity                     *Identity
	orgs                         *sql.DB
	orgPath, groupPath, userPath string
}

// OpenOrganizations prepares the writable organization database.
func OpenOrganizations(ccnetDir string, identity *Identity) (*Organizations, error) {
	orgPath := filepath.Join(ccnetDir, "OrgMgr", "orgmgr.db")
	db, err := OpenSQLiteReadWrite(orgPath)
	if err != nil {
		return nil, err
	}
	return &Organizations{
		identity: identity, orgs: db, orgPath: orgPath,
		groupPath: filepath.Join(ccnetDir, "GroupMgr", "groupmgr.db"),
		userPath:  filepath.Join(ccnetDir, "PeerMgr", "usermgr.db"),
	}, nil
}

func (s *Organizations) Close() error {
	return s.orgs.Close()
}

// RPCHandlers exposes the new ccnet organization administration methods.
func (s *Organizations) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"create_org":             s.createOrg,
		"remove_org":             s.removeOrg,
		"get_all_orgs":           s.allOrgs,
		"count_orgs":             s.countOrgs,
		"get_org_by_url_prefix":  s.orgByPrefix,
		"get_orgs_by_user":       s.orgsByUser,
		"get_org_emailusers":     s.orgEmailUsers,
		"add_org_user":           s.addUser,
		"remove_org_user":        s.changeUser("DELETE FROM OrgUser WHERE org_id = ? AND email = ?"),
		"is_org_staff":           s.isStaff,
		"set_org_staff":          s.changeUser("UPDATE OrgUser SET is_staff = 1 WHERE org_id = ? AND email = ?"),
		"unset_org_staff":        s.changeUser("UPDATE OrgUser SET is_staff = 0 WHERE org_id = ? AND email = ?"),
		"set_org_name":           s.renameOrg,
		"add_org_group":          s.changeGroup("INSERT INTO OrgGroup (org_id, group_id) VALUES (?, ?)"),
		"remove_org_group":       s.changeGroup("DELETE FROM OrgGroup WHERE org_id = ? AND group_id = ?"),
		"is_org_group":           s.isOrgGroup,
		"get_org_id_by_group":    s.groupOrgID,
		"create_org_group":       s.createOrgGroup,
		"get_org_groups":         s.orgGroups,
		"get_org_top_groups":     s.orgTopGroups,
		"get_org_groups_by_user": s.orgGroupsByUser,
	}
}

func (s *Organizations) createOrg(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	name, prefix, creator := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if name == nil || prefix == nil || creator == nil {
		return badOrgArgs(-1)
	}
	var id int64
	_, err := withSQLiteWrite(ctx, s.orgs, func(conn *sql.Conn) (bool, error) {
		result, err := conn.ExecContext(ctx,
			"INSERT INTO Organization (org_name, url_prefix, creator, ctime) VALUES (?, ?, ?, ?)",
			*name, *prefix, *creator, time.Now().UnixMicro())
		if err != nil {
			return false, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return false, err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO OrgUser (org_id, email, is_staff) VALUES (?, ?, 1)", id, *creator)
		return err == nil, err
	})
	if err != nil {
		return orgWriteFailure("Failed to create organization", err)
	}
	return id, nil
}

func (s *Organizations) addUser(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, email := intArgument(args, 0), stringArgument(args, 1)
	if id < 0 || email == nil {
		return badOrgArgs(-1)
	}
	const query = "INSERT INTO OrgUser (org_id, email, is_staff) VALUES (?, ?, ?)"
	return s.mutate(ctx, query, id, *email, intArgument(args, 2))
}

func (s *Organizations) changeUser(query string) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, email := intArgument(args, 0), stringArgument(args, 1)
		if id < 0 || email == nil {
			return badOrgArgs(-1)
		}
		return s.mutate(ctx, query, id, *email)
	}
}

func (s *Organizations) renameOrg(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, name := intArgument(args, 0), stringArgument(args, 1)
	if id < 0 || name == nil {
		return badOrgArgs(-1)
	}
	return s.mutate(ctx, "UPDATE Organization SET org_name = ? WHERE org_id = ?", *name, id)
}

func (s *Organizations) changeGroup(query string) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		org, group := intArgument(args, 0), intArgument(args, 1)
		if org < 0 || group < 0 {
			return badOrgArgs(-1)
		}
		return s.mutate(ctx, query, org, group)
	}
}

func (s *Organizations) mutate(ctx context.Context, query string, args ...any) (any, *searpc.RPCError) {
	if _, err := s.orgs.ExecContext(ctx, query, args...); err != nil {
		log.Printf("SQLite organization write: %v", err)
		return -1, nil
	}
	return 0, nil
}

func badOrgArgs(value any) (any, *searpc.RPCError) {
	return value, rpcError(errCCNetInternal, "Bad arguments")
}

func orgWriteFailure(message string, err error) (any, *searpc.RPCError) {
	log.Printf("SQLite organization write: %v", err)
	return -1, rpcError(0, message)
}
