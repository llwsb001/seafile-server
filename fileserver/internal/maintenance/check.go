package maintenance

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

// CheckOptions controls library selection and validation depth.
type CheckOptions struct {
	RepoIDs       []string
	Prefix        *string
	Threads       int
	Shallow       bool
	CheckFileSize bool
	Repair        bool
}

// Check reports repository damage and scans fallback commits. A writable store
// and explicit repair option are required to replace damaged entries.
func (s *Store) Check(ctx context.Context, opts CheckOptions, output io.Writer) error {
	if opts.Repair && !s.write {
		return errors.New("repair requires a writable SQLite store")
	}
	ids := opts.RepoIDs
	if opts.Prefix != nil || len(ids) == 0 {
		var err error
		ids, err = s.RepoIDs(ctx, opts.Prefix)
		if err != nil {
			return err
		}
		slices.Reverse(ids)
	}
	if opts.Threads < -1 {
		fmt.Fprintln(output, "Failed to create check and recover repo thread pool.")
		return nil
	}

	workers := min(max(1, opts.Threads), len(ids))
	if opts.Threads == -1 {
		workers = len(ids)
	}
	var outputMu sync.Mutex
	log := func(format string, args ...any) {
		outputMu.Lock()
		defer outputMu.Unlock()
		fmt.Fprintf(output, format+"\n", args...)
	}
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := range workers {
		go func() {
			defer wait.Done()
			for i := worker; i < len(ids) && ctx.Err() == nil; i += workers {
				checker := repoCheck{
					store: s, opts: opts, log: log, blocks: make(map[string]bool),
					directories: make(map[string]bool),
				}
				checker.check(ctx, ids[i])
			}
		}()
	}
	wait.Wait()
	return ctx.Err()
}

type repoCheck struct {
	store       *Store
	opts        CheckOptions
	log         func(string, ...any)
	repo        Repo
	head        *commitmgr.Commit
	version     int
	truncateAt  int64
	blocks      map[string]bool
	directories map[string]bool
	repair      repairChanges
}

func (c *repoCheck) check(ctx context.Context, id string) {
	c.log("Running fsck for repo %s.", id)
	defer c.log("Fsck finished for repo %.8s.\n", id)

	if !ValidRepoID(id) {
		c.log("Invalid repo id %s.", id)
		return
	}
	repo, err := c.store.Repo(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		c.log("Repo %.8s doesn't exist.", id)
		return
	}
	if err != nil {
		c.log("Failed to get repo %s: %v.", id, err)
		return
	}
	c.repo = repo
	if c.opts.Repair {
		c.repair = repairChanges{at: time.Now().Unix(), blocks: make(map[string]bool)}
	}
	var root *fsmgr.SeafDir
	var selected *commitmgr.Commit
	var checkName string
	if repo.HeadID == "" {
		c.log("Failed to get master branch of repo %.8s.", id)
	} else {
		c.head, err = c.store.Commit(repo, repo.HeadID)
		selected = c.head
		if err != nil {
			c.log("Commit %s is missing", repo.HeadID)
		} else {
			c.version = c.head.Version
			checkName = c.head.RepoName
			root, err = c.directory(c.head.RootID)
			if isObjectIOError(err) {
				return
			}
		}
	}
	reset := root == nil
	if reset {
		c.log("Repo %.8s HEAD commit is damaged, need to restore to an old version.", id)
		selected, root = c.fallback()
		if root == nil {
			return
		}
		if c.head == nil {
			checkName, c.version = selected.RepoName, selected.Version
		}
	}

	c.log("Checking file system integrity of repo %s(%.8s)...", checkName, id)
	if c.opts.CheckFileSize {
		c.truncateAt = c.store.historyCutoff(ctx, repo, time.Now())
	}
	nextRoot, ok := c.checkDir(ctx, root, "/")
	if c.opts.Repair && ok && (reset || nextRoot != root.DirID) {
		c.publishRepair(ctx, selected, nextRoot)
	}
}

func (c *repoCheck) fallback() (*commitmgr.Commit, *fsmgr.SeafDir) {
	c.log("Scanning available commits...")
	commits, err := c.store.candidateCommits(c.repo)
	if err != nil {
		c.log("Failed to scan available commits: %v.", err)
	}
	for _, commit := range commits {
		root, err := c.store.Directory(c.repo, 1, commit.RootID)
		if isObjectIOError(err) {
			break
		}
		if errors.Is(err, os.ErrNotExist) {
			c.log("Dir %s is missing.", commit.RootID)
		}
		if err == nil {
			c.log("Find available commit %.8s(created at %s) for repo %.8s.", commit.CommitID,
				formatMaintenanceTime(commit.Ctime), c.repo.ID)
			return commit, root
		}
	}
	c.log("No available commits for repo %.8s, can't be repaired.", c.repo.ID)
	return nil, nil
}

