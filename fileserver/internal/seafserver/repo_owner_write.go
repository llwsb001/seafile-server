package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

// SetRepoOwnerRPC transfers ownership on the private management socket.
func SetRepoOwnerRPC(db *sql.DB, cloudMode bool) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		id, owner := stringArgument(args, 0), stringArgument(args, 1)
		if id == nil || owner == nil {
			return -1, rpcError(errBadArgs, "Argument should not be null")
		}
		if !validRepoID(*id) {
			return -1, rpcError(errBadArgs, "Invalid repo id")
		}
		_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
			var oldOwner sql.NullString
			err := conn.QueryRowContext(ctx, "SELECT owner_id FROM RepoOwner WHERE repo_id = ?", *id).Scan(&oldOwner)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
			if oldOwner.Valid && asciiLower(oldOwner.String) == *owner {
				return false, nil
			}
			if _, err := conn.ExecContext(ctx, "REPLACE INTO RepoOwner (repo_id, owner_id) VALUES (?, ?)", *id, *owner); err != nil {
				return false, err
			}
			if !oldOwner.Valid {
				return true, nil
			}
			err = cleanTransferredRepo(ctx, conn, *id, cloudMode)
			return err == nil, err
		})
		if err != nil {
			// Seahub's transfer helper relies on an exception to report failure.
			log.Printf("set repo owner %s: %v", *id, err)
			return -1, rpcError(errGeneral, "Internal server error")
		}
		return 0, nil
	}
}

func cleanTransferredRepo(ctx context.Context, conn *sql.Conn, repoID string, cloudMode bool) error {
	const children = `SELECT repo_id FROM VirtualRepo WHERE origin_repo = ?`
	shares := []string{"SharedRepo", "RepoGroup"}
	if !cloudMode {
		shares = append(shares, "InnerPubRepo")
	}
	for _, table := range shares {
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE repo_id = ? OR repo_id IN ("+children+")", repoID, repoID); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM RepoHead WHERE repo_id IN (`+children+`)
		AND EXISTS (SELECT 1 FROM Branch WHERE Branch.repo_id = RepoHead.repo_id AND Branch.name = RepoHead.branch_name)`, repoID); err != nil {
		return err
	}
	// Keep the origin's sync tokens and the legacy statistics and peer rows of removed views.
	for _, table := range []string{"Repo", "Branch", "RepoOwner", "RepoUserToken", "RepoValidSince", "RepoSize", "RepoInfo"} {
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table+" WHERE repo_id IN ("+children+")", repoID); err != nil {
			return err
		}
	}
	// GC removes the virtual commit objects separately. The data objects belong to the origin.
	if _, err := conn.ExecContext(ctx, "REPLACE INTO GarbageRepos (repo_id) "+children, repoID); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, "DELETE FROM VirtualRepo WHERE repo_id = ? OR origin_repo = ?", repoID, repoID)
	return err
}
