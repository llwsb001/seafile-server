package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

// Export scans commit objects and exports the newest readable plain version.
// It works with the object stores alone, without repository database metadata.
func Export(ctx context.Context, dataDir, configDir, destination string, ids []string, output io.Writer) error {
	if _, err := loadMaintenanceConfig(dataDir, configDir); err != nil {
		return err
	}
	exporter := repoExport{
		objects:   openObjectStores(dataDir),
		encrypted: make(map[string]string),
		log: func(format string, args ...any) {
			fmt.Fprintf(output, format+"\n", args...)
		},
	}
	destination, err := prepareExportDirectory(dataDir, destination)
	if err != nil {
		exporter.log("%v", err)
		return nil
	}
	if len(ids) == 0 {
		commitPath := filepath.Join(dataDir, "storage", "commits")
		entries, err := os.ReadDir(commitPath)
		if err != nil {
			exporter.log("Open dir %s failed: %v.", commitPath, err)
			return nil
		}
		for _, entry := range entries {
			ids = append(ids, entry.Name())
		}
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !ValidRepoID(id) {
			exporter.log("Invalid repo id: %s.", id)
			continue
		}
		exporter.repo(ctx, id, destination)
	}
	if len(exporter.encrypted) != 0 {
		exporter.log("The following repos are encrypted and are not exported:")
		ids := make([]string, 0, len(exporter.encrypted))
		for id := range exporter.encrypted {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			exporter.log("%s(%s)", id, exporter.encrypted[id])
		}
	}
	return ctx.Err()
}

