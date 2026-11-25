package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"math/rand"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/diff"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/workerpool"
	log "github.com/sirupsen/logrus"
)

const mergeVirtualRepoWorkerNumber = 5

var mergeVirtualRepoPool *workerpool.WorkPool

var runningRepo = make(map[string]struct{})
var runningRepoMutex sync.Mutex

func virtualRepoInit() func() {
	mergeVirtualRepoPool = workerpool.CreateWorkerPool(mergeVirtualRepo, mergeVirtualRepoWorkerNumber)
	ctx, cancel := context.WithCancel(context.Background())
	if repomgr.UsesSQLite() {
		go runRPCFileWrites(ctx)
	}
	return cancel
}

func mergeVirtualRepo(args ...interface{}) error {
	if len(args) < 1 {
		return nil
	}
	repoID := args[0].(string)
	excludeRepo := ""
	if len(args) > 1 {
		excludeRepo = args[1].(string)
	}
	return mergeVirtualFamily(repoID, excludeRepo)
}

func mergeVirtualFamily(repoID, excludeRepo string) error {
	virtual, err := repomgr.IsVirtualRepo(repoID)
	if err != nil {
		return err
	}

	if virtual {
		err := mergeVirtualView(repoID)
		go updateSizePool.AddTask(repoID)
		return err
	}

	vRepos, err := repomgr.GetVirtualRepoIDsByOrigin(repoID)
	if err != nil {
		return err
	}
	var failed error
	for _, id := range vRepos {
		if id == excludeRepo {
			continue
		}
		failed = errors.Join(failed, mergeVirtualView(id))
	}

	go updateSizePool.AddTask(repoID)
	return failed
}

func mergeVirtualView(repoID string) error {
	runningRepoMutex.Lock()
	if _, ok := runningRepo[repoID]; ok {
		runningRepoMutex.Unlock()
		go mergeVirtualRepoPool.AddTask(repoID)
		return fmt.Errorf("virtual merge for %s is already running", repoID)
	}
	runningRepo[repoID] = struct{}{}
	runningRepoMutex.Unlock()
	defer func() {
		runningRepoMutex.Lock()
		delete(runningRepo, repoID)
		runningRepoMutex.Unlock()
	}()
	return mergeRepo(repoID)
}

