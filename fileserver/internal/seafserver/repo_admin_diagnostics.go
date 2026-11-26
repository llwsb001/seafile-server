package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

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
