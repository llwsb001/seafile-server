package main

import (
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

func newFileServerCommit(repo *repomgr.Repo, parent *commitmgr.Commit, root, user, desc string) *commitmgr.Commit {
	if option.DBType != "sqlite" {
		commit := commitmgr.NewCommit(repo.ID, parent.CommitID, root, user, desc)
		repomgr.RepoToCommit(repo, commit)
		return commit
	}

	commit := commitmgr.NewCanonicalCommit(repo.ID, parent.CommitID, root, user, desc, time.Now().Unix())
	repomgr.RepoToCommit(repo, commit)
	commit.RepoDesc = repo.Desc
	commit.RepoCategory = parent.RepoCategory
	commit.NoLocalHistory = parent.NoLocalHistory
	if repo.Repaired {
		commit.Repaired = 1
	}
	return commit
}
