package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

type GCOptions struct {
	RepoIDs     []string
	Prefix      *string
	Verbose     bool
	Threads     int
	RemoveFS    bool
	DeletedOnly bool
}

type gcPlan struct {
	repo       Repo
	reachable  reachableObjects
	cutoffs    map[string]int64
	unused     []string
	unusedFS   []string
	blockCount int
	fsCount    int
	skip       string
}

type reachableObjects struct {
	fs     map[string]struct{}
	blocks map[string]struct{}
}

func newReachableObjects() reachableObjects {
	return reachableObjects{fs: make(map[string]struct{}), blocks: make(map[string]struct{})}
}

// DryRunGC scans offline SQLite stores and reports exact block candidates.
func (s *Store) DryRunGC(ctx context.Context, options GCOptions, output io.Writer) error {
	fmt.Fprintln(output, "Database is SQLite, use offline GC.")
	ids, err := s.gcRepoIDs(ctx, options.RepoIDs, options.Prefix)
	if err != nil {
		return err
	}
	now := time.Now()
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := min(max(1, options.Threads), len(ids))
	var wait sync.WaitGroup
	var writing sync.Mutex
	var scanError error
	wait.Add(workers)
	for worker := range workers {
		go func() {
			defer wait.Done()
			for i := worker; i < len(ids) && scanCtx.Err() == nil; i += workers {
				var report strings.Builder
				err := s.dryRunGCRepo(scanCtx, ids[i], now, options, &report)
				writing.Lock()
				if _, writeErr := io.WriteString(output, report.String()); err == nil {
					err = writeErr
				}
				if err != nil && scanError == nil {
					scanError = err
					cancel()
				}
				writing.Unlock()
			}
		}()
	}
	wait.Wait()
	if err := errors.Join(scanError, ctx.Err()); err != nil {
		return err
	}
	if options.Prefix != nil || len(options.RepoIDs) == 0 {
		if err := s.CollectGCDeleted(ctx, true, output); err != nil {
			return err
		}
	}
	fmt.Fprintln(output, "=== GC is finished ===")
	return nil
}

