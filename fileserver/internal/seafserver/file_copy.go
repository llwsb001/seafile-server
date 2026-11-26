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
	from, to, rpcErr := w.copyTrees(ctx, transfer)
	if rpcErr != nil {
		return nil, rpcErr
	}
	direct := linkedTransfer(from, to)
	if !direct && intArgument(args, 8) == 0 {
		return w.moves.submit(intArgument(args, 7) != 0, func(ctx context.Context, task *fileMoveTask) *searpc.RPCError {
			return w.runCopy(ctx, transfer, task)
		})
	}
	if rpcErr := w.runCopy(ctx, transfer, nil); rpcErr != nil {
		if !direct && rpcErr.Code == errGeneral {
			return nil, rpcError(errGeneral, "Failed to copy")
		}
		return nil, rpcErr
	}
	return map[string]any{"background": false, "task-id": nil}, nil
}

func (w *fileWriter) copyTrees(ctx context.Context, transfer libraryTransfer) (*fileTree, *fileTree, *searpc.RPCError) {
	to, rpcErr := w.writableTree(ctx, transfer.toRepo, transfer.user, true)
	if rpcErr != nil {
		return nil, nil, rpcErr
	}
	if transfer.fromRepo == transfer.toRepo {
		return to, to, nil
	}

	repo, err := w.reader.Repo(ctx, transfer.fromRepo)
	if err != nil {
		return nil, nil, rpcError(errBadArgs, "Invalid repo")
	}
	if repo.IsCorrupted || repo.Version != 1 {
		return nil, nil, rpcError(errBadArgs, "File RPC requires a version 1 library")
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
		if linkedTransfer(from, to) {
			description, rpcErr = insertLinkedTransfer(from, to, transfer, "copy")
		} else {
			description, rpcErr = w.copyAcrossLibraries(ctx, from, to, transfer, task)
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

func (w *fileWriter) copyAcrossLibraries(ctx context.Context, from, to *fileTree, transfer libraryTransfer, task *fileMoveTask) (string, *searpc.RPCError) {
	copier := libraryCopy{ctx: ctx, from: from, to: to, task: task, limits: w.limits}
	defer copier.clearKeys()
	if rpcErr := copier.loadKeys(w.passwords); rpcErr != nil {
		return "", rpcErr
	}
	return copier.insert(transfer, w.quota)
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

func linkedTransfer(from, to *fileTree) bool {
	return from.repo.ID == to.repo.ID ||
		from.repo.VirtualInfo != nil && from.repo.VirtualInfo.OriginRepoID == to.repo.ID ||
		to.repo.VirtualInfo != nil && to.repo.VirtualInfo.OriginRepoID == from.repo.ID
}

func originPath(tree *fileTree, path []string) []string {
	if virtual := tree.repo.VirtualInfo; virtual != nil {
		base := strings.FieldsFunc(virtual.Path, func(r rune) bool { return r == '/' })
		return append(base, path...)
	}
	return path
}

func insertLinkedTransfer(from, to *fileTree, transfer libraryTransfer, action string) (string, *searpc.RPCError) {
	dir, rpcErr := from.reader.directoryByPath(from.repo, from.root, strings.Join(transfer.fromPath, "/"))
	if rpcErr != nil {
		return "", rpcErr
	}
	source, target := originPath(from, transfer.fromPath), originPath(to, transfer.toPath)
	entries := make([]*fsmgr.SeafDirent, 0, len(transfer.names))
	for i, name := range transfer.names {
		if len(target) > len(source) && slices.Equal(target[:len(source)], source) && target[len(source)] == name {
			return "", rpcError(errGeneral, "Can not "+action+" directory to its subdirectory")
		}
		index := findEntry(dir.Entries, name)
		if index == -1 {
			return "", rpcError(errBadArgs, fmt.Sprintf("File %s does not exist", name))
		}
		entry := dir.Entries[index]
		entries = append(entries, fsmgr.NewDirent(entry.ID, transfer.targets[i], entry.Mode, entry.Mtime, to.user, entry.Size))
	}
	if rpcErr := to.insertMoved(transfer.toPath, entries, transfer.replace); rpcErr != nil {
		return "", rpcError(errGeneral, "Failed to cp file")
	}
	return describeTransferTarget(transfer.targets, entries[0].Mode), nil
}
