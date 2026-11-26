package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"golang.org/x/text/unicode/norm"
)

const (
	invalidImportSource = "Invalid input file"
	errImportName       = 517
	errImportDuplicates = 521
)

type fileImport struct {
	name   string
	path   string
	source *stagedSource
}

type importedFile struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	Size int64  `json:"size"`
}

func (w *fileWriter) postFile(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	values, rpcErr := fileArguments(args, 5)
	if rpcErr != nil {
		return -1, rpcErr
	}
	repoID, stagedPath, parent, name, user := values[0], values[1], values[2], norm.NFC.String(values[3]), values[4]
	if !validWriteName(name) {
		return -1, rpcError(errBadArgs, "Invalid filename")
	}
	parents, rpcErr := importParent(parent)
	if rpcErr != nil {
		return -1, rpcErr
	}
	_, rpcErr = w.importStagedFiles(ctx, repoID, user, parents, []fileImport{{name: name, path: stagedPath}}, false,
		fmt.Sprintf("Added \"%s\"", name))
	if rpcErr != nil {
		if rpcErr.Code == errPathMissing || rpcErr.Code == errImportDuplicates {
			return -1, rpcError(errGeneral, "Failed to post file")
		}
		return -1, rpcErr
	}
	return 0, nil
}

func (w *fileWriter) postMultiFiles(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	if len(args) != 6 {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	values, rpcErr := fileArguments(args[:5], 5)
	if rpcErr != nil {
		return nil, rpcErr
	}
	repoID, parent, namesJSON, pathsJSON, user := values[0], values[1], values[2], values[3], values[4]
	parents, rpcErr := importParent(parent)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var names, paths []*string
	if json.Unmarshal([]byte(namesJSON), &names) != nil || json.Unmarshal([]byte(pathsJSON), &paths) != nil ||
		len(names) == 0 || len(names) != len(paths) {
		return nil, rpcError(errBadArgs, "Invalid files")
	}
	files := make([]fileImport, len(names))
	for i, name := range names {
		if name == nil || paths[i] == nil {
			return nil, rpcError(errBadArgs, "Invalid files")
		}
		normalized := norm.NFC.String(*name)
		if !validWriteName(normalized) {
			return nil, rpcError(errImportName, normalized)
		}
		files[i] = fileImport{name: normalized, path: *paths[i]}
	}
	description := fmt.Sprintf("Added \"%s\".", files[0].name)
	if len(files) > 1 {
		description = fmt.Sprintf("Added \"%s\" and %d more files.", files[0].name, len(files)-1)
	}
	results, rpcErr := w.importStagedFiles(ctx, repoID, user, parents, files, intArgument(args, 5) != 0, description)
	if rpcErr != nil {
		if rpcErr.Code == errPathMissing {
			return nil, rpcError(errInternal, "Failed to put file")
		}
		if rpcErr.Code == errBadArgs && rpcErr.Message == invalidImportSource {
			return nil, rpcError(errGeneral, "Failed to index blocks")
		}
		return nil, rpcErr
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		return nil, fileWriteFailure("encode import results", err)
	}
	return string(encoded), nil
}

func importParent(parent string) ([]string, *searpc.RPCError) {
	parents, rpcErr := fileWritePath(parent)
	if rpcErr != nil || strings.Contains(parent, "//") {
		return nil, rpcError(errBadArgs, "Invalid parent dir")
	}
	return parents, nil
}

func (w *fileWriter) importStagedFiles(ctx context.Context, repoID, user string, parents []string, files []fileImport,
	replace bool, description string) ([]importedFile, *searpc.RPCError) {
	tree, rpcErr := w.writableTree(ctx, repoID, user, true)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var total int64
	for i := range files {
		source, err := w.reader.openStagedSource(files[i].path)
		if err != nil {
			return nil, rpcError(errBadArgs, invalidImportSource)
		}
		defer source.Close()
		files[i].source = source
		if source.stat.Size > math.MaxInt64-total {
			return nil, rpcError(519, "Quota is full")
		}
		total += source.stat.Size
	}

	for attempt := range maxFileWriteRetries {
		if attempt != 0 {
			tree, rpcErr = w.writableTree(ctx, repoID, user, true)
			if rpcErr != nil {
				return nil, rpcErr
			}
		}
		var results []importedFile
		rpcErr = tree.change(parents, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
			delta := total
			if replace {
				delta = replacementImportDelta(dir.Entries, files, total)
			}
			if delta > 0 && w.quota.check(ctx, repoID, delta) != 0 {
				return false, rpcError(519, "Quota is full")
			}
			var err *searpc.RPCError
			results, err = w.applyImports(ctx, tree, dir, files, replace)
			return err == nil, err
		})
		if rpcErr != nil {
			return nil, rpcErr
		}
		current, rpcErr := w.writableTree(ctx, repoID, user, true)
		if rpcErr != nil {
			return nil, rpcErr
		}
		if current.repo.HeadCommitID != tree.repo.HeadCommitID {
			continue
		}
		if tree.root == tree.repo.RootID {
			return results, nil
		}
		commit, err := w.publishStagedImport(ctx, tree, description)
		if errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		}
		if err != nil {
			return nil, fileWriteFailure("publish import commit", err)
		}
		w.reader.PublishRepoUpdate(ctx, repoID, commit, tree.repo.VirtualInfo != nil)
		return results, nil
	}
	return nil, rpcError(520, "Concurrent upload")
}

