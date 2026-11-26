package seafserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

func (w *fileWriter) revertEntry(directory bool) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		values, rpcErr := fileArguments(args, 4)
		if rpcErr != nil {
			return -1, rpcError(errBadArgs, "Bad arguments")
		}
		repoID, oldID, path, user := values[0], values[1], values[2], values[3]
		if !validRepoID(repoID) {
			return -1, rpcError(errBadArgs, "Invalid repo id")
		}
		if !utils.IsObjectIDValid(oldID) {
			return -1, rpcError(errBadArgs, "Invalid commit id")
		}
		for range maxFileWriteRetries {
			tree, rpcErr := w.writableTree(ctx, repoID, user, true)
			if rpcErr != nil {
				return -1, rpcErr
			}
			if tree.repo.HeadCommitID == oldID {
				return 0, nil
			}
			old, err := w.reader.Commit(repoID, oldID)
			if err != nil {
				return -1, rpcError(errBadArgs, "Invalid commit")
			}
			if old.RepoID != repoID {
				return -1, rpcError(errBadCommit, "bad commit id")
			}
			entry, parents, rpcErr := tree.entryToRestore(old.RootID, path)
			if rpcErr != nil {
				return -1, rpcErr
			}
			if (directory && fsmgr.IsRegular(entry.Mode)) || (!directory && fsmgr.IsDir(entry.Mode)) {
				return -1, nil
			}
			parentRoot, rpcErr := tree.restoreEntry(entry, parents, directory)
			if rpcErr != nil {
				return -1, rpcErr
			}
			if tree.root == parentRoot {
				return 0, nil
			}
			desc := fmt.Sprintf("Reverted file \"%s\" to status at %s",
				entry.Name, time.Unix(old.Ctime, 0).Format(time.DateTime))
			if directory {
				desc = fmt.Sprintf("Recovered deleted directory \"%s\"", entry.Name)
			}
			err = w.publishRestore(ctx, tree, parentRoot, parents, desc)
			if errors.Is(err, ErrSQLiteBranchChanged) {
				continue
			}
			if err != nil {
				return -1, fileWriteFailure("publish restored entry", err)
			}
			return 0, nil
		}
		return -1, rpcError(520, "Concurrent update")
	}
}

func (t *fileTree) entryToRestore(rootID, path string) (*fsmgr.SeafDirent, []string, *searpc.RPCError) {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	if len(parts) == 0 {
		return nil, nil, rpcError(errBadArgs, "Invalid dir")
	}
	if len(path) >= 4096 {
		return nil, nil, rpcError(errBadArgs, "Invalid path")
	}
	for _, name := range parts {
		if !validWriteName(name) {
			return nil, nil, rpcError(errBadArgs, "Invalid path")
		}
	}
	parents := parts[:len(parts)-1]
	dir, rpcErr := t.reader.directoryByPath(t.repo, rootID, strings.Join(parents, "/"))
	if rpcErr != nil {
		return nil, nil, rpcError(errBadArgs, "Invalid dir")
	}
	index := findEntry(dir.Entries, parts[len(parts)-1])
	if index < 0 {
		return nil, nil, rpcError(errBadArgs, "failed to get dirent")
	}
	return dir.Entries[index], parents, nil
}

func (t *fileTree) restoreEntry(old *fsmgr.SeafDirent, parents []string, directory bool) (string, *searpc.RPCError) {
	if _, rpcErr := t.reader.directoryByPath(t.repo, t.root, strings.Join(parents, "/")); rpcErr != nil {
		if rpcErr.Code != errPathMissing || t.makeParents(nil, parents) != nil {
			return "", rpcError(errGeneral, "internal error")
		}
	}
	parentRoot := t.root
	restore := *old
	restore.Mode = emptyFileMode
	if directory {
		restore.Mode, restore.Modifier, restore.Size = dirMode, "", 0
	}
	rpcErr := t.change(parents, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
		for suffix := 1; ; suffix++ {
			index := findEntry(dir.Entries, restore.Name)
			if index < 0 {
				dir.Entries = append(dir.Entries, &restore)
				return true, nil
			}
			current := dir.Entries[index]
			sameType := (directory && fsmgr.IsDir(current.Mode)) || (!directory && fsmgr.IsRegular(current.Mode))
			if sameType && current.ID == restore.ID {
				return false, nil
			}
			if !directory && fsmgr.IsRegular(current.Mode) {
				dir.Entries[index] = &restore
				return true, nil
			}
			restore.Name = restoredEntryName(old.Name, suffix, directory)
		}
	})
	return parentRoot, rpcErr
}

func restoredEntryName(name string, suffix int, directory bool) string {
	if directory {
		return fmt.Sprintf("%s (%d)", name, suffix)
	}
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		return fmt.Sprintf("%s (%d)%s", name[:dot], suffix, name[dot:])
	}
	// The Linux C RPC formats a missing extension with printf's null string.
	return fmt.Sprintf("%s (%d)(null)", name, suffix)
}

func (w *fileWriter) publishRestore(ctx context.Context, tree *fileTree, parentRoot string, parents []string, description string) error {
	var mkdir, restored *commitmgr.Commit
	var err error
	if parentRoot == tree.repo.RootID {
		restored, err = w.prepareCommit(tree, description)
	} else {
		// Preserve C's mkdir commit in the ancestry, while making the restored
		// head and its queue job visible only after both objects are durable.
		mkdirTree := *tree
		mkdirTree.root = parentRoot
		mkdir, err = w.prepareCommit(&mkdirTree, fmt.Sprintf("Added directory \"%s\"", strings.Join(parents, "/")))
		if err != nil {
			return err
		}
		restored, err = w.prepareCommitAfter(tree, description, mkdir)
	}
	if err != nil {
		return err
	}
	if err := w.publishStagedFileWrite(ctx, tree.repo.HeadCommitID, restored); err != nil {
		return err
	}
	isVirtual := tree.repo.VirtualInfo != nil
	if mkdir != nil {
		w.reader.PublishRepoUpdate(ctx, tree.repo.ID, mkdir.CommitID, isVirtual)
	}
	w.reader.PublishRepoUpdate(ctx, tree.repo.ID, restored.CommitID, isVirtual)
	return nil
}
