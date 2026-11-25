package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const groupReposByIDs = `sh.group_id IN (SELECT value FROM json_each(?))
ORDER BY sh.group_id, sh.repo_id`

func (r *Reader) orgSharedUsersByRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	orgID, repoID := intArgument(args, 0), stringArgument(args, 1)
	if orgID < 0 || repoID == nil {
		return nil, rpcError(errBadArgs, "Arguments error")
	}
	const query = `SELECT repo_id, to_email, permission FROM OrgSharedRepo
WHERE org_id = ? AND repo_id = ? ORDER BY rowid`
	return readShareRows(ctx, r.db, "", query, scanSharedUser, orgID, *repoID)
}

func groupInfoIDs(groups []groupInfo) []int {
	ids := make([]int, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

func encodeGroupIDs(ids []int) string {
	encoded, _ := json.Marshal(ids) // A slice of integers always encodes as JSON.
	return string(encoded)
}

func (s *Identity) groupsInOrg(ctx context.Context, orgID int, groups []int) ([]int, error) {
	const query = `SELECT group_id FROM OrgGroup WHERE org_id = ?
AND group_id IN (SELECT value FROM json_each(?))`
	rows, err := s.orgs.QueryContext(ctx, query, orgID, encodeGroupIDs(groups))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []int
	for rows.Next() {
		var groupID int
		if err := rows.Scan(&groupID); err != nil {
			return nil, err
		}
		result = append(result, groupID)
	}
	return result, rows.Err()
}

func (r *Reader) repoOrg(ctx context.Context, id string) (int, error) {
	const query = `SELECT org_id FROM OrgRepo
WHERE repo_id = COALESCE((SELECT origin_repo FROM VirtualRepo WHERE repo_id = ?1), ?1)`
	var orgID int
	err := r.db.QueryRowContext(ctx, query, id).Scan(&orgID)
	return orgID, err
}

func (r *Reader) sharedRepoAt(ctx context.Context, id string, path *string) (string, error) {
	if path == nil {
		return id, nil
	}
	const query = `SELECT repo_id FROM VirtualRepo WHERE origin_repo = ? AND path = ? ORDER BY rowid LIMIT 1`
	var virtualID string
	err := r.db.QueryRowContext(ctx, query, id, *path).Scan(&virtualID)
	return virtualID, err
}

func (r *Reader) personalSharedRepoByPath(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user := stringArgument(args, 0), stringArgument(args, 2)
	if id == nil || user == nil {
		return nil, rpcError(errBadArgs, "Arguments error")
	}
	resolved, err := r.sharedRepoAt(ctx, *id, stringArgument(args, 1))
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("get_shared_repo_by_path: %v", err)
		}
		return nil, nil
	}
	from := " FROM SharedRepo sh " + shareRepoJoins + shareOriginJoin
	if intArgument(args, 3) != 0 {
		from = " FROM OrgSharedRepo sh " + shareRepoJoins + shareOriginJoin + orgShareRepoJoin
	}
	const filter = " WHERE sh.repo_id = ? AND sh.to_email = ? ORDER BY sh.rowid DESC LIMIT 1"
	repos, rpcErr := readShareRows(ctx, r.db, "", personalRepoSelect+from+filter, scanSharedRepo, resolved, *user)
	if len(repos) == 0 {
		return nil, rpcErr
	}
	return r.sharedRepoObject(repos[0], "personal", nil, false), nil
}

func (r *Reader) groupSharedRepoByPath(identity *Identity) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, groupID := stringArgument(args, 0), intArgument(args, 2)
		if id == nil || groupID < 0 {
			return nil, rpcError(errBadArgs, "Arguments error")
		}
		resolved, err := r.sharedRepoAt(ctx, *id, stringArgument(args, 1))
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				log.Printf("get_group_shared_repo_by_path: %v", err)
			}
			return nil, nil
		}

		query := groupRepoSelect + " WHERE sh.repo_id = ? AND sh.group_id = ?"
		params := []any{resolved, groupID}
		if intArgument(args, 3) != 0 {
			orgID, err := r.repoOrg(ctx, resolved)
			if err != nil {
				if !errors.Is(err, sql.ErrNoRows) {
					log.Printf("get_group_shared_repo_by_path: %v", err)
					return nil, rpcError(errShareRead, "Failed to get repo by group_id from db.")
				}
				return nil, nil
			}
			groups, err := identity.groupsInOrg(ctx, orgID, []int{groupID})
			if err != nil {
				return nil, shareReadError("Failed to get repo by group_id from db.", err)
			}
			if len(groups) == 0 {
				return nil, nil
			}
			query = orgGroupRepoSelect + " WHERE sh.repo_id = ? AND sh.group_id = ? AND sh.org_id = ?"
			params = append(params, orgID)
		}
		repos, rpcErr := readShareRows(ctx, r.db, "Failed to get repo by group_id from db.",
			query+" ORDER BY sh.rowid DESC LIMIT 1", scanSharedRepo, params...)
		if len(repos) == 0 {
			return nil, rpcErr
		}
		return r.sharedRepoObject(repos[0], "group", nil, true), nil
	}
}