func mergeRepo(repoID string) error {
	repo := repomgr.Get(repoID)
	if repo == nil {
		err := fmt.Errorf("failed to get virt repo %.10s", repoID)
		return err
	}
	vInfo := repo.VirtualInfo
	if vInfo == nil {
		return nil
	}
	origRepo := repomgr.Get(vInfo.OriginRepoID)
	if origRepo == nil {
		err := fmt.Errorf("failed to get orig repo %.10s", repoID)
		return err
	}

	head, err := commitmgr.Load(repo.ID, repo.HeadCommitID)
	if err != nil {
		err := fmt.Errorf("failed to get commit %s:%.8s", repo.ID, repo.HeadCommitID)
		return err
	}
	origHead, err := commitmgr.Load(origRepo.ID, origRepo.HeadCommitID)
	if err != nil {
		err := fmt.Errorf("merge repo %.8s failed: failed to get origin repo commit %s:%.8s", repoID, origRepo.ID, origRepo.HeadCommitID)
		return err
	}

	var origRoot string
	origRoot, err = fsmgr.GetSeafdirIDByPath(origRepo.StoreID, origHead.RootID, vInfo.Path)
	if err != nil && !errors.Is(err, fsmgr.ErrPathNoExist) {
		err := fmt.Errorf("merge repo %.10s failed: failed to get seafdir id by path in origin repo %.10s: %v", repoID, origRepo.StoreID, err)
		return err
	}
	if origRoot == "" {
		newPath, err := handleMissingVirtualRepo(origRepo, origHead, vInfo)
		if err != nil || newPath == "" {
			return err
		}
		origRoot, err = fsmgr.GetSeafdirIDByPath(origRepo.StoreID, origHead.RootID, newPath)
		if err != nil {
			return err
		}
		vInfo.Path, vInfo.BaseCommitID = newPath, origHead.CommitID
	}

	base, err := commitmgr.Load(origRepo.ID, vInfo.BaseCommitID)
	if err != nil {
		err := fmt.Errorf("merge repo %.8s failed: failed to get origin repo commit %s:%.8s", repoID, origRepo.ID, vInfo.BaseCommitID)
		return err
	}

	root := head.RootID
	baseRoot, _ := fsmgr.GetSeafdirIDByPath(origRepo.StoreID, base.RootID, vInfo.Path)
	if baseRoot == "" {
		err := fmt.Errorf("merge repo %.10s failed: cannot find seafdir for origin repo %.10s path %s", repoID, vInfo.OriginRepoID, vInfo.Path)
		return err
	}

	if root == origRoot {
		if repomgr.UsesSQLite() && vInfo.BaseCommitID != origHead.CommitID {
			return repomgr.SetVirtualRepoBaseCommitPath(repo.ID, origHead.CommitID, vInfo.Path)
		}
	} else if baseRoot == root {
		_, err := updateDir(repoID, "/", origRoot, origHead.CreatorName, head.CommitID)
		if err != nil {
			err := fmt.Errorf("failed to update root of virtual repo %.10s", repoID)
			return err
		}
		return repomgr.SetVirtualRepoBaseCommitPath(repo.ID, origRepo.HeadCommitID, vInfo.Path)
	} else if baseRoot == origRoot {
		newBaseCommit, err := updateDir(vInfo.OriginRepoID, vInfo.Path, root, head.CreatorName, origHead.CommitID)
		if err != nil {
			err := fmt.Errorf("merge repo %.8s failed: failed to update origin repo%.10s path %s", repoID, vInfo.OriginRepoID, vInfo.Path)
			return err
		}
		if err := repomgr.SetVirtualRepoBaseCommitPath(repo.ID, newBaseCommit, vInfo.Path); err != nil {
			return err
		}
		cleanupVirtualRepos(vInfo.OriginRepoID)
		mergeVirtualRepo(vInfo.OriginRepoID, repoID)
	} else {
		roots := []string{baseRoot, origRoot, root}
		opt := new(mergeOptions)
		opt.remoteRepoID = repoID
		opt.remoteHead = head.CommitID

		err := mergeTrees(origRepo.StoreID, roots, opt)
		if err != nil {
			err := fmt.Errorf("failed to merge")
			return err
		}

		_, err = updateDir(repoID, "/", opt.mergedRoot, origHead.CreatorName, head.CommitID)
		if err != nil {
			err := fmt.Errorf("failed to update root of virtual repo %.10s", repoID)
			return err
		}

		newBaseCommit, err := updateDir(vInfo.OriginRepoID, vInfo.Path, opt.mergedRoot, head.CreatorName, origHead.CommitID)
		if err != nil {
			err := fmt.Errorf("merge repo %.10s failed: failed to update origin repo %.10s path %s", repoID, vInfo.OriginRepoID, vInfo.Path)
			return err
		}
		if err := repomgr.SetVirtualRepoBaseCommitPath(repo.ID, newBaseCommit, vInfo.Path); err != nil {
			return err
		}
		cleanupVirtualRepos(vInfo.OriginRepoID)
		mergeVirtualRepo(vInfo.OriginRepoID, repoID)
	}

	return nil
}

func cleanupVirtualRepos(repoID string) error {
	repo := repomgr.Get(repoID)
	if repo == nil {
		err := fmt.Errorf("failed to get repo %.10s", repoID)
		return err
	}

	head, err := commitmgr.Load(repo.ID, repo.HeadCommitID)
	if err != nil {
		err := fmt.Errorf("failed to load commit %s/%s : %v", repo.ID, repo.HeadCommitID, err)
		return err
	}

	vRepos, err := repomgr.GetVirtualRepoInfoByOrigin(repoID)
	if err != nil {
		err := fmt.Errorf("failed to get virtual repo ids by origin repo %.10s", repoID)
		return err
	}
	for _, vInfo := range vRepos {
		_, err := fsmgr.GetSeafdirByPath(repo.StoreID, head.RootID, vInfo.Path)
		if err != nil {
			if err == fsmgr.ErrPathNoExist {
				handleMissingVirtualRepo(repo, head, vInfo)
			}
		}
	}

	return nil
}

