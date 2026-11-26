package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// AdminRepoReadRPCHandlers exposes the SQLite repository inventory on the management socket.
func (r *Reader) AdminRepoReadRPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_get_repo_list":          r.listAdminRepos,
		"seafile_count_repos":            r.countAdminRepos,
		"seafile_search_repos_by_name":   r.searchAdminReposByName,
		"seafile_get_repos_by_id_prefix": r.searchAdminReposByIDPrefix,
		"get_system_default_repo_id":     r.getSystemDefaultRepoID,
	}
}

const adminRepoColumns = `
SELECT i.repo_id, COALESCE(s.size, 0), b.commit_id, i.name, COALESCE(i.update_time, 0),
       COALESCE(i.version, 0), COALESCE(i.is_encrypted, 0), i.last_modifier,
       COALESCE(i.status, 0), i.type`

const adminRepoTables = `
FROM RepoInfo i
LEFT JOIN RepoSize s ON i.repo_id = s.repo_id
LEFT JOIN Branch b ON i.repo_id = b.repo_id`

const adminRepoCountJoin = `
LEFT JOIN RepoFileCount f ON i.repo_id = f.repo_id
LEFT JOIN Repo r ON i.repo_id = r.repo_id
LEFT JOIN VirtualRepo v ON i.repo_id = v.repo_id`

const adminRepoQuery = adminRepoColumns + ", COALESCE(f.file_count, 0)" + adminRepoTables + adminRepoCountJoin

func (r *Reader) listAdminRepos(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	var query strings.Builder
	query.WriteString(adminRepoQuery + " WHERE r.repo_id IS NOT NULL")
	if intArgument(args, 3) == 0 {
		query.WriteString(" AND v.repo_id IS NULL")
	}
	order := "i.update_time"
	if requested := stringArgument(args, 2); requested != nil {
		switch *requested {
		case "size":
			order = "s.size"
		case "file_count":
			order = "f.file_count"
		}
	}
	query.WriteString(" ORDER BY " + order + " DESC, i.repo_id")
	start, limit := intArgument(args, 0), intArgument(args, 1)
	if start == -1 && limit == -1 {
		return r.adminRepoRows(ctx, query.String()), nil
	}
	query.WriteString(" LIMIT ? OFFSET ?")
	return r.adminRepoRows(ctx, query.String(), limit, start), nil
}

func (r *Reader) countAdminRepos(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	var count int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(repo_id) FROM Repo").Scan(&count); err != nil {
		log.Printf("count repositories: %v", err)
		return int64(-1), rpcError(errGeneral, "Failed to count repos from db")
	}
	return count, nil
}

func (r *Reader) getSystemDefaultRepoID(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	var id *string
	const query = "SELECT info_value FROM SystemInfo WHERE info_key = 'default_repo_id'"
	if err := r.db.QueryRowContext(ctx, query).Scan(&id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("get system default repository: %v", err)
	}
	return id, nil
}

func (r *Reader) searchAdminReposByName(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	// GLib prints a null name as "(null)" in the C LIKE pattern.
	term := "(null)"
	if name := stringArgument(args, 0); name != nil {
		term = *name
	}
	const query = adminRepoQuery + `
WHERE i.name LIKE ? COLLATE NOCASE AND r.repo_id IS NOT NULL AND v.repo_id IS NULL
ORDER BY i.update_time DESC, i.repo_id`
	return r.adminRepoRows(ctx, query, "%"+term+"%"), nil
}

func (r *Reader) searchAdminReposByIDPrefix(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	prefix := stringArgument(args, 0)
	if prefix == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if len(*prefix) > 36 {
		return nil, nil
	}
	// Unlike the name search, the C ID lookup also includes orphaned RepoInfo records.
	query := adminRepoColumns + ", 0" + adminRepoTables + `
LEFT JOIN VirtualRepo v ON i.repo_id = v.repo_id
WHERE i.repo_id LIKE ? AND v.repo_id IS NULL
ORDER BY i.update_time DESC, i.repo_id`
	parameters := []any{*prefix + "%"}
	start, limit := intArgument(args, 1), intArgument(args, 2)
	if start != -1 || limit != -1 {
		query += " LIMIT ? OFFSET ?"
		parameters = append(parameters, limit, start)
	}
	result := r.adminRepoRows(ctx, query, parameters...)
	// The RPC returns the C collector's prepended list after SQLite applies the page.
	slices.Reverse(result)
	return result, nil
}

func (r *Reader) adminRepoRows(ctx context.Context, query string, args ...any) []map[string]any {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		log.Printf("query repository inventory: %v", err)
		return nil
	}
	defer rows.Close()

	var result []map[string]any
	for rows.Next() {
		var repo adminRepoRow
		if err := rows.Scan(&repo.id, &repo.size, &repo.head, &repo.name, &repo.modified, &repo.version,
			&repo.encrypted, &repo.modifier, &repo.status, &repo.kind, &repo.fileCount); err != nil {
			log.Printf("scan repository inventory: %v", err)
			return nil
		}
		result = append(result, repo.object())
	}
	if err := rows.Err(); err != nil {
		log.Printf("read repository inventory: %v", err)
		return nil
	}
	return result
}

type adminRepoRow struct {
	id                         string
	head, name, modifier, kind *string
	size, modified, fileCount  int64
	version, encrypted, status int
}

func (repo adminRepoRow) object() map[string]any {
	object := newRepoObject(repo.id)
	// C lists shallow SQLite metadata, including rows whose head is missing.
	object["root"], object["magic"], object["pwd-hash"], object["store-id"] = "", "", "", ""
	if repo.head == nil {
		object["is-corrupted"] = true
		return object
	}
	object["head-cmmt-id"], object["size"], object["file-count"] = *repo.head, repo.size, repo.fileCount
	object["repo-type"] = repo.kind
	if repo.name != nil {
		object["name"], object["repo-name"] = repo.name, repo.name
		object["last-modify"], object["last-modified"] = repo.modified, repo.modified
		object["version"], object["last-modifier"] = repo.version, repo.modifier
		object["encrypted"], object["status"] = repo.encrypted != 0, repo.status
	}
	return object
}
