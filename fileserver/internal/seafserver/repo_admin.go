package seafserver

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// AdminRepoReadRPCHandlers exposes the SQLite repository inventory on the management socket.
func (r *Reader) AdminRepoReadRPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_get_repo_list": r.listAdminRepos,
		"seafile_count_repos":   r.countAdminRepos,
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

func (r *Reader) listAdminRepos(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	var query strings.Builder
	query.WriteString(adminRepoColumns + ", COALESCE(f.file_count, 0)" + adminRepoTables + adminRepoCountJoin + " WHERE r.repo_id IS NOT NULL")
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