func handleMissingVirtualRepo(repo *repomgr.Repo, head *commitmgr.Commit, vInfo *repomgr.VRepoInfo) (string, error) {
	parent, err := commitmgr.Load(head.RepoID, head.ParentID.String)
	if err != nil {
		err := fmt.Errorf("failed to load commit %s/%s : %v", head.RepoID, head.ParentID.String, err)
		return "", err
	}

	var results []*diff.DiffEntry
	err = diff.DiffCommits(parent, head, &results, true)
	if err != nil {
		err := fmt.Errorf("failed to diff commits")
		return "", err
	}

	parPath := vInfo.Path
	var isRenamed bool
	var subPath string
	var returnPath string
	for {
		var newPath string
		oldDirID, err := fsmgr.GetSeafdirIDByPath(repo.StoreID, parent.RootID, parPath)
		if err != nil || oldDirID == "" {
			if errors.Is(err, fsmgr.ErrPathNoExist) {
				return "", deleteMissingVirtualRepo(vInfo)
			}
			err := fmt.Errorf("failed to find %s under commit %s in repo %s", parPath, parent.CommitID, repo.StoreID)
			return "", err
		}

		for _, de := range results {
			if de.Status == diff.DiffStatusDirRenamed {
				if de.Sha1 == oldDirID {
					if subPath != "" {
						newPath = filepath.Join("/", de.NewName, subPath)
					} else {
						newPath = filepath.Join("/", de.NewName)
					}
					if err := repomgr.SetVirtualRepoBaseCommitPath(vInfo.RepoID, head.CommitID, newPath); err != nil {
						return "", err
					}
					returnPath = newPath
					if subPath == "" {
						newName := filepath.Base(newPath)
						err := editRepo(vInfo.RepoID, newName, "Changed library name", "")
						if err != nil {
							log.Warnf("falied to rename repo %s.\n", newName)
						}
					}
					isRenamed = true
					break
				}
			}
		}

		if isRenamed {
			break
		}

		slash := strings.LastIndex(parPath, "/")
		if slash <= 0 {
			break
		}
		subPath = filepath.Base(parPath)
		parPath = filepath.Dir(parPath)
	}

	if !isRenamed {
		return "", deleteMissingVirtualRepo(vInfo)
	}

	return returnPath, nil
}

func deleteMissingVirtualRepo(info *repomgr.VRepoInfo) error {
	if !repomgr.UsesSQLite() {
		return repomgr.DelVirtualRepo(info.RepoID, option.CloudMode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), option.DBOpTimeout)
	defer cancel()
	return seafserver.DeleteSQLiteVirtualRepo(ctx, seafileDB, info, option.CloudMode)
}

func editRepo(repoID, name, desc, user string) error {
	if name == "" && desc == "" {
		err := fmt.Errorf("at least one argument should be non-null")
		return err
	}

	var retryCnt int
	for retry, err := editRepoNeedRetry(repoID, name, desc, user); err != nil || retry; {
		if err != nil {
			err := fmt.Errorf("failed to edit repo: %v", err)
			return err
		}
		if retryCnt < 3 {
			random := rand.Intn(10) + 1
			time.Sleep(time.Duration(random*100) * time.Millisecond)
			retryCnt++
		} else {
			err := fmt.Errorf("stop edit repo %s after 3 retries", repoID)
			return err
		}
	}

	return nil
}

func editRepoNeedRetry(repoID, name, desc, user string) (bool, error) {
	repo := repomgr.Get(repoID)
	if repo == nil {
		err := fmt.Errorf("no such library")
		return false, err
	}
	if name == "" {
		name = repo.Name
	}
	if desc == "" {
		desc = repo.Desc
	}

	parent, err := commitmgr.Load(repo.ID, repo.HeadCommitID)
	if err != nil {
		err := fmt.Errorf("failed to get commit %s:%s", repo.ID, repo.HeadCommitID)
		return false, err
	}

	if user == "" {
		user = parent.CreatorName
	}

	commit := newFileServerCommit(repo, parent, parent.RootID, user, "Changed library name or description")
	commit.RepoName = name
	commit.RepoDesc = desc

	err = commitmgr.Save(commit)
	if err != nil {
		err := fmt.Errorf("failed to add commit: %v", err)
		return false, err
	}

	_, err = updateBranch(repoID, repo.StoreID, commit.CommitID, parent.CommitID, "", false, "")
	if err != nil {
		if repomgr.UsesSQLite() && !errors.Is(err, seafserver.ErrSQLiteBranchChanged) {
			return false, err
		}
		return true, nil
	}

	if !repomgr.UsesSQLite() {
		if err := repomgr.UpdateRepoInfo(repoID, commit.CommitID); err != nil {
			log.Warnf("failed to update repo info %s: %v", repoID, err)
		}
	}

	return false, nil
}
