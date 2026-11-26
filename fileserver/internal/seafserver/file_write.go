package seafserver

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"golang.org/x/text/unicode/norm"
)

const (
	dirMode             = 0040000
	emptyFileMode       = 0100644
	errRepoAuth         = 508
	errRepoLocked       = 514
	maxFileWriteRetries = 3
)

type fileWriter struct {
	reader      *Reader
	db          *sql.DB
	permissions *permissionReader
	quota       *UserQuota
	moves       *fileMoveQueue
	limits      FileMoveLimits
}

// FileWriteRPCHandlers exposes SQLite library writes on the private core socket.
func (r *Reader) FileWriteRPCHandlers(ctx context.Context, db *sql.DB, identity *Identity, cloudMode bool, quota *UserQuota, limits FileMoveLimits) (map[string]searpc.Handler, func()) {
	writer := &fileWriter{reader: r, db: db,
		permissions: &permissionReader{db: r.db, identity: identity, cloudMode: cloudMode},
		quota:       quota, moves: newFileMoveQueue(ctx), limits: limits,
	}
	return map[string]searpc.Handler{
		"seafile_post_empty_file":    writer.edit(4, createFileEntry(false)),
		"seafile_post_dir":           writer.edit(4, createFileEntry(true)),
		"seafile_mkdir_with_parents": writer.edit(4, mkdirWithParents),
		"seafile_del_file":           writer.edit(4, deleteFiles),
		"seafile_batch_del_files":    writer.edit(3, batchDeleteFiles),
		"seafile_rename_file":        writer.edit(5, renameFile),
		"seafile_move_file":          writer.moveFiles,
		"seafile_copy_file":          writer.copyFiles,
		"get_copy_task":              writer.moves.get,
		"cancel_copy_task":           writer.moves.cancel,
	}, writer.moves.close
}

type fileEdit func(*fileTree, []string) (string, *searpc.RPCError)

func fileArguments(args []json.RawMessage, count int) ([]string, *searpc.RPCError) {
	if len(args) != count {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	values := make([]string, count)
	for i := range values {
		value := stringArgument(args, i)
		if value == nil {
			return nil, rpcError(errBadArgs, "Argument should not be null")
		}
		values[i] = *value
	}
	return values, nil
}

func (w *fileWriter) edit(count int, edit fileEdit) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		values, rpcErr := fileArguments(args, count)
		if rpcErr != nil {
			return -1, rpcErr
		}
		if rpcErr := w.update(ctx, values[0], values[count-1], values, edit); rpcErr != nil {
			return -1, rpcErr
		}
		return 0, nil
	}
}

func (w *fileWriter) update(ctx context.Context, repoID, user string, values []string, edit fileEdit) *searpc.RPCError {
	for range maxFileWriteRetries {
		tree, rpcErr := w.writableTree(ctx, repoID, user, true)
		if rpcErr != nil {
			return rpcErr
		}
		description, rpcErr := edit(tree, values)
		if rpcErr != nil || tree.root == tree.repo.RootID {
			return rpcErr
		}
		if err := w.publish(ctx, tree, description); errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		} else if err != nil {
			return fileWriteFailure("publish file write", err)
		}
		return nil
	}
	return rpcError(520, "Concurrent update")
}

func (w *fileWriter) writableTree(ctx context.Context, repoID, user string, allowVirtual bool) (*fileTree, *searpc.RPCError) {
	if !validRepoID(repoID) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	repo, err := w.reader.Repo(ctx, repoID)
	if err != nil {
		return nil, rpcError(errBadArgs, "Invalid repo")
	}
	if repo.IsCorrupted || repo.Version != 1 || (repo.VirtualInfo != nil && !allowVirtual) {
		return nil, rpcError(errBadArgs, "File RPC requires an ordinary version 1 library")
	}
	permission, err := w.permissions.check(ctx, repoID, user)
	if err != nil {
		return nil, fileWriteFailure("check file write permission", err)
	}
	if permission == nil || *permission != "rw" {
		return nil, rpcError(errRepoAuth, "Permission denied")
	}
	if rpcErr := w.checkWriteStatus(ctx, repoID); rpcErr != nil {
		return nil, rpcErr
	}
	if repo.VirtualInfo != nil {
		if rpcErr := w.checkWriteStatus(ctx, repo.VirtualInfo.OriginRepoID); rpcErr != nil {
			return nil, rpcErr
		}
	}
	return &fileTree{reader: w.reader, repo: repo, root: repo.RootID, user: user, now: time.Now().Unix()}, nil
}

