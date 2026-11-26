package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"
)

// VerifyGC runs the read-only seafserv-gc integrity check.
func (s *Store) VerifyGC(ctx context.Context, explicit []string, output io.Writer) (int, error) {
	ids, err := s.gcRepoIDs(ctx, explicit, nil)
	if err != nil {
		return 1, err
	}
	now := time.Now()
	exit := 0
	for _, id := range ids {
		repo, err := s.Repo(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(output, "Start to verify repo %s\n", id)
		if _, err := s.Commit(repo, repo.HeadID); err != nil {
			fmt.Fprintf(output, "Repo %s is corrupted.\n", id)
			continue
		}
		if err := s.verifyGCRepo(ctx, repo, now, output); err != nil {
			fmt.Fprintf(output, "Failed to verify repo %s: %v\n", id, err)
			exit = 255
			if ctx.Err() != nil {
				return 1, ctx.Err()
			}
			continue
		}
		fmt.Fprintf(output, "Verify repo %s success\n", id)
		exit = 0
	}
	return exit, nil
}

func (s *Store) verifyGCRepo(ctx context.Context, repo Repo, now time.Time, output io.Writer) error {
	cutoff, err := s.readHistoryCutoff(ctx, repo, now)
	if err != nil {
		return err
	}
	heads, err := s.gcBranches(ctx, repo)
	if err != nil {
		return err
	}
	keep := newReachableObjects()
	for _, head := range heads {
		if err := s.collectHistory(ctx, repo, head, cutoff, keep); err != nil {
			return err
		}
	}
	for _, block := range sortedStringKeys(keep.blocks) {
		if !s.blockExists(repo, block) {
			fmt.Fprintf(output, "Block %s is missing.\n", block)
		}
	}
	if repo.ID != repo.StoreID {
		return nil
	}
	views, err := s.gcVirtualRepos(ctx, repo)
	if err != nil {
		return err
	}
	for _, view := range views {
		if err := s.collectHistory(ctx, repo, view.base, cutoff, keep); err != nil {
			return fmt.Errorf("virtual library %s base: %w", view.repo.ID, err)
		}
	}
	return nil
}
