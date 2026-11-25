package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const emptyObjectID = "0000000000000000000000000000000000000000"
const initialRepoVersion = 1

func (r *Reader) RepoCreationRPCHandlers(db *sql.DB) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_create_repo": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			return r.createRepo(ctx, db, args)
		},
		"create_virtual_repo": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			return r.createVirtualRepo(ctx, db, args)
		},
	}
}

func (r *Reader) createRepo(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	name, description, owner := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if name == nil || description == nil || owner == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	password := stringArgument(args, 3)
	if password != nil && (*password != "" || intArgument(args, 4) != -1 || stringArgument(args, 5) != nil) {
		return nil, rpcError(errBadArgs, "Unsupported encryption version")
	}

	repoID, err := uuid.NewRandom()
	if err != nil {
		return nil, rpcError(errGeneral, "Failed to generate repo id")
	}
	commit := newInitialRepoCommit(repoID.String(), emptyObjectID, *name, *description, "Created library", *owner, initialRepoVersion, time.Now().Unix())
	if err := r.writeInitialRepoCommit(commit); err != nil {
		log.Printf("create repo: write initial commit: %v", err)
		return nil, rpcError(errGeneral, "Failed to add commit")
	}
	_, err = withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		if err := publishInitialRepo(ctx, conn, commit, *owner); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		log.Printf("create repo: publish initial metadata: %v", err)
		return nil, rpcError(errGeneral, "Failed to add repo.")
	}
	return commit.RepoID, nil
}

func newInitialRepoCommit(repoID, rootID, name, description, message, owner string, version int, timestamp int64) *commitmgr.Commit {
	commit := commitmgr.NewCanonicalCommit(repoID, "", rootID, owner, message, timestamp)
	commit.RepoName = name
	commit.RepoDesc = description
	commit.Version = version
	commit.NoLocalHistory = 1
	return commit
}

func (r *Reader) writeInitialRepoCommit(commit *commitmgr.Commit) error {
	return commitmgr.SaveCanonicalToStore(r.commits, commit)
}

func publishInitialRepo(ctx context.Context, conn *sql.Conn, commit *commitmgr.Commit, owner string) error {
	if _, err := conn.ExecContext(ctx, "INSERT INTO Branch (name, repo_id, commit_id) VALUES ('master', ?, ?)",
		commit.RepoID, commit.CommitID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO RepoHead (repo_id, branch_name) VALUES (?, 'master')",
		commit.RepoID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO Repo (repo_id) VALUES (?)", commit.RepoID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO RepoOwner (repo_id, owner_id) VALUES (?, ?)",
		commit.RepoID, owner); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO RepoInfo
		(repo_id, name, update_time, version, is_encrypted, last_modifier) VALUES (?, ?, ?, ?, 0, ?)`,
		commit.RepoID, commit.RepoName, commit.Ctime, commit.Version, commit.CreatorName)
	return err
}
