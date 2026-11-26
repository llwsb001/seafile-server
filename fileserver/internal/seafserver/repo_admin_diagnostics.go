package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"slices"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) adminRepoSize(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil || len(*id) != 36 {
		return int64(-1), rpcError(errBadArgs, "Bad repo id")
	}
	if !validRepoID(*id) {
		return int64(-1), rpcError(errBadArgs, "Invalid repo id")
	}
	var size int64
	err := r.db.QueryRowContext(ctx, "SELECT COALESCE(size, 0) FROM RepoSize WHERE repo_id = ?", *id).Scan(&size)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("get repository size: %v", err)
		return int64(-1), rpcError(errGeneral, "Internal server error")
	}
	if size < 0 {
		return int64(-1), rpcError(errGeneral, "Internal server error")
	}
	return size, nil
}

func (r *Reader) listOrphanRepos(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	const query = `
SELECT Repo.repo_id
FROM Repo LEFT JOIN RepoOwner ON Repo.repo_id = RepoOwner.repo_id
WHERE RepoOwner.owner_id IS NULL`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("query orphan repositories: %v", err)
		return nil, nil
	}
	defer rows.Close()

	var repoIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			log.Printf("scan orphan repository: %v", err)
			return nil, nil
		}
		repoIDs = append(repoIDs, id)
	}
	if err := rows.Err(); err != nil {
		log.Printf("read orphan repositories: %v", err)
		return nil, nil
	}

	// The C orphan listing loads the full repo and omits missing or corrupt heads.
	var result []map[string]any
	for _, id := range repoIDs {
		repo, err := r.Repo(ctx, id)
		if err == nil {
			result = append(result, repoObject(repo))
		}
	}
	return result, nil
}

func (r *Reader) totalAdminRepoFiles(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	const query = `
SELECT COALESCE(SUM(f.file_count), 0)
FROM RepoFileCount f
LEFT JOIN VirtualRepo v ON f.repo_id = v.repo_id
JOIN Repo r ON f.repo_id = r.repo_id
WHERE v.repo_id IS NULL`
	return r.adminRepoTotal(ctx, query, "Failed to get total file number from db.")
}

func (r *Reader) totalAdminRepoStorage(ctx context.Context, _ []json.RawMessage) (any, *searpc.RPCError) {
	// C includes retained RepoSize rows after the corresponding Repo row is gone.
	const query = `
SELECT COALESCE(SUM(s.size), 0)
FROM RepoSize s LEFT JOIN VirtualRepo v ON s.repo_id = v.repo_id
WHERE v.repo_id IS NULL`
	return r.adminRepoTotal(ctx, query, "Failed to get total storage occupation from db.")
}

func (r *Reader) adminRepoTotal(ctx context.Context, query, errorMessage string) (any, *searpc.RPCError) {
	var value int64
	if err := r.db.QueryRowContext(ctx, query).Scan(&value); err != nil {
		log.Printf("%s %v", errorMessage, err)
		return int64(-1), rpcError(errGeneral, errorMessage)
	}
	return value, nil
}

func (r *Reader) adminRepoBranches(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id := stringArgument(args, 0)
	if id == nil || !validRepoID(*id) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	const query = "SELECT name, repo_id, commit_id FROM Branch WHERE repo_id = ?"
	rows, err := r.db.QueryContext(ctx, query, *id)
	if err != nil {
		log.Printf("query repository branches: %v", err)
		return nil, nil
	}
	defer rows.Close()

	var result []map[string]string
	for rows.Next() {
		var name, repoID, commitID string
		if err := rows.Scan(&name, &repoID, &commitID); err != nil {
			log.Printf("scan repository branch: %v", err)
			return nil, nil
		}
		result = append(result, map[string]string{"name": name, "repo-id": repoID, "commit-id": commitID})
	}
	if err := rows.Err(); err != nil {
		log.Printf("read repository branches: %v", err)
		return nil, nil
	}
	// The C branch collector prepends records in SQLite index-scan order.
	slices.Reverse(result)
	return result, nil
}
