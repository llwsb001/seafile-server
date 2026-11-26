package seafserver

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

type fileLastModified struct {
	Name string `json:"file-name"`
	Time int64  `json:"last-modified"`
}

func (r *Reader) filesLastModified(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, path := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || path == nil {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	if !validRepoID(*repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errGeneral, "No such repo "+*repoID)
	}
	head, err := r.Commit(*repoID, repo.HeadCommitID)
	if err != nil {
		return nil, rpcError(errGeneral, "Failed to get commit "+repo.HeadCommitID)
	}
	dirPath := formatPath(*path)
	dir, rpcErr := r.directoryByPath(repo, head.RootID, dirPath)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if len(dir.Entries) == 0 {
		return nil, nil
	}

	limit := intArgument(args, 2)
	fileIDs := make(map[string]string, len(dir.Entries))
	times := make(map[string]int64, len(dir.Entries))
	for _, entry := range dir.Entries {
		fileIDs[entry.Name] = entry.ID
		times[entry.Name] = head.Ctime
	}

	queue := newCommitQueue(r, *repoID, head)
	var count int
	for len(queue.pending) > 0 {
		commit := queue.pop()
		if commit != head {
			dir, rpcErr = r.directoryByPath(repo, commit.RootID, dirPath)
			if rpcErr != nil {
				if rpcErr.Code == errPathMissing {
					// This branch predates the current directory path.
					continue
				}
				return nil, rpcError(errGeneral, "failed to traverse commit of repo "+*repoID)
			}
		}
		updateModificationTimes(fileIDs, times, dir.Entries, commit.Ctime)
		if len(fileIDs) == 0 {
			continue
		}
		if err := queue.addParents(commit); err != nil {
			return nil, rpcError(errGeneral, "failed to traverse commit of repo "+*repoID)
		}
		count++
		// Finish walking both sides of a merge before applying the limit.
		if limit > 0 && count >= limit && len(queue.pending) <= 1 {
			break
		}
	}

	// C iterates a hash table. Emit a stable order for the same name/time set.
	result := make([]fileLastModified, 0, len(times))
	for name, timestamp := range times {
		result = append(result, fileLastModified{Name: name, Time: timestamp})
	}
	slices.SortFunc(result, func(a, b fileLastModified) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return result, nil
}

func updateModificationTimes(fileIDs map[string]string, times map[string]int64, entries []*fsmgr.SeafDirent, timestamp int64) {
	for _, entry := range entries {
		if id, exists := fileIDs[entry.Name]; exists {
			if entry.ID == id {
				times[entry.Name] = timestamp
			} else {
				delete(fileIDs, entry.Name)
			}
		}
	}
	for name := range fileIDs {
		// Preserve C's treatment of commits created within the same second.
		if times[name] != timestamp {
			delete(fileIDs, name)
		}
	}
}
