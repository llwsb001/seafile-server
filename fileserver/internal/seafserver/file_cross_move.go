package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// FileMoveLimits contains the optional limits from the web_copy configuration.
type FileMoveLimits struct {
	MaxFiles int64
	MaxBytes int64
}

type libraryTransfer struct {
	fromRepo, toRepo string
	user             string
	fromPath, toPath []string
	names, targets   []string
	replace          bool
}

func parseLibraryTransfer(values []string, user string) (libraryTransfer, *searpc.RPCError) {
	from, rpcErr := fileWritePath(values[1])
	if rpcErr != nil {
		return libraryTransfer{}, rpcErr
	}
	to, rpcErr := fileWritePath(values[4])
	if rpcErr != nil {
		return libraryTransfer{}, rpcErr
	}
	names, validSource := fileNames(values[2])
	targets, validTarget := fileNames(values[5])
	if !validSource || !validTarget {
		return libraryTransfer{}, rpcError(errBadArgs, "Load filenames to json failed")
	}
	if len(names) != len(targets) {
		return libraryTransfer{}, rpcError(errBadArgs, "The number of files in the parameters does not match")
	}
	return libraryTransfer{fromRepo: values[0], toRepo: values[3], user: user,
		fromPath: from, toPath: to, names: names, targets: targets}, nil
}

func (w *fileWriter) moveBetweenLibraries(ctx context.Context, values []string, user string, args []json.RawMessage) (any, *searpc.RPCError) {
	move, rpcErr := parseLibraryTransfer(values, user)
	if rpcErr != nil {
		return nil, rpcErr
	}
	seen := make(map[string]bool, len(move.names))
	for _, name := range move.names {
		if seen[name] {
			return nil, rpcError(errBadArgs, "Duplicate source filename")
		}
		seen[name] = true
	}
	move.replace = intArgument(args, 6) > 0
	trees, rpcErr := w.moveTrees(ctx, move)
	if rpcErr != nil {
		return nil, rpcErr
	}
	direct := linkedTransfer(trees[0], trees[1])
	if !direct && intArgument(args, 9) == 0 {
		return w.moves.submit(intArgument(args, 8) != 0, func(ctx context.Context, task *fileMoveTask) *searpc.RPCError {
			return w.runCrossMove(ctx, move, task)
		})
	}
	if rpcErr := w.runCrossMove(ctx, move, nil); rpcErr != nil {
		if !direct && rpcErr.Code == errGeneral {
			return nil, rpcError(errGeneral, "Failed to move")
		}
		return nil, rpcErr
	}
	return map[string]any{"background": false, "task-id": nil}, nil
}

func (w *fileWriter) moveTrees(ctx context.Context, move libraryTransfer) ([2]*fileTree, *searpc.RPCError) {
	var trees [2]*fileTree
	for i, repoID := range []string{move.fromRepo, move.toRepo} {
		tree, rpcErr := w.writableTree(ctx, repoID, move.user, true)
		if rpcErr != nil {
			return trees, rpcErr
		}
		trees[i] = tree
	}
	from, to := trees[0], trees[1]
	if move.replace && from.repo.StoreID == to.repo.StoreID &&
		slices.Equal(originPath(from, move.fromPath), originPath(to, move.toPath)) {
		for _, target := range move.targets {
			if slices.Contains(move.names, target) {
				return trees, rpcError(errBadArgs, "Move would overwrite a source entry")
			}
		}
	}
	return trees, nil
}

func (w *fileWriter) runCrossMove(ctx context.Context, move libraryTransfer, task *fileMoveTask) *searpc.RPCError {
	for range maxFileWriteRetries {
		trees, rpcErr := w.moveTrees(ctx, move)
		if rpcErr != nil {
			return rpcErr
		}
		from, to := trees[0], trees[1]
		var toDesc string
		if linkedTransfer(from, to) {
			toDesc, rpcErr = insertLinkedTransfer(from, to, move, "move")
		} else {
			toDesc, rpcErr = w.copyAcrossLibraries(ctx, from, to, move, task)
		}
		if rpcErr != nil {
			return rpcErr
		}
		deleted, rpcErr := from.remove(move.fromPath, move.names)
		if rpcErr != nil {
			return rpcErr
		}
		if _, rpcErr := w.moveTrees(ctx, move); rpcErr != nil {
			return rpcErr
		}
		err := w.publishMove(ctx, from, to, describeDeleted(deleted), toDesc, task)
		if errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		}
		if err != nil {
			return fileWriteFailure("publish cross-library move", err)
		}
		return nil
	}
	return rpcError(520, "Concurrent update")
}

func (w *fileWriter) publishMove(ctx context.Context, from, to *fileTree, fromDesc, toDesc string, task *fileMoveTask) error {
	fromCommit, err := w.prepareCommit(from, fromDesc)
	if err != nil {
		return err
	}
	toCommit, err := w.prepareCommit(to, toDesc)
	if err != nil {
		return err
	}
	return w.publishTransfer(ctx, task, func(conn *sql.Conn) (bool, error) {
		if _, err := publishFileWrite(ctx, conn, to.repo.HeadCommitID, toCommit); err != nil {
			return false, err
		}
		return publishFileWrite(ctx, conn, from.repo.HeadCommitID, fromCommit)
	})
}