func (w *fileWriter) checkWriteStatus(ctx context.Context, repoID string) *searpc.RPCError {
	status, err := repomgr.LoadRepoStatus(ctx, w.reader.db, repoID)
	if err != nil {
		return fileWriteFailure("read file write status", err)
	}
	if status != -1 && status != repomgr.RepoStatusNormal {
		return rpcError(errRepoLocked, "Repository is read-only")
	}
	return nil
}

func (w *fileWriter) publish(ctx context.Context, tree *fileTree, description string) error {
	commit, err := w.prepareCommit(tree, description)
	if err != nil {
		return err
	}
	_, err = withSQLiteWrite(ctx, w.db, func(conn *sql.Conn) (bool, error) {
		return publishFileWrite(ctx, conn, tree.repo.HeadCommitID, commit)
	})
	return err
}

func (w *fileWriter) prepareCommit(tree *fileTree, description string) (*commitmgr.Commit, error) {
	repo := tree.repo
	parent, err := w.reader.Commit(repo.ID, repo.HeadCommitID)
	if err != nil {
		return nil, err
	}
	commit := newInitialRepoCommit(repo.ID, tree.root, repo.Name, repo.Desc, description, tree.user, repo.Version, tree.now)
	commit.ParentID.SetValid(parent.CommitID)
	commit.Encrypted, commit.EncVersion = parent.Encrypted, parent.EncVersion
	commit.Magic, commit.RandomKey, commit.Salt = parent.Magic, parent.RandomKey, parent.Salt
	commit.PwdHash, commit.PwdHashAlgo, commit.PwdHashParams = parent.PwdHash, parent.PwdHashAlgo, parent.PwdHashParams
	commit.NoLocalHistory, commit.Repaired = parent.NoLocalHistory, parent.Repaired
	var payload bytes.Buffer
	if err := commit.ToData(&payload); err != nil {
		return nil, err
	}
	if err := w.reader.commits.Write(repo.ID, commit.CommitID, &payload, true); err != nil {
		return nil, err
	}
	return commit, nil
}

func fileWriteFailure(action string, err error) *searpc.RPCError {
	log.Printf("%s: %v", action, err)
	return rpcError(errGeneral, "File write failed")
}

type fileTree struct {
	reader *Reader
	repo   *repomgr.Repo
	root   string
	user   string
	now    int64
}

type dirEdit func(*fsmgr.SeafDir) (bool, *searpc.RPCError)

func (t *fileTree) change(path []string, updateMtime bool, edit dirEdit) *searpc.RPCError {
	id, err := t.rewrite(t.root, path, updateMtime, edit)
	if err == nil {
		t.root = id
	}
	return err
}

func (t *fileTree) rewrite(id string, path []string, updateMtime bool, edit dirEdit) (string, *searpc.RPCError) {
	dir, err := t.reader.Directory(t.repo, id)
	if err != nil {
		return "", fileWriteFailure("read directory to write", err)
	}
	if len(path) == 0 {
		changed, rpcErr := edit(dir)
		if rpcErr != nil || !changed {
			return id, rpcErr
		}
		return t.save(dir.Entries)
	}
	index := findEntry(dir.Entries, path[0])
	if index == -1 || !fsmgr.IsDir(dir.Entries[index].Mode) {
		return "", rpcError(errPathMissing, "Parent directory does not exist")
	}
	entry := dir.Entries[index]
	child, rpcErr := t.rewrite(entry.ID, path[1:], updateMtime, edit)
	if rpcErr != nil || child == entry.ID {
		return id, rpcErr
	}
	entry.ID = child
	if updateMtime {
		entry.Mtime = t.now
	}
	return t.save(dir.Entries)
}

func (t *fileTree) save(entries []*fsmgr.SeafDirent) (string, *searpc.RPCError) {
	slices.SortFunc(entries, func(left, right *fsmgr.SeafDirent) int { return cmp.Compare(right.Name, left.Name) })
	dir, err := fsmgr.NewSeafdir(1, entries)
	if err != nil {
		return "", fileWriteFailure("encode directory", err)
	}
	if dir.DirID == fsmgr.EmptySha1 {
		return dir.DirID, nil
	}
	var data bytes.Buffer
	if err := dir.ToData(&data); err != nil {
		return "", fileWriteFailure("compress directory", err)
	}
	if err := t.reader.fs.Write(t.repo.StoreID, dir.DirID, &data, true); err != nil {
		return "", fileWriteFailure("write directory", err)
	}
	return dir.DirID, nil
}

