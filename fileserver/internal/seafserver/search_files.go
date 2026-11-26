package seafserver

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

type fileSearchResult struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	IsDir bool   `json:"is-dir"`
}

func (r *Reader) searchFiles(scoped bool) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id := stringArgument(args, 0)
		if id == nil || !validRepoID(*id) {
			return nil, rpcError(errBadArgs, "Invalid repo id")
		}

		var path string
		query := stringArgument(args, 1)
		if scoped {
			query = stringArgument(args, 2)
			if arg := stringArgument(args, 1); arg != nil && *arg != "/" {
				path = *arg
			}
		}
		if query == nil {
			return nil, rpcError(errBadArgs, "Invalid search query")
		}
		repo, err := r.Repo(ctx, *id)
		if err != nil {
			return nil, nil
		}
		dir, rpcErr := r.directoryByPath(repo, repo.RootID, path)
		if rpcErr != nil {
			return nil, nil
		}

		var results []fileSearchResult
		if err := r.searchDirectory(repo, dir, path, asciiLower(*query), &results); err != nil {
			log.Printf("search_files: %v", err)
		}
		// The C traversal prepends each match in depth-first order.
		slices.Reverse(results)
		return results, nil
	}
}

func (r *Reader) searchDirectory(repo *repomgr.Repo, dir *fsmgr.SeafDir, path, query string, results *[]fileSearchResult) error {
	for _, entry := range dir.Entries {
		entryPath := path + "/" + entry.Name
		isDir := fsmgr.IsDir(entry.Mode)
		if strings.Contains(asciiLower(entry.Name), query) {
			*results = append(*results, fileSearchResult{Path: entryPath, Size: entry.Size, Mtime: entry.Mtime, IsDir: isDir})
		}
		if isDir {
			child, err := r.Directory(repo, entry.ID)
			if err != nil {
				return err
			}
			if err := r.searchDirectory(repo, child, entryPath, query, results); err != nil {
				return err
			}
		}
	}
	return nil
}