func replacementImportDelta(entries []*fsmgr.SeafDirent, files []fileImport, total int64) int64 {
	seen := make(map[string]bool, len(files))
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		if seen[file.name] {
			// Only the last replacement of each name survives publication.
			total = max(0, total-file.source.stat.Size)
			continue
		}
		seen[file.name] = true
		if index := findEntry(entries, file.name); index >= 0 && fsmgr.IsRegular(entries[index].Mode) {
			total = max(0, total-max(0, entries[index].Size))
		}
	}
	return total
}

func (w *fileWriter) applyImports(ctx context.Context, tree *fileTree, dir *fsmgr.SeafDir, files []fileImport,
	replace bool) ([]importedFile, *searpc.RPCError) {
	results := make([]importedFile, 0, len(files))
	for _, file := range files {
		name, index := file.name, -1
		if replace {
			index = findEntry(dir.Entries, name)
		} else {
			name = importedName(name, dir.Entries)
			if name == "" {
				return nil, rpcError(errImportDuplicates, "Too many files with same name")
			}
		}
		id, rpcErr := w.indexStagedSource(ctx, tree, file.source)
		if rpcErr != nil {
			return nil, rpcErr
		}
		size := file.source.stat.Size
		results = append(results, importedFile{Name: name, ID: id, Size: size})
		entry := fsmgr.NewDirent(id, name, emptyFileMode, tree.now, tree.user, size)
		if index < 0 {
			dir.Entries = append(dir.Entries, entry)
		} else if old := dir.Entries[index]; old.ID != id || old.Size != size || !fsmgr.IsRegular(old.Mode) {
			dir.Entries[index] = entry
		}
	}
	return results, nil
}

func (w *fileWriter) publishStagedImport(ctx context.Context, tree *fileTree, description string) (string, error) {
	repo := tree.repo
	parent, err := w.reader.Commit(repo.ID, repo.HeadCommitID)
	if err != nil {
		return "", err
	}
	var id string
	_, err = withSQLiteWrite(ctx, w.db, func(conn *sql.Conn) (bool, error) {
		for timestamp := tree.now; ; timestamp++ {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			commit := newFileWriteCommit(tree, description, parent, timestamp)
			// The C hash omits the parent. Replacing the same filename with
			// an earlier version in the same second can revisit a prior root.
			exists, err := w.reader.commits.Exists(repo.ID, commit.CommitID)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			if exists {
				continue
			}
			if err := commitmgr.SaveCanonicalToStore(w.reader.commits, commit); err != nil {
				return false, err
			}
			id = commit.CommitID
			return publishFileWrite(ctx, conn, repo.HeadCommitID, commit)
		}
	})
	return id, err
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
		return "", rpcError(errBadArgs, invalidImportSource)
	}
	id, rpcErr := w.indexLocalFile(ctx, tree, source.File, source.stat.Size)
	if !source.unchanged() {
		return "", rpcError(errBadArgs, invalidImportSource)
	}
	return id, rpcErr
}
