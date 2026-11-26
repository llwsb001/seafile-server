package seafserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/diff"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

var historyDiffStatus = map[rune]string{
	diff.DiffStatusAdded: "add", diff.DiffStatusDeleted: "del", diff.DiffStatusModified: "mod",
	diff.DiffStatusRenamed: "mov", diff.DiffStatusDirAdded: "newdir", diff.DiffStatusDirDeleted: "deldir",
}

func (r *Reader) commitDiff(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, oldID, newID := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if repoID == nil || oldID == nil || newID == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if !validRepoID(*repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	if (*oldID != "" && !utils.IsObjectIDValid(*oldID)) || !utils.IsObjectIDValid(*newID) {
		return nil, rpcError(errBadArgs, "Invalid commit id")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errBadArgs, "No such repository")
	}
	newer, err := r.resolveDiffCommit(ctx, *repoID, *newID)
	if err != nil {
		return nil, rpcError(-1, "Can't find new commit")
	}
	var older *commitmgr.Commit
	if *oldID != "" {
		older, err = r.resolveDiffCommit(ctx, *repoID, *oldID)
		if err != nil {
			return nil, rpcError(-1, "Can't find old commit")
		}
	}
	entries, err := r.diffHistory(repo, older, newer, intArgument(args, 3) != 0)
	if err != nil {
		return nil, rpcError(-1, err.Error())
	}
	var result []map[string]any
	for _, entry := range entries {
		result = append(result, map[string]any{
			"status": optionalString(historyDiffStatus[entry.Status]),
			"name":   entry.Name, "new-name": optionalString(entry.NewName),
		})
	}
	return result, nil
}

func (r *Reader) resolveDiffCommit(ctx context.Context, repoID, ref string) (*commitmgr.Commit, error) {
	var commitID string
	err := r.db.QueryRowContext(ctx, "SELECT commit_id FROM Branch WHERE repo_id = ? AND name = ?", repoID, ref).Scan(&commitID)
	if err != nil {
		commitID = ref
	}
	return r.Commit(repoID, commitID)
}

// diffHistory uses the parents of newer when no old commit is supplied.
func (r *Reader) diffHistory(repo *repomgr.Repo, older, newer *commitmgr.Commit, foldDirs bool) ([]*diff.DiffEntry, error) {
	loadDir := func(id string) (*fsmgr.SeafDir, error) { return r.Directory(repo, id) }
	if older == nil {
		if !newer.ParentID.Valid {
			return nil, nil
		}
		first, err := r.Commit(repo.ID, newer.ParentID.String)
		if !newer.SecondParentID.Valid {
			if err != nil {
				return nil, errors.New("Can't find old commit")
			}
			older = first
		} else {
			if err != nil {
				return nil, errors.New("Failed to do diff")
			}
			second, err := r.Commit(repo.ID, newer.SecondParentID.String)
			if err != nil {
				return nil, errors.New("Failed to do diff")
			}
			entries, err := diff.DiffHistoryRoots([]string{newer.RootID, first.RootID, second.RootID}, loadDir, foldDirs)
			if err != nil {
				return nil, errors.New("Failed to do diff")
			}
			return entries, nil
		}
	}
	// C returns already collected entries if a directory cannot be loaded in a two-commit diff.
	entries, _ := diff.DiffHistoryRoots([]string{older.RootID, newer.RootID}, loadDir, foldDirs)
	return entries, nil
}
