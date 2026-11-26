package seafserver

import (
	"context"
	"encoding/json"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const selectOwnedRepos = `
SELECT owner.repo_id, COALESCE(size.size, 0), branch.commit_id,
       info.name, COALESCE(info.update_time, 0), COALESCE(info.version, 0),
       COALESCE(info.is_encrypted, 0), info.last_modifier, COALESCE(info.status, 0), info.type
FROM RepoOwner owner
LEFT JOIN RepoSize size ON size.repo_id = owner.repo_id
LEFT JOIN Branch branch ON branch.repo_id = owner.repo_id
LEFT JOIN RepoInfo info ON info.repo_id = owner.repo_id
LEFT JOIN VirtualRepo view ON view.repo_id = owner.repo_id
WHERE owner.owner_id = ? AND view.repo_id IS NULL
ORDER BY info.update_time DESC, owner.repo_id
LIMIT ? OFFSET ?`

type ownedRepo struct {
	id                         string
	head, name, modifier, kind *string
	size, lastModified         int64
	version, encrypted, status int
}

func (r *Reader) listOwnedRepos(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	owner := stringArgument(args, 0)
	if owner == nil {
		return nil, nil
	}
	includeCorrupted := intArgument(args, 1) != 0
	start, limit := intArgument(args, 2), intArgument(args, 3)
	rows, err := r.db.QueryContext(ctx, selectOwnedRepos, *owner, limit, start)
	if err != nil {
		log.Printf("list owned repositories: %v", err)
		return nil, nil
	}
	defer rows.Close()

	// The C listing applies the page limit before omitting corrupted rows.
	var result []map[string]any
	for rows.Next() {
		var repo ownedRepo
		if err := rows.Scan(&repo.id, &repo.size, &repo.head, &repo.name, &repo.lastModified, &repo.version,
			&repo.encrypted, &repo.modifier, &repo.status, &repo.kind); err != nil {
			log.Printf("scan owned repository: %v", err)
			return nil, nil
		}
		if object := r.ownedRepoObject(repo, includeCorrupted); object != nil {
			result = append(result, object)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("read owned repositories: %v", err)
		return nil, nil
	}
	return result, nil
}

func (r *Reader) ownedRepoObject(repo ownedRepo, includeCorrupted bool) map[string]any {
	object := newRepoObject(repo.id)
	// The C listing uses shallow repository objects with empty backing arrays.
	object["root"], object["magic"], object["pwd-hash"], object["store-id"] = "", "", "", ""
	if repo.head == nil {
		if !includeCorrupted {
			return nil
		}
		object["is-corrupted"] = true
		return object
	}

	object["head-cmmt-id"], object["size"], object["repo-type"] = *repo.head, repo.size, repo.kind
	if repo.name == nil {
		// collect_repos_fill_size_commit only loads status when RepoInfo has a name.
		repo.lastModified, repo.version, repo.encrypted, repo.status, repo.modifier = 0, 0, 0, 0, nil
	}
	encrypted := repo.encrypted != 0
	if repo.name == nil || repo.modifier == nil {
		// Recover legacy metadata in memory so this RPC can use the read-only connection.
		commit, err := r.Commit(repo.id, *repo.head)
		if err != nil {
			if !includeCorrupted {
				return nil
			}
			object["is-corrupted"] = true
		} else {
			repo.name, repo.modifier = &commit.RepoName, &commit.CreatorName
			repo.lastModified, repo.version, encrypted = commit.Ctime, commit.Version, commit.Encrypted == "true"
		}
	}
	object["name"], object["repo-name"] = repo.name, repo.name
	object["last-modifier"], object["version"] = repo.modifier, repo.version
	object["last-modify"], object["last-modified"] = repo.lastModified, repo.lastModified
	object["encrypted"], object["status"] = encrypted, repo.status
	return object
}
