package seafserver

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const repoPasswordUpgradeMessage = "Changed library password hash algorithm"

func (p *RepoPasswords) upgradeRepoPasswordHash(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user, password := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	algo, params := stringArgument(args, 3), stringArgument(args, 4)
	switch {
	case user == nil:
		return -1, rpcError(errBadArgs, "No user given")
	case password == nil || *password == "":
		return -1, rpcError(errBadArgs, "Empty passwd")
	case id == nil || !validRepoID(*id):
		return -1, rpcError(errBadArgs, "Invalid repo id")
	case algo == nil:
		return -1, rpcError(errBadArgs, "Invalid pwd hash algorithm")
	case *algo != "pbkdf2_sha256" && *algo != "argon2id":
		return -1, rpcError(errBadArgs, "Unsupported pwd hash algorithm")
	case params == nil:
		return -1, rpcError(errBadArgs, "Invalid pwd hash params")
	}

	return p.mutateRepoCredentials(ctx, db, *id, *user, repoPasswordUpgradeMessage, true, func(repo *repomgr.Repo) (bool, int, *searpc.RPCError) {
		if repo.PwdHashAlgo == *algo && repo.PwdHashParams == *params {
			return false, 0, nil
		}
		if !repo.IsEncrypted {
			return false, -1, rpcError(errBadArgs, "Repo not encrypted")
		}
		if repo.EncVersion < 2 || repo.EncVersion > 4 {
			// The C v1 reader only accepts a 16-byte magic, so it cannot read a
			// v1 commit where the 32-byte password hash replaces that field.
			return false, -1, rpcError(errBadArgs, "Unsupported enc version")
		}
		if !canCreateRepoPasswordHash(*algo, *params) {
			return false, -1, rpcError(errBadArgs, "Invalid pwd hash params")
		}
		salt := repoKeySalt(repo)
		if salt == nil || !verifyRepoPassword(repo, salt, *password) {
			status := -1
			if repo.PwdHashAlgo != "" {
				status = 1
			}
			return false, status, rpcError(errBadArgs, "Incorrect password")
		}
		verifier := repoPasswordVerifier(repo.EncVersion, salt, repo.ID, *password, *algo, *params)
		if verifier == nil {
			return false, -1, rpcError(errBadArgs, "Invalid pwd hash params")
		}
		defer clear(verifier)
		repo.Magic, repo.PwdHash = "", hex.EncodeToString(verifier)
		repo.PwdHashAlgo, repo.PwdHashParams = *algo, *params
		return true, 0, nil
	})
}

func deleteRepoSyncTokens(ctx context.Context, conn *sql.Conn, repoID string) error {
	const tokens = `(SELECT token FROM RepoUserToken WHERE repo_id = ?)`
	if _, err := conn.ExecContext(ctx, `DELETE FROM RepoTokenPeerInfo WHERE token IN `+tokens, repoID); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `DELETE FROM RepoUserToken WHERE token IN `+tokens, repoID)
	return err
}
