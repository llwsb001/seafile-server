package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) createVirtualRepo(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	origin, path := stringArgument(args, 0), stringArgument(args, 1)
	name, description, owner := stringArgument(args, 2), stringArgument(args, 3), stringArgument(args, 4)
	if origin == nil || path == nil || name == nil || description == nil || owner == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if !validRepoID(*origin) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	var password string
	if arg := stringArgument(args, 5); arg != nil {
		password = *arg
	}

	var repoID string
	var rpcErr *searpc.RPCError
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		repoID, rpcErr = r.createVirtualRepoInTx(ctx, conn, *origin, *path, *name, *description, password)
		return rpcErr == nil, nil
	})
	if err != nil {
		log.Printf("create virtual repo: %v", err)
		return nil, rpcError(errGeneral, "Internal error")
	}
	if rpcErr != nil {
		return nil, rpcErr
	}
	return repoID, nil
}

// createVirtualRepoInTx serializes lookup and publication in the caller's
// SQLite write transaction. The filesystem object is written before any row
// is added. A caller that also writes a share must commit both together.
func (r *Reader) createVirtualRepoInTx(ctx context.Context, conn *sql.Conn, repoID, path, name, description, password string) (string, *searpc.RPCError) {
	path = formatPath(path)
	if path == "/" {
		return "", rpcError(errGeneral, "Invalid path")
	}
	origin, fullPath, err := virtualRepoLocation(ctx, conn, repoID, path)
	if err != nil {
		log.Printf("create virtual repo: resolve origin: %v", err)
		return "", rpcError(errGeneral, "Internal error")
	}
	id, err := lookupVirtualRepo(ctx, conn, origin, fullPath)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		log.Printf("create virtual repo: lookup existing mapping: %v", err)
		return "", rpcError(errGeneral, "Internal error")
	}

	source, err := repomgr.Load(ctx, conn, origin, r.Commit)
	if err != nil || source.VirtualInfo != nil || !validRepoID(source.StoreID) {
		return "", rpcError(errGeneral, "Origin library not exists")
	}
	if source.Status != repomgr.RepoStatusNormal {
		return "", rpcError(errGeneral, "Unnormal repo status")
	}
	if source.IsEncrypted {
		if source.EncVersion < 2 {
			return "", rpcError(errBadArgs, "Library encryption version must be higher than 2")
		}
		if password == "" {
			return "", rpcError(errBadArgs, "Password is not set")
		}
	}
	if source.IsEncrypted || password != "" {
		return "", rpcError(errBadArgs, "Unsupported encryption version")
	}
	dir, rpcErr := r.directoryByPath(source, source.RootID, fullPath)
	if rpcErr != nil {
		return "", rpcError(errBadArgs, "Bad path")
	}
	counts, err := fsmgr.ReadFileCountInfo(r.fs, source.StoreID, dir.DirID, source.Version)
	if err != nil {
		log.Printf("create virtual repo: count files: %v", err)
		return "", rpcError(errGeneral, "Internal error")
	}

	// C inherits the owner from the ordinary origin, even when the RPC's
	// supplied owner differs. The original owner also creates the commit.
	owner, err := repomgr.LoadRepoOwner(ctx, conn, origin)
	if err != nil {
		return "", rpcError(errGeneral, "Failed to set repo owner.")
	}
	owner = asciiLower(owner)
	virtualID, err := uuid.NewRandom()
	if err != nil {
		return "", rpcError(errGeneral, "Failed to generate repo id")
	}
	commit := newInitialRepoCommit(virtualID.String(), dir.DirID, name, description, description, owner, source.Version, time.Now().Unix())
	if err := r.writeInitialRepoCommit(commit); err != nil {
		log.Printf("create virtual repo: write initial commit: %v", err)
		return "", rpcError(errGeneral, "Failed to add commit")
	}
	if err := publishInitialRepo(ctx, conn, commit, owner); err != nil {
		log.Printf("create virtual repo: publish initial metadata: %v", err)
		return "", rpcError(errGeneral, "Failed to add repo.")
	}
	if err := writeSQLiteRepoStats(ctx, conn, commit.RepoID, commit.CommitID, counts.Size, counts.FileCount); err != nil {
		log.Printf("create virtual repo: initial statistics: %v", err)
		return "", rpcError(errGeneral, "Internal error")
	}
	const insert = "INSERT INTO VirtualRepo (repo_id, origin_repo, path, base_commit) VALUES (?, ?, ?, ?)"
	if _, err := conn.ExecContext(ctx, insert, commit.RepoID, origin, fullPath, source.HeadCommitID); err != nil {
		log.Printf("create virtual repo: publish mapping: %v", err)
		return "", rpcError(errGeneral, "Internal error")
	}
	return commit.RepoID, nil
}

func virtualRepoLocation(ctx context.Context, conn *sql.Conn, repoID, path string) (string, string, error) {
	path = formatPath(path)
	var origin, prefix string
	const query = "SELECT origin_repo, path FROM VirtualRepo WHERE repo_id = ?"
	err := conn.QueryRowContext(ctx, query, repoID).Scan(&origin, &prefix)
	if errors.Is(err, sql.ErrNoRows) {
		return repoID, path, nil
	}
	if err != nil {
		return "", "", err
	}
	return origin, prefix + path, nil
}

func lookupVirtualRepo(ctx context.Context, conn *sql.Conn, origin, path string) (string, error) {
	var id string
	const query = "SELECT repo_id FROM VirtualRepo WHERE origin_repo = ? AND path = ?"
	err := conn.QueryRowContext(ctx, query, origin, path).Scan(&id)
	return id, err
}

// DeleteSQLiteVirtualRepo removes a missing view and its metadata as one unit.
func DeleteSQLiteVirtualRepo(ctx context.Context, db *sql.DB, info *repomgr.VRepoInfo, cloudMode bool) error {
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		deleted, err := conn.ExecContext(ctx, `DELETE FROM VirtualRepo WHERE repo_id = ? AND origin_repo = ?
			AND path = ? AND base_commit = ?`, info.RepoID, info.OriginRepoID, info.Path, info.BaseCommitID)
		if err != nil {
			return false, err
		}
		rows, err := deleted.RowsAffected()
		if err != nil {
			return false, err
		}
		if rows == 0 {
			return false, ErrSQLiteBranchChanged
		}
		tables := []string{"Repo", "RepoHead", "Branch", "RepoOwner", "SharedRepo", "RepoGroup",
			"RepoUserToken", "RepoValidSince", "RepoSize", "RepoFileCount", "RepoInfo"}
		if !cloudMode {
			tables = append(tables, "InnerPubRepo")
		}
		for _, table := range tables {
			if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE repo_id = ?", info.RepoID); err != nil {
				return false, err
			}
		}
		_, err = conn.ExecContext(ctx, "INSERT OR IGNORE INTO GarbageRepos (repo_id) VALUES (?)", info.RepoID)
		return err == nil, err
	})
	return err
}