func findEntry(entries []*fsmgr.SeafDirent, name string) int {
	return slices.IndexFunc(entries, func(entry *fsmgr.SeafDirent) bool { return entry.Name == name })
}

func validWriteName(name string) bool {
	return len(name) > 0 && len(name) < 256 && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\\x00") && utf8.ValidString(name)
}

func fileWritePath(value string) ([]string, *searpc.RPCError) {
	if len(value) >= 4096 {
		return nil, rpcError(errBadArgs, "Invalid path")
	}
	elements := strings.FieldsFunc(norm.NFC.String(value), func(r rune) bool { return r == '/' })
	for _, name := range elements {
		if !validWriteName(name) {
			return nil, rpcError(errBadArgs, "Invalid path")
		}
	}
	return elements, nil
}

func createFileEntry(directory bool) fileEdit {
	return func(t *fileTree, args []string) (string, *searpc.RPCError) {
		parent, rpcErr := fileWritePath(args[1])
		if rpcErr != nil {
			return "", rpcErr
		}
		name, kind, mode := norm.NFC.String(args[2]), "file", uint32(emptyFileMode)
		description := fmt.Sprintf("Added \"%s\"", name)
		if directory {
			kind, mode = "dir", dirMode
			description = fmt.Sprintf("Added directory \"%s\"", name)
		}
		if !validWriteName(name) {
			return "", rpcError(errBadArgs, "Invalid "+kind+" name")
		}
		rpcErr = t.change(parent, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
			if findEntry(dir.Entries, name) >= 0 {
				return false, rpcError(errBadArgs, "file already exists")
			}
			dir.Entries = append(dir.Entries, fsmgr.NewDirent("", name, mode, t.now, t.user, 0))
			return true, nil
		})
		if rpcErr != nil && rpcErr.Code == errPathMissing {
			rpcErr = rpcError(errGeneral, "Failed to put dir")
		}
		return description, rpcErr
	}
}

func mkdirWithParents(t *fileTree, args []string) (string, *searpc.RPCError) {
	if strings.HasPrefix(args[2], "/") {
		return "", rpcError(errBadArgs, "Invalid relative path")
	}
	parent, rpcErr := fileWritePath(args[1])
	if rpcErr != nil {
		return "", rpcErr
	}
	folders, rpcErr := fileWritePath(args[2])
	if rpcErr != nil {
		return "", rpcError(errBadArgs, "Invalid dir name")
	}
	rpcErr = t.makeParents(parent, folders)
	return fmt.Sprintf("Added directory \"%s\"", strings.TrimRight(norm.NFC.String(args[2]), "/")), rpcErr
}

func (t *fileTree) makeParents(parent, folders []string) *searpc.RPCError {
	for _, name := range folders {
		rpcErr := t.change(parent, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
			index := findEntry(dir.Entries, name)
			if index >= 0 {
				if !fsmgr.IsDir(dir.Entries[index].Mode) {
					return false, rpcError(errGeneral, "Failed to put dir")
				}
				return false, nil
			}
			dir.Entries = append(dir.Entries, fsmgr.NewDirent("", name, dirMode, t.now, "", 0))
			return true, nil
		})
		if rpcErr != nil {
			if rpcErr.Code == errPathMissing {
				return rpcError(errGeneral, "Failed to put dir")
			}
			return rpcErr
		}
		parent = append(parent, name)
	}
	return nil
}

func renameFile(t *fileTree, args []string) (string, *searpc.RPCError) {
	parent, rpcErr := fileWritePath(args[1])
	if rpcErr != nil {
		return "", rpcErr
	}
	oldName, newName := norm.NFC.String(args[2]), norm.NFC.String(args[3])
	if !validWriteName(oldName) || !validWriteName(newName) {
		return "", rpcError(errBadArgs, "Invalid filename")
	}
	if oldName == newName {
		return "", nil
	}
	description := fmt.Sprintf("Renamed \"%s\"", oldName)
	rpcErr = t.change(parent, false, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
		index := findEntry(dir.Entries, oldName)
		if index == -1 {
			return false, rpcError(errBadArgs, "file does not exist")
		}
		if findEntry(dir.Entries, newName) >= 0 {
			return false, rpcError(errBadArgs, "file already exists")
		}
		entry := dir.Entries[index]
		entry.Name = newName
		if fsmgr.IsDir(entry.Mode) {
			description = fmt.Sprintf("Renamed directory \"%s\"", oldName)
		}
		return true, nil
	})
	if rpcErr != nil && rpcErr.Code == errPathMissing {
		rpcErr = rpcError(errBadArgs, "file does not exist")
	}
	return description, rpcErr
}
