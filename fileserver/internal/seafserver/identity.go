package seafserver

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
)

// Identity reads the SQLite databases under the C server's ccnet directory.
type Identity struct {
	users  *sql.DB
	groups *sql.DB
	orgs   *sql.DB
}

// OpenIdentityReadOnly opens the user, group, and organization databases.
func OpenIdentityReadOnly(ctx context.Context, configDir string) (*Identity, error) {
	store := &Identity{}
	for _, database := range []struct {
		dir, name string
		handle    **sql.DB
	}{
		{"PeerMgr", "usermgr.db", &store.users},
		{"GroupMgr", "groupmgr.db", &store.groups},
		{"OrgMgr", "orgmgr.db", &store.orgs},
	} {
		db, err := OpenSQLiteReadOnly(ctx, filepath.Join(configDir, database.dir, database.name))
		if err != nil {
			return nil, errors.Join(err, store.Close())
		}
		*database.handle = db
	}
	return store, nil
}

// Close releases the identity database handles.
func (s *Identity) Close() error {
	var errorsOnClose []error
	for _, db := range []*sql.DB{s.users, s.groups, s.orgs} {
		if db != nil {
			errorsOnClose = append(errorsOnClose, db.Close())
		}
	}
	return errors.Join(errorsOnClose...)
}

type emailUser struct {
	ID          int     `json:"id"`
	Email       string  `json:"email"`
	Staff       bool    `json:"is-staff"`
	Active      bool    `json:"is-active"`
	CreatedAt   int64   `json:"ctime"`
	Source      string  `json:"source"`
	Role        string  `json:"role"`
	Password    *string `json:"password"`
	ReferenceID *string `json:"reference-id"`
}

func (s *Identity) emailUser(ctx context.Context, email string) (*emailUser, error) {
	return queryEmailUser(ctx, s.users, email)
}

const accountColumns = `SELECT e.id, e.email, COALESCE(e.is_staff, 0), COALESCE(e.is_active, 0),
	COALESCE(e.ctime, 0), COALESCE(r.role, ''), e.passwd, `
const accountTables = " FROM EmailUser e LEFT JOIN UserRole r ON e.email = r.email"

func queryEmailUser(ctx context.Context, db *sql.DB, email string) (*emailUser, error) {
	return scanAccount(db.QueryRowContext(ctx, accountColumns+"e.reference_id"+accountTables+
		" WHERE e.email IN (?, ?) ORDER BY e.email = ? DESC LIMIT 1", email, asciiLower(email), email))
}

func scanAccount(row interface{ Scan(...any) error }) (*emailUser, error) {
	user := &emailUser{Source: "DB"}
	var staff, active int
	if err := row.Scan(&user.ID, &user.Email, &staff, &active, &user.CreatedAt,
		&user.Role, &user.Password, &user.ReferenceID); err != nil {
		return nil, err
	}
	user.Email = asciiLower(user.Email)
	user.Staff, user.Active = staff != 0, active != 0
	return user, nil
}

type groupInfo struct {
	ID       int     `json:"id"`
	Name     *string `json:"group-name"`
	Creator  *string `json:"creator-name"`
	Time     int64   `json:"timestamp"`
	Source   string  `json:"source"`
	ParentID int     `json:"parent-group-id"`
}

const groupColumns = "g.group_id, g.group_name, g.creator_name, COALESCE(g.timestamp, 0), COALESCE(g.parent_group_id, 0)"

func (g *groupInfo) scan(row interface{ Scan(...any) error }) error {
	g.Source = "DB"
	return row.Scan(&g.ID, &g.Name, &g.Creator, &g.Time, &g.ParentID)
}