func newestCommitFirst(a, b *commitmgr.Commit) int {
	// C visits the newest commit first. The fallback order for ties is unspecified.
	if a.Ctime > b.Ctime {
		return -1
	}
	if a.Ctime < b.Ctime {
		return 1
	}
	return 0
}

func formatMaintenanceTime(unix int64) string {
	return time.Unix(unix, 0).Local().Format("2006-01-02 15:04:05")
}

func isObjectIOError(err error) bool {
	var pathErr *os.PathError
	return errors.As(err, &pathErr) && !errors.Is(err, os.ErrNotExist)
}

func (c *repoCheck) directory(id string) (*fsmgr.SeafDir, error) {
	dir, err := c.store.Directory(c.repo, c.version, id)
	if errors.Is(err, os.ErrNotExist) {
		c.log("Dir %s is missing.", id)
	} else if isObjectIOError(err) {
		c.log("Failed to read dir %s:%s.", c.repo.StoreID, id)
	}
	return dir, err
}

func (c *repoCheck) checkDir(ctx context.Context, dir *fsmgr.SeafDir, parent string) (string, bool) {
	c.directories[dir.DirID] = true
	defer delete(c.directories, dir.DirID)

	changed := false
	for _, entry := range dir.Entries {
		if ctx.Err() != nil {
			return "", false
		}
		original := entry.ID
		if fsmgr.IsRegular(entry.Mode) {
			if !c.checkFile(ctx, entry, parent+entry.Name) {
				return "", false
			}
		} else if fsmgr.IsDir(entry.Mode) {
			path := parent + entry.Name + "/"
			subdir, err := c.directory(entry.ID)
			if isObjectIOError(err) {
				return "", false
			}
			if err != nil || c.directories[entry.ID] {
				c.reportDamagedDir(entry, path)
			} else {
				next, ok := c.checkDir(ctx, subdir, path)
				if !ok {
					return "", false
				}
				if c.opts.Repair {
					entry.ID = next
				}
			}
		}
		changed = changed || original != entry.ID
	}
	if !changed {
		return dir.DirID, true
	}
	next, err := fsmgr.WriteCompatibleDirectory(c.store.fs, c.repo.StoreID, c.version, dir.Entries)
	if err != nil {
		c.log("Repo[%.8s] failed to save dir: %v.", c.repo.ID, err)
		return "", false
	}
	return next, true
}

func (c *repoCheck) checkFile(ctx context.Context, entry *fsmgr.SeafDirent, path string) bool {
	file, err := c.store.File(c.repo, c.version, entry.ID)
	if errors.Is(err, os.ErrNotExist) {
		c.log("File %s is missing.", entry.ID)
	} else if isObjectIOError(err) {
		c.log("Failed to read file %s:%s.", c.repo.StoreID, entry.ID)
		return false
	}
	ok := err == nil
	if ok {
		ok, err = c.checkBlocks(file)
		if err != nil {
			c.log("Failed to check blocks for repo[%.8s] file %s(%.8s).", c.repo.ID, path, entry.ID)
			return false
		}
	}
	if !ok {
		c.reportDamagedFile(entry, path)
	} else if c.opts.CheckFileSize && file.FileSize != uint64(entry.Size) {
		c.reportFileSize(ctx, path, entry.ID)
	}
	return true
}

func (c *repoCheck) checkBlocks(file *fsmgr.Seafile) (bool, error) {
	ok := true
	for _, id := range file.BlkIDs {
		if c.repair.blocks[id] {
			ok = false
			continue
		}
		if c.blocks[id] {
			continue
		}
		if !c.store.blockExists(c.repo, id) {
			c.log("Repo[%.8s] block %s:%s is missing.", c.repo.ID, c.repo.StoreID, id)
			ok = false
			continue
		}
		if !c.opts.Shallow {
			digest := sha1.New()
			if err := c.store.ReadBlock(c.repo, id, digest); err != nil {
				if !ok {
					return false, nil
				}
				return false, err
			}
			if hex.EncodeToString(digest.Sum(nil)) != id {
				c.reportDamagedBlock(id)
				ok = false
			}
		}
		// C checks an existing block at most once per repository.
		c.blocks[id] = true
	}
	return ok, nil
}