func (s *Store) dryRunGCRepo(ctx context.Context, id string, now time.Time, opts GCOptions, output io.Writer) error {
	repo, err := s.Repo(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if repo.ID != repo.StoreID {
		return nil // C collects a shared store under its origin.
	}
	if _, err := s.Commit(repo, repo.HeadID); err != nil {
		fmt.Fprintf(output, "Repo %s is damaged, skip GC. %v\n", id, err)
		return nil
	}
	plan, err := s.planGC(ctx, repo, now, opts.RemoveFS)
	if err != nil {
		fmt.Fprintf(output, "Repo %s is damaged, skip GC. %v\n", id, err)
		return ctx.Err()
	}
	if plan.skip != "" {
		fmt.Fprintln(output, plan.skip)
		return nil
	}
	fmt.Fprintf(output, "Scanning unused blocks for repo %.8s.\n", id)
	if opts.Verbose {
		for _, repoID := range sortedStringKeys(plan.cutoffs) {
			fmt.Fprintf(output, "History cutoff for repo %s: %d.\n", repoID, plan.cutoffs[repoID])
		}
		for _, block := range plan.unused {
			fmt.Fprintf(output, "Unused block %s in repo %s.\n", block, plan.repo.ID)
		}
		for _, id := range plan.unusedFS {
			fmt.Fprintf(output, "Unused FS object %s in repo %s.\n", id, plan.repo.ID)
		}
	}
	fmt.Fprintf(output, "GC finished for repo %.8s. %d blocks total, about %d reachable blocks, %d blocks can be removed.",
		id, plan.blockCount, len(plan.reachable.blocks), len(plan.unused))
	if opts.RemoveFS {
		fmt.Fprintf(output, " %d fs total, about %d reachable fs, %d fs can be removed.",
			plan.fsCount, len(plan.reachable.fs), len(plan.unusedFS))
	}
	fmt.Fprintln(output)
	return nil
}

func sortedStringKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (s *Store) gcRepoIDs(ctx context.Context, explicit []string, prefix *string) ([]string, error) {
	if prefix != nil || len(explicit) == 0 {
		return s.RepoIDs(ctx, prefix)
	}
	return explicit, nil
}

func (s *Store) planGC(ctx context.Context, repo Repo, now time.Time, includeFS bool) (gcPlan, error) {
	plan := gcPlan{repo: repo}
	cutoff, err := s.readHistoryCutoff(ctx, repo, now)
	if err != nil {
		return plan, err
	}
	if cutoff < 0 {
		plan.skip = fmt.Sprintf("Repo %.8s keeps all history, skip GC.", repo.ID)
		return plan, nil
	}

	blocks, err := s.objectIDs("blocks", repo.StoreID)
	if err != nil {
		return plan, err
	}
	if len(blocks) == 0 {
		plan.skip = fmt.Sprintf("No blocks for repo %.8s, skip GC.", repo.ID)
		return plan, nil
	}
	plan.blockCount = len(blocks)
	plan.reachable = newReachableObjects()
	plan.cutoffs = make(map[string]int64)
	views, err := s.gcVirtualRepos(ctx, repo)
	if err != nil {
		return plan, err
	}
	if err := s.collectActiveHistory(ctx, repo, cutoff, plan.reachable, plan.cutoffs); err != nil {
		return plan, err
	}
	for _, view := range views {
		viewCutoff, err := s.readHistoryCutoff(ctx, view.repo, now)
		if err != nil {
			return plan, err
		}
		if err := s.collectActiveHistory(ctx, view.repo, viewCutoff, plan.reachable, plan.cutoffs); err != nil {
			return plan, err
		}
		if err := s.collectHistory(ctx, repo, view.base, 0, plan.reachable); err != nil {
			return plan, fmt.Errorf("virtual library %s base: %w", view.repo.ID, err)
		}
	}

	for _, block := range blocks {
		if _, keep := plan.reachable.blocks[block]; !keep {
			plan.unused = append(plan.unused, block)
		}
	}
	if includeFS {
		files, err := s.objectIDs("fs", repo.StoreID)
		if err != nil {
			return plan, err
		}
		plan.fsCount = len(files)
		for _, id := range files {
			if _, keep := plan.reachable.fs[id]; !keep {
				plan.unusedFS = append(plan.unusedFS, id)
			}
		}
	}
	return plan, nil
}

func (s *Store) collectActiveHistory(ctx context.Context, repo Repo, cutoff int64, keep reachableObjects, cutoffs map[string]int64) error {
	head, err := s.Commit(repo, repo.HeadID)
	if err != nil {
		return fmt.Errorf("load master commit %s for %s: %w", repo.HeadID, repo.ID, err)
	}
	branches, err := s.gcBranches(ctx, repo)
	if err != nil {
		return err
	}
	for _, branch := range branches {
		if err := s.collectHistory(ctx, repo, branch, cutoff, keep); err != nil {
			return err
		}
	}
	// A partially restored store can still have a second recoverable head.
	var trashHead sql.NullString
	err = s.db.QueryRowContext(ctx, "SELECT head_id FROM RepoTrash WHERE repo_id = ?", repo.ID).Scan(&trashHead)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if trashHead.Valid && trashHead.String != repo.HeadID {
		if err := s.collectHistory(ctx, repo, trashHead.String, -1, keep); err != nil {
			return fmt.Errorf("recoverable trash head: %w", err)
		}
	}
	if cutoff == 0 {
		cutoff = head.Ctime
	}
	if cutoff > 0 {
		cutoffs[repo.ID] = cutoff
	}
	return nil
}

func (s *Store) gcBranches(ctx context.Context, repo Repo) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT commit_id FROM Branch WHERE repo_id = ? ORDER BY name", repo.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var heads []string
	for rows.Next() {
		var head string
		if err := rows.Scan(&head); err != nil {
			return nil, err
		}
		heads = append(heads, head)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(heads) == 0 {
		return nil, fmt.Errorf("failed to get branch list of repo %s", repo.ID)
	}
	return heads, nil
}

type gcView struct {
	repo Repo
	base string
}

func (s *Store) gcVirtualRepos(ctx context.Context, origin Repo) ([]gcView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.repo_id, v.base_commit, COALESCE(b.commit_id, '')
		FROM VirtualRepo v LEFT JOIN Branch b ON v.repo_id = b.repo_id AND b.name = 'master'
		WHERE v.origin_repo = ? ORDER BY v.repo_id`, origin.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var views []gcView
	for rows.Next() {
		view := gcView{repo: Repo{StoreID: origin.StoreID}}
		if err := rows.Scan(&view.repo.ID, &view.base, &view.repo.HeadID); err != nil {
			return nil, err
		}
		if !ValidRepoID(view.repo.ID) {
			return nil, fmt.Errorf("invalid virtual repository %q", view.repo.ID)
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

func (s *Store) collectHistory(ctx context.Context, repo Repo, head string, cutoff int64, keep reachableObjects) error {
	pending := []string{head}
	seen := make(map[string]bool)
	for len(pending) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		commit, err := s.Commit(repo, id)
		if err != nil {
			return fmt.Errorf("load commit %s/%s: %w", repo.ID, id, err)
		}
		if commit.RepoID != repo.ID || commit.CommitID != id {
			return fmt.Errorf("invalid commit reference %s/%s", repo.ID, id)
		}
		if err := s.collectDirectory(ctx, repo, commit.Version, commit.RootID, keep); err != nil {
			return err
		}
		// The first older commit on each parent path is still used by file trash.
		if cutoff == 0 || (cutoff > 0 && commit.Ctime < cutoff && id != head) {
			continue
		}
		for _, parent := range []string{commit.ParentID.ValueOrZero(), commit.SecondParentID.ValueOrZero()} {
			if parent != "" {
				pending = append(pending, parent)
			}
		}
	}
	return nil
}

func (s *Store) collectDirectory(ctx context.Context, repo Repo, version int, id string, keep reachableObjects) error {
	if _, exists := keep.fs[id]; exists || id == fsmgr.EmptySha1 {
		return nil
	}
	dir, err := s.Directory(repo, version, id)
	if err != nil {
		return err
	}
	keep.fs[id] = struct{}{}
	for _, entry := range dir.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := keep.fs[entry.ID]; exists || entry.ID == fsmgr.EmptySha1 {
			continue
		}
		if fsmgr.IsDir(entry.Mode) {
			if err := s.collectDirectory(ctx, repo, version, entry.ID, keep); err != nil {
				return err
			}
		} else if fsmgr.IsRegular(entry.Mode) {
			file, err := s.File(repo, version, entry.ID)
			if err != nil {
				return err
			}
			keep.fs[entry.ID] = struct{}{}
			for _, block := range file.BlkIDs {
				if !utils.IsObjectIDValid(block) {
					return fmt.Errorf("file %s references invalid block %q", entry.ID, block)
				}
				keep.blocks[block] = struct{}{}
			}
		}
	}
	return nil
}

func (s *Store) objectIDs(kind, store string) ([]string, error) {
	root := filepath.Join(s.dataDir, "storage", kind, store)
	shards, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, shard := range shards {
		if len(shard.Name()) != 2 || !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if id := shard.Name() + file.Name(); utils.IsObjectIDValid(id) && file.Type().IsRegular() {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}
