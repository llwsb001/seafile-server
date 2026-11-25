package seafserver

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
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
	commit := newInitialRepoCommit(repoID.String(), emptyObjectID, *name, *description, *owner, time.Now().Unix())
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

func newInitialRepoCommit(repoID, rootID, name, description, owner string, timestamp int64) *commitmgr.Commit {
	commit := &commitmgr.Commit{
		RepoID: repoID, RootID: rootID, CreatorName: owner, CreatorID: emptyObjectID,
		Desc: "Created library", Ctime: timestamp, RepoName: name, RepoDesc: description,
		Version: initialRepoVersion,
	}

	hash := sha1.New()
	var root, creator [41]byte
	copy(root[:], commit.RootID)
	copy(creator[:], commit.CreatorID)
	hash.Write(root[:])
	hash.Write(creator[:])
	hash.Write(append([]byte(commit.CreatorName), 0))
	hash.Write(append([]byte(commit.Desc), 0))
	var value [8]byte
	binary.BigEndian.PutUint64(value[:], uint64(commit.Ctime))
	hash.Write(value[:])
	commit.CommitID = hex.EncodeToString(hash.Sum(nil))
	return commit
}

func (r *Reader) writeInitialRepoCommit(commit *commitmgr.Commit) error {
	// The C initial commit records creator_name even for the empty string.
	object := struct {
		*commitmgr.Commit
		CreatorName    string  `json:"creator_name"`
		RepoCategory   *string `json:"repo_category"`
		NoLocalHistory int     `json:"no_local_history"`
	}{Commit: commit, CreatorName: commit.CreatorName, NoLocalHistory: 1}
	payload, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return r.commits.Write(commit.RepoID, commit.CommitID, bytes.NewReader(payload), true)
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
