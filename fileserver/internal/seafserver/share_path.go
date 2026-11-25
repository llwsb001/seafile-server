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

func (r *Reader) convertSharedRepoPath(identity *Identity) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, path, user := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
		if id == nil || !validRepoID(*id) || path == nil || user == nil {
			return nil, rpcError(errBadArgs, "Arguments error")
		}
		converted, err := r.lookupSharedRepoPath(ctx, identity, *id, formatPath(*path), *user, intArgument(args, 3) != 0)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			log.Printf("convert_repo_path: %v", err)
		}
		return converted, nil
	}
}

func (r *Reader) lookupSharedRepoPath(ctx context.Context, identity *Identity, repoID, path, user string, isOrg bool) (*string, error) {
	origin, subpath := repoID, ""
	const lookup = "SELECT origin_repo, path FROM VirtualRepo WHERE repo_id = ?"
	err := r.db.QueryRowContext(ctx, lookup, repoID).Scan(&origin, &subpath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	path = subpath + path

	personalTable, groupTable, orgScope := "SharedRepo", "RepoGroup", ""
	params := []any{origin, path, user}
	var orgID int
	if isOrg {
		orgID, err = r.repoOrg(ctx, origin)
		if err != nil {
			return nil, err
		}
		personalTable, groupTable = "OrgSharedRepo", "OrgGroupRepo"
		orgScope = " AND sh.org_id = ?4"
		params = append(params, orgID)
	}
	const pathFilter = `
WHERE v.origin_repo = ?1 AND
      (?2 = v.path OR substr(?2, 1, length(v.path) + 1) = v.path || '/') AND `
	const closest = " ORDER BY length(v.path) DESC, sh.rowid LIMIT 1"
	query := "SELECT v.repo_id, v.path, 0 FROM VirtualRepo v JOIN " + personalTable + " sh ON sh.repo_id = v.repo_id " +
		pathFilter + "sh.to_email = ?3" + orgScope + closest
	result, err := r.closestSharedRepoPath(ctx, query, path, params)
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}

	// C gives a matching personal share precedence over every group share.
	groups, err := identity.groupsByUser(ctx, user, true)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	ids := groupInfoIDs(groups)
	if isOrg {
		ids, err = identity.groupsInOrg(ctx, orgID, ids)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
	}
	params[2] = encodeGroupIDs(ids)
	query = "SELECT v.repo_id, v.path, sh.group_id FROM VirtualRepo v JOIN " + groupTable + " sh ON sh.repo_id = v.repo_id " +
		pathFilter + "sh.group_id IN (SELECT value FROM json_each(?3))" + orgScope + closest
	return r.closestSharedRepoPath(ctx, query, path, params)
}

func (r *Reader) closestSharedRepoPath(ctx context.Context, query, path string, args []any) (*string, error) {
	var repoID, sharedPath string
	var groupID int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&repoID, &sharedPath, &groupID); err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(path, sharedPath)
	if relative == "" {
		relative = "/"
	}
	// The wire result is a serialized JSON object inside a Searpc string.
	var result strings.Builder
	encoder := json.NewEncoder(&result)
	encoder.SetEscapeHTML(false)
	result.WriteString(`{"repo_id": `)
	_ = encoder.Encode(repoID)
	encodedRepo := strings.TrimSuffix(result.String(), "\n")
	result.Reset()
	_ = encoder.Encode(relative)
	converted := encodedRepo + `, "path": ` + strings.TrimSuffix(result.String(), "\n")
	if groupID > 0 {
		converted += `, "group_id": ` + strconv.Itoa(groupID)
	}
	converted += "}"
	return &converted, nil
}
