package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	log "github.com/sirupsen/logrus"
)

func runRPCFileWrites(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		dbCtx, cancel := context.WithTimeout(ctx, option.DBOpTimeout)
		err := seafserver.EnsureFileWriteQueue(dbCtx, seafileDB)
		cancel()
		if err == nil {
			break
		}
		if ctx.Err() == nil {
			log.Warnf("initialize SQLite file write work: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
	for {
		if err := processRPCFileWrites(ctx); err != nil && ctx.Err() == nil {
			log.Warnf("process SQLite file write work: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func processRPCFileWrites(ctx context.Context) error {
	dbCtx, cancel := context.WithTimeout(ctx, option.DBOpTimeout)
	jobs, err := seafserver.PendingFileWrites(dbCtx, seafileDB)
	cancel()
	if err != nil {
		return err
	}
	var failed error
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := processRPCFileWrite(ctx, job); err != nil {
			failed = errors.Join(failed, fmt.Errorf("library %s: %w", job.RepoID, err))
		}
	}
	return failed
}

func processRPCFileWrite(ctx context.Context, job seafserver.FileWriteJob) error {
	if !job.Deleted {
		virtual, err := repomgr.GetVirtualRepoInfo(job.RepoID)
		if err != nil {
			return err
		}
		if err := mergeVirtualFamily(job.RepoID, ""); err != nil {
			return err
		}
		origin := job.RepoID
		if virtual != nil {
			origin = virtual.OriginRepoID
			if err := mergeVirtualFamily(origin, job.RepoID); err != nil {
				return err
			}
		}
		views, err := repomgr.GetVirtualRepoIDsByOrigin(origin)
		if err != nil {
			return err
		}
		for _, id := range append(views, origin) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := computeRepoSize(id); err != nil {
				return err
			}
		}
	}
	dbCtx, cancel := context.WithTimeout(ctx, option.DBOpTimeout)
	defer cancel()
	return seafserver.FinishFileWrite(dbCtx, seafileDB, job)
}
