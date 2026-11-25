package seafserver

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const selectGroups = `SELECT ` + groupColumns + ` FROM "Group" g`

const groupDescendants = `
FROM "Group" g JOIN GroupStructure s ON g.group_id = s.group_id
WHERE s.path LIKE (?1 || ', %') OR s.path LIKE ('%, ' || ?1 || ', %') OR g.group_id = ?1`

func (s *Identity) getAllGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = selectGroups + " ORDER BY g.timestamp DESC, g.group_id LIMIT ? OFFSET ?"
	groups, err := s.readGroups(ctx, query, true, intArgument(args, 1), intArgument(args, 0))
	return identityResult("get_all_groups", groups, err), nil
}

func (s *Identity) getAncestorGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = `
WITH structure AS (SELECT path FROM GroupStructure WHERE group_id = ?1)
SELECT g.group_id, g.group_name,
       CASE WHEN EXISTS (SELECT 1 FROM structure WHERE path IS NOT NULL)
            THEN g.creator_name ELSE lower(g.creator_name) END,
       COALESCE(g.timestamp, 0), COALESCE(g.parent_group_id, 0)
FROM "Group" g
WHERE g.group_id IN (
  SELECT value FROM structure, json_each('[' || structure.path || ']')
  UNION ALL SELECT ?1 WHERE NOT EXISTS (SELECT 1 FROM structure WHERE path IS NOT NULL)
)
ORDER BY g.group_id`
	groups, err := s.readGroups(ctx, query, false, intArgument(args, 0))
	return identityResult("get_ancestor_groups", groups, err), nil
}

func (s *Identity) getTopGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	join, scope := "", ""
	if intArgument(args, 0) == 0 {
		// The C SQLite implementation reads this relation from the group database.
		join = " LEFT JOIN OrgGroup o ON g.group_id = o.group_id"
		scope = " AND o.group_id IS NULL"
	}
	query := selectGroups + join + " WHERE g.parent_group_id = -1" + scope + " ORDER BY g.timestamp DESC, g.group_id"
	groups, err := s.readGroups(ctx, query, true)
	return identityResult("get_top_groups", groups, err), nil
}

func (s *Identity) getChildGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = selectGroups + " WHERE g.parent_group_id = ? ORDER BY g.group_id"
	groups, err := s.readGroups(ctx, query, false, intArgument(args, 0))
	return identityResult("get_child_groups", groups, err), nil
}

func (s *Identity) getDescendantsGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = "SELECT " + groupColumns + " " + groupDescendants + " ORDER BY g.group_id"
	groups, err := s.readGroups(ctx, query, false, intArgument(args, 0))
	return identityResult("get_descendants_groups", groups, err), nil
}

func (s *Identity) listAllDepartments(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	// The C row mapper reads type as the parent ID in this listing.
	const query = `SELECT g.group_id, g.group_name, g.creator_name, COALESCE(g.timestamp, 0), COALESCE(CAST(g.type AS INTEGER), 0)
FROM "Group" g WHERE g.parent_group_id = -1 OR g.parent_group_id > 0 ORDER BY g.group_id`
	groups, err := s.readGroups(ctx, query, true)
	return identityResult("list_all_departments", groups, err), nil
}

func (s *Identity) getGroupMembersWithPrefix(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = "WITH descendants AS (SELECT g.group_id " + groupDescendants + `)
SELECT group_id, user_name, COALESCE(is_staff, 0) FROM GroupUser
WHERE group_id IN (SELECT group_id FROM descendants UNION ALL SELECT ?1 WHERE NOT EXISTS (SELECT 1 FROM descendants))
  AND (?2 IS NULL OR user_name LIKE ?2)
ORDER BY group_id DESC, user_name DESC`
	prefix := stringArgument(args, 1)
	if prefix != nil {
		*prefix += "%"
	}
	members, err := s.readGroupMembers(ctx, query, intArgument(args, 0), prefix)
	return identityResult("get_members_with_prefix", members, err), nil
}

func (s *Identity) checkGroupStaff(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user := intArgument(args, 0), stringArgument(args, 1)
	if id <= 0 || user == nil {
		return -1, rpcError(errCCNetInternal, "Bad arguments")
	}
	const query = `
WITH structure AS (SELECT path FROM GroupStructure WHERE group_id = ?1 AND ?3 <> 0)
SELECT EXISTS (
  SELECT 1 FROM GroupUser
  WHERE user_name = ?2 AND is_staff = 1 AND group_id IN (
    SELECT value FROM structure, json_each('[' || structure.path || ']')
    UNION ALL SELECT ?1 WHERE NOT EXISTS (SELECT 1 FROM structure WHERE path IS NOT NULL)
  )
)`
	var staff int
	if err := s.groups.QueryRowContext(ctx, query, id, *user, intArgument(args, 2)).Scan(&staff); err != nil {
		log.Printf("check_group_staff: %v", err)
		return 0, nil
	}
	return staff, nil
}

func (s *Identity) searchGroups(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = selectGroups + " WHERE g.group_name LIKE ? ORDER BY g.group_id LIMIT ? OFFSET ?"
	groups, err := s.readGroups(ctx, query, true, groupSearchPattern(stringArgument(args, 0)), intArgument(args, 2), intArgument(args, 1))
	return identityResult("search_groups", groups, err), nil
}

func (s *Identity) searchGroupMembers(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	const query = `SELECT DISTINCT 0, user_name, 0 FROM GroupUser WHERE group_id = ? AND user_name LIKE ? ORDER BY user_name DESC`
	members, err := s.readGroupMembers(ctx, query, intArgument(args, 0), groupSearchPattern(stringArgument(args, 1)))
	return identityResult("search_group_members", members, err), nil
}

func groupSearchPattern(pattern *string) string {
	if pattern == nil {
		return "%(null)%"
	}
	return "%" + *pattern + "%"
}

func (s *Identity) getGroupsMembers(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	groupIDs := stringArgument(args, 0)
	if groupIDs == nil || *groupIDs == "" {
		return nil, rpcError(errCCNetInternal, "Bad arguments")
	}
	ids, valid := groupMemberIDs(*groupIDs)
	if !valid {
		return nil, rpcError(0, "Bad args.")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	const query = `
SELECT DISTINCT 0, user_name, 0 FROM GroupUser WHERE group_id IN (SELECT value FROM json_each(?))`
	encoded, _ := json.Marshal(ids) // A slice of integers always encodes as JSON.
	members, err := s.readGroupMembers(ctx, query, string(encoded))
	return identityResult("get_groups_members", members, err), nil
}

func groupMemberIDs(argument string) ([]int, bool) {
	// Jansson decodes arrays and objects. A JSON object yields an empty ID list.
	if strings.HasPrefix(strings.TrimSpace(argument), "{") {
		return nil, json.Valid([]byte(argument))
	}
	var values []json.RawMessage
	if err := json.Unmarshal([]byte(argument), &values); err != nil || values == nil {
		return nil, false
	}
	ids := make([]int, len(values))
	for i := range values {
		id := intArgument(values, i)
		if id <= 0 {
			return nil, false
		}
		ids[i] = id
	}
	return ids, true
}
