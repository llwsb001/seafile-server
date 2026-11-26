package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

const sqliteCredentialMergeMessage = "Merged files with the current library password"

var sqliteCredentialCommitMu sync.Mutex

func newFileServerCommit(repo *repomgr.Repo, parent *commitmgr.Commit, root, user, desc string) *commitmgr.Commit {
	if option.DBType != "sqlite" {
		commit := commitmgr.NewCommit(repo.ID, parent.CommitID, root, user, desc)
		repomgr.RepoToCommit(repo, commit)
		return commit
	}

	return newSQLiteFileServerCommit(repo, parent, root, user, desc, time.Now().Unix())
}

func newSQLiteFileServerCommit(repo *repomgr.Repo, parent *commitmgr.Commit, root, user, desc string, timestamp int64) *commitmgr.Commit {
	commit := commitmgr.NewCanonicalCommit(repo.ID, parent.CommitID, root, user, desc, timestamp)
	repomgr.RepoToCommit(repo, commit)
	commit.RepoDesc = repo.Desc
	commit.RepoCategory = parent.RepoCategory
	commit.NoLocalHistory = parent.NoLocalHistory
	if repo.Repaired {
		commit.Repaired = 1
	}
	return commit
}

func sameCommitEncryption(a, b *commitmgr.Commit) bool {
	return a.Encrypted == b.Encrypted && a.EncVersion == b.EncVersion && a.Magic == b.Magic &&
		a.RandomKey == b.RandomKey && a.Salt == b.Salt && a.PwdHash == b.PwdHash &&
		a.PwdHashAlgo == b.PwdHashAlgo && a.PwdHashParams == b.PwdHashParams &&
		a.HasPwdHashParams == b.HasPwdHashParams
}

func mergeSQLiteCredentialCommit(repo *repomgr.Repo, head, incoming *commitmgr.Commit, user string) (*commitmgr.Commit, error) {
	// The canonical hash excludes parents. Serialize selection and publication
	// of a distinct object ID for competing file uploads with the same tree.
	sqliteCredentialCommitMu.Lock()
	defer sqliteCredentialCommitMu.Unlock()
	for timestamp := time.Now().Unix(); ; timestamp++ {
		commit := newSQLiteFileServerCommit(repo, head, incoming.RootID, user, sqliteCredentialMergeMessage, timestamp)
		exists, err := commitmgr.Exists(repo.ID, commit.CommitID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if exists {
			continue
		}
		commit.SecondParentID.SetValid(incoming.CommitID)
		commit.NewMerge = 1
		if err := commitmgr.Save(commit); err != nil {
			return nil, fmt.Errorf("write SQLite credential-preserving merge: %w", err)
		}
		return commit, nil
	}
}