type libraryCopy struct {
	ctx                  context.Context
	from, to             *fileTree
	task                 *fileMoveTask
	limits               FileMoveLimits
	sourceKey, targetKey transferKey
}

func (c *libraryCopy) insert(transfer libraryTransfer, quota *UserQuota) (string, *searpc.RPCError) {
	fromDir, rpcErr := c.from.reader.directoryByPath(c.from.repo, c.from.root, strings.Join(transfer.fromPath, "/"))
	if rpcErr != nil {
		return "", rpcError(errGeneral, "Internal error when copy or move")
	}
	if _, rpcErr := c.to.reader.directoryByPath(c.to.repo, c.to.root, strings.Join(transfer.toPath, "/")); rpcErr != nil {
		return "", rpcError(errGeneral, "Internal error when copy or move")
	}
	entries := make([]*fsmgr.SeafDirent, 0, len(transfer.names))
	var total copyTotal
	for _, name := range transfer.names {
		index := findEntry(fromDir.Entries, name)
		if index == -1 {
			return "", rpcError(errGeneral, "Internal error when copy or move")
		}
		entry := fromDir.Entries[index]
		if rpcErr := c.count(entry, &total); rpcErr != nil {
			return "", rpcErr
		}
		entries = append(entries, entry)
	}
	if quota.check(c.ctx, c.to.repo.ID, total.bytes) != 0 {
		return "", rpcError(errGeneral, "Quota is full")
	}
	if c.task != nil {
		c.task.setTotal(total.files)
	}

	copied := make([]*fsmgr.SeafDirent, 0, len(entries))
	for i, entry := range entries {
		next, rpcErr := c.copyEntry(entry, transfer.targets[i])
		if rpcErr != nil {
			return "", rpcErr
		}
		copied = append(copied, next)
	}
	if rpcErr := c.to.insertMoved(transfer.toPath, copied, transfer.replace); rpcErr != nil {
		return "", rpcErr
	}
	return describeTransferTarget(transfer.targets, copied[0].Mode), nil
}

func describeTransferTarget(names []string, mode uint32) string {
	if len(names) > 1 {
		return fmt.Sprintf("Added \"%s\" and %d more files", names[0], len(names)-1)
	}
	if fsmgr.IsDir(mode) {
		return fmt.Sprintf("Added directory \"%s\"", names[0])
	}
	return fmt.Sprintf("Added \"%s\"", names[0])
}

type copyTotal struct {
	files, bytes int64
}

func (c *libraryCopy) count(entry *fsmgr.SeafDirent, total *copyTotal) *searpc.RPCError {
	if err := c.ctx.Err(); err != nil {
		return copyFailure(err)
	}
	if fsmgr.IsDir(entry.Mode) {
		dir, err := c.from.reader.Directory(c.from.repo, entry.ID)
		if err != nil {
			return copyFailure(err)
		}
		for _, child := range dir.Entries {
			if rpcErr := c.count(child, total); rpcErr != nil {
				return rpcErr
			}
		}
		return nil
	}
	if !fsmgr.IsRegular(entry.Mode) {
		return rpcError(errGeneral, "Invalid arguments")
	}
	file, err := c.from.reader.File(c.from.repo, entry.ID)
	if err != nil {
		return copyFailure(err)
	}
	total.files++
	if c.limits.MaxFiles > 0 && total.files > c.limits.MaxFiles {
		return rpcError(errGeneral, "Too many files")
	}
	if file.FileSize > uint64(math.MaxInt64-total.bytes) {
		return rpcError(errGeneral, "Folder or file size is too large")
	}
	total.bytes += int64(file.FileSize)
	if c.limits.MaxBytes > 0 && total.bytes > c.limits.MaxBytes {
		return rpcError(errGeneral, "Folder or file size is too large")
	}
	return nil
}

func (c *libraryCopy) copyEntry(entry *fsmgr.SeafDirent, name string) (*fsmgr.SeafDirent, *searpc.RPCError) {
	if err := c.ctx.Err(); err != nil {
		return nil, copyFailure(err)
	}
	id := entry.ID
	var size int64
	if fsmgr.IsDir(entry.Mode) {
		dir, err := c.from.reader.Directory(c.from.repo, id)
		if err != nil {
			return nil, copyFailure(err)
		}
		copied := make([]*fsmgr.SeafDirent, 0, len(dir.Entries))
		for _, child := range dir.Entries {
			next, rpcErr := c.copyEntry(child, child.Name)
			if rpcErr != nil {
				return nil, rpcErr
			}
			copied = append(copied, next)
		}
		var rpcErr *searpc.RPCError
		id, rpcErr = c.to.save(copied)
		if rpcErr != nil {
			return nil, rpcErr
		}
	} else {
		file, err := c.from.reader.File(c.from.repo, id)
		if err != nil {
			return nil, copyFailure(err)
		}
		size = int64(file.FileSize)
		id, err = c.copyFile(file)
		if err != nil {
			return nil, copyFailure(err)
		}
		if c.task != nil {
			c.task.completeFile()
		}
	}
	return fsmgr.NewDirent(id, name, entry.Mode, entry.Mtime, c.to.user, size), nil
}

func copyFailure(err error) *searpc.RPCError {
	log.Printf("copy SQLite library objects: %v", err)
	return rpcError(errGeneral, "Internal error when copy or move")
}
