package seafserver

import (
	"context"
	"database/sql"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const shareRepoColumns = `
sh.repo_id, v.repo_id, v.origin_repo, v.path, sh.permission, b.commit_id, COALESCE(s.size, 0),
i.name, COALESCE(i.update_time, 0), COALESCE(i.version, 0), COALESCE(i.is_encrypted, 0),
i.last_modifier, COALESCE(i.status, 0), i.type`

const shareRepoJoins = `
JOIN Branch b ON b.repo_id = sh.repo_id AND b.name = 'master'
LEFT JOIN VirtualRepo v ON v.repo_id = sh.repo_id
LEFT JOIN RepoInfo i ON i.repo_id = sh.repo_id
LEFT JOIN RepoSize s ON s.repo_id = sh.repo_id`

const groupRepoSelect = "SELECT " + shareRepoColumns + ", sh.user_name, COALESCE(sh.group_id, 0), origin.name" +
	" FROM RepoGroup sh " + shareRepoJoins + " LEFT JOIN RepoInfo origin ON origin.repo_id = v.origin_repo"

const publicRepoSelect = "SELECT " + shareRepoColumns + ", owner.owner_id, 0, NULL" +
	" FROM InnerPubRepo sh " + shareRepoJoins + " JOIN RepoOwner owner ON owner.repo_id = sh.repo_id"

type sharedRepo struct {
	id, head                              string
	virtualID, originID, path, originName *string
	name, modifier, kind                  *string
	permission, user                      *string
	size, mtime                           int64
	status, groupID, version, encrypted   int
}

func scanSharedRepo(row *sql.Rows) (sharedRepo, error) {
	var repo sharedRepo
	err := row.Scan(&repo.id, &repo.virtualID, &repo.originID, &repo.path, &repo.permission, &repo.head, &repo.size,
		&repo.name, &repo.mtime, &repo.version, &repo.encrypted, &repo.modifier, &repo.status, &repo.kind,
		&repo.user, &repo.groupID, &repo.originName)
	return repo, err
}

func (r *Reader) readSharedRepos(ctx context.Context, shareType, query string, groupNames map[int]*string, message string, args ...any) (any, *searpc.RPCError) {
	repos, rpcErr := readShareRows(ctx, r.db, message, query, scanSharedRepo, args...)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var result []map[string]any
	for _, repo := range repos {
		if repo.name == nil || repo.modifier == nil {
			commit, err := r.Commit(repo.id, repo.head)
			if err != nil {
				log.Printf("SQLite share read: load commit %s/%s: %v", repo.id, repo.head, err)
				continue
			}
			repo.name, repo.mtime, repo.version = &commit.RepoName, commit.Ctime, commit.Version
			repo.modifier = nil
			if commit.CreatorName != "" {
				modifier := asciiLower(commit.CreatorName)
				repo.modifier = &modifier
			}
			repo.encrypted = 0
			if commit.Encrypted == "true" {
				repo.encrypted = 1
			}
		}
		object := newRepoObject(repo.id)
		object["name"], object["repo-name"] = repo.name, repo.name
		object["last-modifier"], object["head-cmmt-id"] = repo.modifier, repo.head
		object["last-modify"], object["last-modified"] = repo.mtime, repo.mtime
		object["version"], object["encrypted"] = repo.version, repo.encrypted != 0
		object["size"], object["status"], object["repo-type"] = repo.size, repo.status, repo.kind
		object["permission"], object["user"], object["share-type"] = repo.permission, lowerNullable(repo.user), shareType
		groupID := int(int32(repo.groupID))
		object["group-id"], object["group-name"] = groupID, groupNames[groupID]
		if repo.virtualID != nil {
			object["is-virtual"] = true
			object["store-id"], object["origin-repo-id"] = repo.originID, repo.originID
			object["origin-path"], object["origin-repo-name"] = repo.path, repo.originName
		}
		result = append(result, object)
	}
	return result, nil
}
