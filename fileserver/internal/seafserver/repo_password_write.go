package seafserver

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/repocrypt"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const repoPasswordChangeMessage = "Changed library password"

// MutationRPCHandlers exposes password updates on the private management socket.
func (p *RepoPasswords) MutationRPCHandlers(db *sql.DB) map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_change_repo_passwd": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			return p.changeRepoPassword(ctx, db, args)
		},
		"seafile_upgrade_repo_pwd_hash_algorithm": func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
			return p.upgradeRepoPasswordHash(ctx, db, args)
		},
	}
}

func (p *RepoPasswords) changeRepoPassword(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	id, old, next, user := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2), stringArgument(args, 3)
	switch {
	case user == nil:
		return -1, rpcError(errBadArgs, "No user given")
	case old == nil || *old == "" || next == nil || *next == "":
		return -1, rpcError(errBadArgs, "Empty passwd")
	case id == nil || !validRepoID(*id):
		return -1, rpcError(errBadArgs, "Invalid repo id")
	}

	return p.mutateRepoCredentials(ctx, db, *id, *user, repoPasswordChangeMessage, false, func(repo *repomgr.Repo) (bool, int, *searpc.RPCError) {
		if !repo.IsEncrypted {
			return false, -1, rpcError(errBadArgs, "Repo not encrypted")
		}
		if repo.EncVersion < 2 || repo.EncVersion > 4 {
			return false, -1, rpcError(errBadArgs, "Unsupported enc version")
		}
		salt := repoKeySalt(repo)
		if salt == nil || !verifyRepoPassword(repo, salt, *old) {
			return false, -1, rpcError(errBadArgs, "Incorrect password")
		}
		secret := unwrapRepoSecret(repo, salt, *old)
		if secret == nil {
			return false, -1, rpcError(errGeneral, "Internal server error")
		}
		defer clear(secret)
		key, iv := deriveRepoKey(repo.EncVersion, salt, []byte(*next))
		defer clear(key)
		defer clear(iv)
		wrapped, err := repocrypt.Encrypt(repo.EncVersion, key, iv, secret)
		if err != nil {
			return false, -1, rpcError(errGeneral, "Internal server error")
		}
		verifier := repoPasswordVerifier(repo.EncVersion, salt, repo.ID, *next, repo.PwdHashAlgo, repo.PwdHashParams)
		if verifier == nil {
			return false, -1, rpcError(errGeneral, "Internal server error")
		}
		defer clear(verifier)
		if repo.PwdHashAlgo == "" {
			repo.Magic = hex.EncodeToString(verifier)
		} else {
			repo.PwdHash = hex.EncodeToString(verifier)
		}
		repo.RandomKey = hex.EncodeToString(wrapped)
		return true, 0, nil
	})
}

func (p *RepoPasswords) mutateRepoCredentials(ctx context.Context, db *sql.DB, repoID, user, message string, revokeTokens bool,
	mutate func(*repomgr.Repo) (advanced bool, status int, rpcErr *searpc.RPCError),
) (int, *searpc.RPCError) {
	for range maxFileWriteRetries {
		repo, err := p.reader.Repo(ctx, repoID)
		if err != nil {
			return -1, rpcError(errBadArgs, "No such library")
		}
		advance, status, rpcErr := mutate(repo)
		if !advance || rpcErr != nil {
			return status, rpcErr
		}
		parent, err := p.reader.Commit(repo.ID, repo.HeadCommitID)
		if err != nil {
			return -1, rpcError(errGeneral, "Internal server error")
		}

		var commit *commitmgr.Commit
		advanced, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
			commit, err = p.reader.newRepoMetadataCommit(ctx, repo, parent, user, message)
			if err != nil {
				return false, err
			}
			published, err := publishSQLiteBranch(ctx, conn, parent.CommitID, commit)
			if err != nil || !published {
				return false, err
			}
			if revokeTokens {
				if err := deleteRepoSyncTokens(ctx, conn, repoID); err != nil {
					return false, err
				}
			}
			err = commitmgr.SaveCanonicalToStore(p.reader.commits, commit)
			return err == nil, err
		})
		if errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		}
		if err != nil || !advanced {
			log.Printf("update repo credentials %s: %v", repoID, err)
			return -1, rpcError(errGeneral, "Internal server error")
		}
		p.refreshCachedKey(repoPasswordID{repo: repoID, user: user})
		p.reader.PublishRepoUpdate(ctx, repoID, commit.CommitID, repo.VirtualInfo != nil)
		return status, nil
	}
	return -1, rpcError(errGeneral, "Internal server error")
}

func (p *RepoPasswords) refreshCachedKey(id repoPasswordID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cached, exists := p.keys[id]; exists {
		// Rewrapping the secret preserves the cached file key. Match the C
		// refresh for the acting user and leave all other entries untouched.
		cached.expiresAt = time.Now().Unix() + repoPasswordLifetime
		p.keys[id] = cached
	}
}
