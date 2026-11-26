package seafserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"golang.org/x/text/unicode/norm"
)

func (w *fileWriter) postFile(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	values, rpcErr := fileArguments(args, 5)
	if rpcErr != nil {
		return -1, rpcErr
	}
	repoID, stagedPath, parent, name, user := values[0], values[1], values[2], norm.NFC.String(values[3]), values[4]
	if !validWriteName(name) {
		return -1, rpcError(errBadArgs, "Invalid filename")
	}
	parents, rpcErr := fileWritePath(parent)
	if rpcErr != nil || strings.Contains(parent, "//") {
		return -1, rpcError(errBadArgs, "Invalid parent dir")
	}
	tree, rpcErr := w.writableTree(ctx, repoID, user, true)
	if rpcErr != nil {
		return -1, rpcErr
	}
	source, err := w.reader.openStagedSource(stagedPath)
	if err != nil {
		return -1, rpcError(errBadArgs, "Invalid input file")
	}
	defer source.Close()

	for attempt := range maxFileWriteRetries {
		if attempt != 0 {
			tree, rpcErr = w.writableTree(ctx, repoID, user, true)
			if rpcErr != nil {
				return -1, rpcErr
			}
		}
		rpcErr = tree.change(parents, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
			unique := importedName(name, dir.Entries)
			if unique == "" {
				return false, rpcError(errGeneral, "Failed to post file")
			}
			if source.stat.Size > 0 && w.quota.check(ctx, repoID, source.stat.Size) != 0 {
				return false, rpcError(519, "Quota is full")
			}
			id, rpcErr := w.indexStagedSource(ctx, tree, source)
			if rpcErr != nil {
				return false, rpcErr
			}
			dir.Entries = append(dir.Entries, fsmgr.NewDirent(id, unique, emptyFileMode, tree.now, user, source.stat.Size))
			return true, nil
		})
		if rpcErr != nil {
			if rpcErr.Code == errPathMissing {
				return -1, rpcError(errGeneral, "Failed to post file")
			}
			return -1, rpcErr
		}
		commit, err := w.prepareCommit(tree, fmt.Sprintf("Added \"%s\"", name))
		if err != nil {
			return -1, fileWriteFailure("write import commit", err)
		}
		if err := w.publishStagedFileWrite(ctx, tree.repo.HeadCommitID, commit); errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		} else if err != nil {
			return -1, fileWriteFailure("publish import commit", err)
		}
		w.reader.PublishRepoUpdate(ctx, repoID, commit.CommitID, tree.repo.VirtualInfo != nil)
		return 0, nil
	}
	return -1, rpcError(520, "Concurrent upload")
}

func importedName(name string, entries []*fsmgr.SeafDirent) string {
	if findEntry(entries, name) < 0 {
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for suffix := 1; suffix < 1000; suffix++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, suffix, ext)
		if findEntry(entries, candidate) < 0 {
			return candidate
		}
	}
	return ""
}

func (w *fileWriter) indexStagedSource(ctx context.Context, tree *fileTree, source *stagedSource) (string, *searpc.RPCError) {
	if _, err := source.Seek(0, io.SeekStart); err != nil || !source.unchanged() {
		return "", rpcError(errBadArgs, "Invalid input file")
	}
	id, rpcErr := w.indexLocalFile(ctx, tree, source.File, source.stat.Size)
	if !source.unchanged() {
		return "", rpcError(errBadArgs, "Invalid input file")
	}
	return id, rpcErr
}
