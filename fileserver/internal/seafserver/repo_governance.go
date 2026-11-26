package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const repoEditMessage = "Changed library name or description"

func (r *Reader) RepoGovernanceRPCHandlers(db *sql.DB) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_edit_repo": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			return r.editRepo(ctx, db, args)
		},
	}
}

func (r *Reader) editRepo(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	id, name, description, user := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2), stringArgument(args, 3)
	if name == nil && description == nil {
		return -1, rpcError(errBadArgs, "At least one argument should be non-null")
	}
	if id == nil || !validRepoID(*id) {
		return -1, rpcError(errBadArgs, "Invalid repo id")
	}

	for range maxFileWriteRetries {
		repo, err := r.Repo(ctx, *id)
		if err != nil {
			return -1, rpcError(errBadArgs, "No such library")
		}
		parent, err := r.Commit(repo.ID, repo.HeadCommitID)
		if err != nil {
			log.Printf("edit repo %s: read head: %v", repo.ID, err)
			return -1, nil
		}

		var commit *commitmgr.Commit
		advanced, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
			commit, err = r.newRepoEditCommit(ctx, repo, parent, name, description, user)
			if err != nil {
				return false, err
			}
			advanced, err := publishSQLiteBranch(ctx, conn, parent.CommitID, commit)
			if err != nil || !advanced {
				return false, err
			}
			// The transaction hides the new reference until its object is durable.
			err = commitmgr.SaveCanonicalToStore(r.commits, commit)
			return err == nil, err
		})
		if errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		}
		if err != nil {
			log.Printf("edit repo %s: publish metadata: %v", repo.ID, err)
			return -1, nil
		}
		if advanced {
			r.PublishRepoUpdate(ctx, repo.ID, commit.CommitID, repo.VirtualInfo != nil)
			return 0, nil
		}
		break
	}
	return -1, nil
}

func (r *Reader) newRepoEditCommit(ctx context.Context, repo *repomgr.Repo, parent *commitmgr.Commit, name, description, user *string) (*commitmgr.Commit, error) {
	creator := parent.CreatorName
	if user != nil {
		creator = *user
	}
	for timestamp := time.Now().Unix(); ; timestamp++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		commit := commitmgr.NewCanonicalCommit(repo.ID, parent.CommitID, parent.RootID, creator, repoEditMessage, timestamp)
		// The C hash excludes the parent, library name, and library description.
		// Consecutive edits by the same creator must use distinct object IDs.
		exists, err := r.commits.Exists(repo.ID, commit.CommitID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if exists {
			continue
		}
		repomgr.RepoToCommit(repo, commit)
		commit.RepoDesc = parent.RepoDesc
		commit.NoLocalHistory = parent.NoLocalHistory
		commit.Repaired = parent.Repaired
		if name != nil {
			commit.RepoName = *name
		}
		if description != nil {
			commit.RepoDesc = *description
		}
		return commit, nil
	}
}