func prepareExportDirectory(dataDir, destination string) (string, error) {
	if destination == "" {
		return "", errors.New("empty export directory")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(destination)
	exists := err == nil
	if errors.Is(err, os.ErrNotExist) {
		var parent string
		parent, err = filepath.EvalSymlinks(filepath.Dir(destination))
		target = filepath.Join(parent, filepath.Base(destination))
	}
	if err != nil {
		return "", fmt.Errorf("Mkdir %s failed: %w", destination, err)
	}
	for _, path := range []string{dataDir, filepath.Join(dataDir, "storage"),
		filepath.Join(dataDir, "storage", "commits"), filepath.Join(dataDir, "storage", "fs"),
		filepath.Join(dataDir, "storage", "blocks")} {
		source, err := canonicalPath(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(source, target)
		if err != nil {
			return "", err
		}
		if filepath.IsLocal(rel) {
			return "", fmt.Errorf("export directory is inside the source data: %s", destination)
		}
	}
	if !exists {
		if err := os.Mkdir(target, 0o777); err != nil {
			return "", fmt.Errorf("Mkdir %s failed: %w", destination, err)
		}
	} else {
		info, err := os.Stat(target)
		if err != nil {
			return "", fmt.Errorf("Stat path: %s failed: %w", destination, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("%s already exist, but it is not a directory", destination)
		}
	}
	return target, nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

type repoExport struct {
	objects   *objectStores
	encrypted map[string]string
	log       func(string, ...any)
}

func (e *repoExport) repo(ctx context.Context, id, destination string) {
	// The C export mode walks the physical repository directory, even if the
	// logical library was removed from SQLite or shares an origin store.
	repo := Repo{ID: id, StoreID: id}
	e.log("Scanning available commits for repo %s...", id)
	commit, root := e.availableCommit(repo)
	if commit == nil {
		return
	}
	stamp := formatMaintenanceTime(commit.Ctime)
	if commit.Encrypted == "true" {
		e.log("Find available commit %.8s(created at %s), but the repo is encrypted and has not been exported.",
			commit.CommitID, stamp)
		e.encrypted[id] = commit.RepoName
		return
	}
	if commit.RootID == fsmgr.EmptySha1 {
		e.log("Find available commit %.8s(created at %s), which is an empty commit, will export an empty dir.", commit.CommitID, stamp)
	} else {
		e.log("Find available commit %.8s(created at %s), will export files from it.", commit.CommitID, stamp)
	}
	e.log("Start to export files for repo %.8s(%s).", id, commit.RepoName)
	creator := strings.Map(func(ch rune) rune {
		if ch >= 'A' && ch <= 'Z' {
			return ch + 'a' - 'A'
		}
		return ch
	}, commit.CreatorName)
	if creator == "" {
		creator = "(null)"
	}
	dirname := fmt.Sprintf("%.8s_%s_%s", id, commit.RepoName, creator)
	if !safeExportName(dirname) {
		e.log("Invalid export directory name %q.", dirname)
		return
	}
	dir := filepath.Join(destination, dirname)
	if err := os.Mkdir(dir, 0o777); err != nil {
		e.log("Failed to create export dir %s: %v, export failed.", dir, err)
		return
	}
	e.directory(ctx, repo, root, dir, map[string]bool{})
	if ctx.Err() == nil {
		e.log("Finish exporting files for repo %.8s.\n", id)
	}
}

func (e *repoExport) availableCommit(repo Repo) (*commitmgr.Commit, *fsmgr.SeafDir) {
	commits, err := e.objects.candidateCommits(repo)
	if err != nil {
		e.log("Failed to scan available commits: %v.", err)
	}
	for _, commit := range commits {
		dir, err := e.objects.Directory(repo, 1, commit.RootID)
		if err == nil {
			return commit, dir
		}
		if isObjectIOError(err) {
			e.log("Failed to read dir %s:%s.", repo.StoreID, commit.RootID)
			return nil, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			e.log("Dir %s is missing.", commit.RootID)
		}
	}
	e.log("No available commits for repo %.8s, export failed.\n", repo.ID)
	return nil, nil
}

func safeExportName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		filepath.IsLocal(name) && filepath.Base(name) == name && !strings.ContainsRune(name, 0)
}

func (e *repoExport) directory(ctx context.Context, repo Repo, dir *fsmgr.SeafDir, parent string, ancestors map[string]bool) {
	ancestors[dir.DirID] = true
	defer delete(ancestors, dir.DirID)
	for _, entry := range dir.Entries {
		if ctx.Err() != nil {
			return
		}
		if !safeExportName(entry.Name) {
			e.log("Invalid export entry name %q.", entry.Name)
			continue
		}
		path := filepath.Join(parent, entry.Name)
		if fsmgr.IsRegular(entry.Mode) {
			e.file(repo, entry, path)
		} else if fsmgr.IsDir(entry.Mode) {
			if err := os.Mkdir(path, 0o777); err != nil {
				e.log("Failed to mkdir %s: %v.", path, err)
				continue
			}
			e.log("Export dir %s.", path)
			child, err := fsmgr.ReadSeafdir(e.objects.fs, repo.StoreID, entry.ID, 1)
			if err == nil && !ancestors[entry.ID] {
				e.directory(ctx, repo, child, path, ancestors)
			}
		}
	}
}

func (e *repoExport) file(repo Repo, entry *fsmgr.SeafDirent, path string) {
	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		e.log("Open file %s failed: %v.", path, err)
		return
	}
	err = errors.Join(e.fileContents(repo, entry, output), output.Close())
	if err != nil {
		if err := os.Remove(path); err != nil {
			e.log("Failed to delete file %s: %v.", path, err)
		}
		e.log("Failed to export file %s.", path)
		return
	}
	e.log("Export file %s.", path)
}

func (e *repoExport) fileContents(repo Repo, entry *fsmgr.SeafDirent, output *os.File) error {
	file, err := fsmgr.ReadSeafile(e.objects.fs, repo.StoreID, entry.ID, 1)
	if err != nil {
		return err
	}
	for _, id := range file.BlkIDs {
		if err := e.objects.ReadBlock(repo, id, output); err != nil {
			return err
		}
	}
	if len(file.BlkIDs) != 0 {
		mtime := time.Unix(entry.Mtime, 0)
		if err := os.Chtimes(output.Name(), mtime, mtime); err != nil {
			e.log("Current file (%s) lose its mtime.", output.Name())
		}
	}
	return nil
}
