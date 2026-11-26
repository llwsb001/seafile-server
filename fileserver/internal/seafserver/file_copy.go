package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (w *fileWriter) copyFiles(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	if len(args) != 9 {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	values, rpcErr := fileArguments(args[:7], 7)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if !validRepoID(values[0]) || !validRepoID(values[3]) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	transfer, rpcErr := parseLibraryTransfer(values, values[6])
	if rpcErr != nil {
		return nil, rpcErr
	}
	if transfer.fromRepo != transfer.toRepo && intArgument(args, 8) == 0 {
		if _, _, rpcErr := w.copyTrees(ctx, transfer); rpcErr != nil {
			return nil, rpcErr
		}
		return w.moves.submit(intArgument(args, 7) != 0, func(ctx context.Context, task *fileMoveTask) *searpc.RPCError {
			return w.runCopy(ctx, transfer, task)
		})
	}
	if rpcErr := w.runCopy(ctx, transfer, nil); rpcErr != nil {
		if transfer.fromRepo != transfer.toRepo && rpcErr.Code == errGeneral {
			return nil, rpcError(errGeneral, "Failed to copy")
		}
		return nil, rpcErr
	}
	return map[string]any{"background": false, "task-id": nil}, nil
}

func (w *fileWriter) copyTrees(ctx context.Context, transfer libraryTransfer) (*fileTree, *fileTree, *searpc.RPCError) {
	to, rpcErr := w.writableTree(ctx, transfer.toRepo, transfer.user, false)
	if rpcErr != nil {
		return nil, nil, rpcErr
	}
	if to.repo.IsEncrypted {
		return nil, nil, rpcError(errBadArgs, "Copy requires unencrypted libraries")
	}
	if transfer.fromRepo == transfer.toRepo {
		return to, to, nil
	}

	repo, err := w.reader.Repo(ctx, transfer.fromRepo)
	if err != nil {
		return nil, nil, rpcError(errBadArgs, "Invalid repo")
	}
	if repo.IsCorrupted || repo.Version != 1 || repo.VirtualInfo != nil {
		return nil, nil, rpcError(errBadArgs, "File RPC requires an ordinary version 1 library")
	}
	if repo.IsEncrypted {
		return nil, nil, rpcError(errBadArgs, "Copy requires unencrypted libraries")
	}
	permission, err := w.permissions.check(ctx, repo.ID, transfer.user)
	if err != nil {
		return nil, nil, fileWriteFailure("check file copy permission", err)
	}
	if permission == nil || (*permission != "r" && *permission != "rw") {
		return nil, nil, rpcError(errRepoAuth, "Permission denied")
	}
	from := &fileTree{reader: w.reader, repo: repo, root: repo.RootID, user: transfer.user, now: time.Now().Unix()}
	return from, to, nil
}

func (w *fileWriter) runCopy(ctx context.Context, transfer libraryTransfer, task *fileMoveTask) *searpc.RPCError {
	for range maxFileWriteRetries {
		from, to, rpcErr := w.copyTrees(ctx, transfer)
		if rpcErr != nil {
			return rpcErr
		}
		var description string
		if from == to {
			description, rpcErr = copyWithinLibrary(to, transfer)
		} else {
			copy := libraryCopy{ctx: ctx, from: from, to: to, task: task, limits: w.limits}
			description, rpcErr = copy.insert(transfer, w.quota)
		}
		if rpcErr != nil {
			return rpcErr
		}
		if _, _, rpcErr := w.copyTrees(ctx, transfer); rpcErr != nil {
			return rpcErr
		}
		err := w.publishCopy(ctx, from, to, description, task)
		if errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		}
		if err != nil {
			return fileWriteFailure("publish file copy", err)
		}
		return nil
	}
	return rpcError(520, "Concurrent update")
}

func (w *fileWriter) publishCopy(ctx context.Context, from, to *fileTree, desc string, task *fileMoveTask) error {
	commit, err := w.prepareCommit(to, desc)
	if err != nil {
		return err
	}
	return w.publishTransfer(ctx, task, func(conn *sql.Conn) (bool, error) {
		if from != to {
			var head string
			err := conn.QueryRowContext(ctx, "SELECT commit_id FROM Branch WHERE repo_id = ? AND name = 'master'",
				from.repo.ID).Scan(&head)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && head != from.repo.HeadCommitID) {
				return false, ErrSQLiteBranchChanged
			}
			if err != nil {
				return false, err
			}
		}
		return publishFileWrite(ctx, conn, to.repo.HeadCommitID, commit)
	})
}

func copyWithinLibrary(tree *fileTree, transfer libraryTransfer) (string, *searpc.RPCError) {
	dir, rpcErr := tree.reader.directoryByPath(tree.repo, tree.root, strings.Join(transfer.fromPath, "/"))
	if rpcErr != nil {
		return "", rpcErr
	}
	entries := make([]*fsmgr.SeafDirent, 0, len(transfer.names))
	for i, name := range transfer.names {
		if len(transfer.toPath) > len(transfer.fromPath) &&
			slices.Equal(transfer.toPath[:len(transfer.fromPath)], transfer.fromPath) &&
			transfer.toPath[len(transfer.fromPath)] == name {
			return "", rpcError(errGeneral, "Can not copy directory to its subdirectory")
		}
		index := findEntry(dir.Entries, name)
		if index == -1 {
			return "", rpcError(errBadArgs, fmt.Sprintf("File %s does not exist", name))
		}
		entry := dir.Entries[index]
		entries = append(entries, fsmgr.NewDirent(entry.ID, transfer.targets[i], entry.Mode, entry.Mtime, tree.user, entry.Size))
	}
	if rpcErr := tree.insertMoved(transfer.toPath, entries, false); rpcErr != nil {
		return "", rpcError(errGeneral, "Failed to cp file")
	}
	return describeTransferTarget(transfer.targets, entries[0].Mode), nil
}