func (s *Identity) group(ctx context.Context, groupID int) (*groupInfo, error) {
	group := &groupInfo{}
	row := s.groups.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM "Group" g WHERE g.group_id = ?`, groupID)
	if err := group.scan(row); err != nil {
		return nil, err
	}
	if group.Creator != nil {
		creator := asciiLower(*group.Creator)
		group.Creator = &creator
	}
	return group, nil
}

// GroupStructure.path contains a comma-separated list of group IDs including
// the direct group. Standalone groups have no path. The C lookup returns no
// groups if a user's structured memberships have no usable path rows.
const ancestorGroups = `
WITH direct AS (
    SELECT g.group_id, g.parent_group_id
      FROM GroupUser u JOIN "Group" g ON g.group_id = u.group_id
     WHERE u.user_name = ?
), paths AS (
    SELECT part.value AS group_id
      FROM direct d JOIN GroupStructure s ON s.group_id = d.group_id
      JOIN json_each('[' || s.path || ']') part
     WHERE d.parent_group_id <> 0
), ids AS (
    SELECT group_id FROM direct
     WHERE COALESCE(parent_group_id, 0) = 0
       AND (EXISTS (SELECT 1 FROM paths)
         OR NOT EXISTS (SELECT 1 FROM direct WHERE parent_group_id <> 0))
    UNION ALL SELECT DISTINCT group_id FROM paths
)
SELECT ` + groupColumns + `
  FROM ids JOIN "Group" g ON g.group_id = ids.group_id
 ORDER BY g.group_id DESC`

func (s *Identity) groupsByUser(ctx context.Context, user string, includeAncestors bool) ([]groupInfo, error) {
	query := `SELECT ` + groupColumns + `
  FROM GroupUser u JOIN "Group" g ON g.group_id = u.group_id
 WHERE u.user_name = ?
 ORDER BY g.group_id DESC`
	if includeAncestors {
		query = ancestorGroups
	}
	return s.readGroups(ctx, query, false, user)
}

func (s *Identity) readGroups(ctx context.Context, query string, lowerCreator bool, args ...any) ([]groupInfo, error) {
	rows, err := s.groups.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []groupInfo
	for rows.Next() {
		var group groupInfo
		if err := group.scan(rows); err != nil {
			return nil, err
		}
		if lowerCreator {
			group.Creator = lowerNullable(group.Creator)
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

type groupMember struct {
	GroupID int     `json:"group-id"`
	User    *string `json:"user-name"`
	Staff   int     `json:"is-staff"`
}

func (s *Identity) groupMembers(ctx context.Context, groupID, start, limit int) ([]groupMember, error) {
	// Match the C query so SQLite selects the same members before reversing the page.
	query := `SELECT group_id, user_name, COALESCE(is_staff, 0) FROM GroupUser
          WHERE group_id = ?`
	params := []any{groupID}
	if limit != -1 {
		query += " LIMIT ? OFFSET ?"
		params = append(params, limit, max(start, 0))
	}
	members, err := s.readGroupMembers(ctx, query, params...)
	// The C RPC reverses the rows after SQLite selects the page.
	slices.Reverse(members)
	return members, err
}

func (s *Identity) readGroupMembers(ctx context.Context, query string, args ...any) ([]groupMember, error) {
	rows, err := s.groups.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []groupMember
	for rows.Next() {
		var member groupMember
		if err := rows.Scan(&member.GroupID, &member.User, &member.Staff); err != nil {
			return nil, err
		}
		member.User = lowerNullable(member.User)
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *Identity) isGroupUser(ctx context.Context, groupID int, user string, inStructure bool) (int, error) {
	var match int
	err := s.groups.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM GroupUser WHERE group_id = ? AND user_name = ?)`,
		groupID, user).Scan(&match)
	if err != nil || match == 1 || !inStructure {
		return match, err
	}
	groups, err := s.groupsByUser(ctx, user, true)
	if err != nil {
		return 0, err
	}
	for _, group := range groups {
		if group.ID == groupID {
			return 1, nil
		}
	}
	return 0, nil
}

type organization struct {
	ID        int     `json:"org-id"`
	Email     *string `json:"email"`
	Staff     int     `json:"is-staff"`
	Name      *string `json:"org-name"`
	URLPrefix *string `json:"url-prefix"`
	Creator   *string `json:"creator"`
	CreatedAt int64   `json:"ctime"`
}

func (s *Identity) organization(ctx context.Context, orgID int) (*organization, error) {
	const query = "SELECT " + orgFields + ", NULL, 0 FROM Organization o WHERE o.org_id = ?"
	return scanOrganization(s.orgs.QueryRowContext(ctx, query, orgID))
}

func (s *Identity) orgUserExists(ctx context.Context, orgID int, email string) (int, error) {
	var match int
	err := s.orgs.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM OrgUser WHERE org_id = ? AND email = ?)`, orgID, email).Scan(&match)
	return match, err
}
