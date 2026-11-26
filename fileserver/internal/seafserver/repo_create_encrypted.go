package seafserver

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func (r *Reader) createEncryptedRepo(ctx context.Context, db *sql.DB, args []json.RawMessage) (any, *searpc.RPCError) {
	id, name, description, owner := stringArgument(args, 0), stringArgument(args, 1),
		stringArgument(args, 2), stringArgument(args, 3)
	if id == nil || name == nil || description == nil || owner == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if !validRepoID(*id) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	var rejected *searpc.RPCError
	failure := "Failed to add repo."
	_, err := withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		var exists bool
		if err := conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM Repo WHERE repo_id = ?)", *id).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			rejected = rpcError(errBadArgs, "Repo already exists")
			return false, nil
		}
		commit, err := r.unusedInitialRepoCommit(ctx, *id, *name, *description, *owner, time.Now().Unix())
		if err != nil {
			failure = "Failed to add commit"
			return false, err
		}
		rejected = applyInitialEncryption(commit, args)
		if rejected != nil {
			return false, nil
		}
		if err := publishInitialRepo(ctx, conn, commit, *owner); err != nil {
			return false, err
		}
		// The database references stay invisible until the new commit is durable.
		if err := r.writeInitialRepoCommit(commit); err != nil {
			failure = "Failed to add commit"
			return false, err
		}
		return true, nil
	})
	if err != nil {
		log.Printf("create encrypted repo %s: %v", *id, err)
		return nil, rpcError(errGeneral, failure)
	}
	if rejected != nil {
		return nil, rejected
	}
	return *id, nil
}

func (r *Reader) unusedInitialRepoCommit(ctx context.Context, id, name, description, owner string, timestamp int64) (*commitmgr.Commit, error) {
	for ; ; timestamp++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		commit := newInitialRepoCommit(id, emptyObjectID, name, description, "Created library", owner, initialRepoVersion, timestamp)
		exists, err := r.commits.Exists(commit.RepoID, commit.CommitID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if !exists {
			return commit, nil
		}
		// C's initial commit ID omits the encryption fields. An earlier
		// failed SQLite commit may already have written this object.
	}
}

func applyInitialEncryption(commit *commitmgr.Commit, args []json.RawMessage) *searpc.RPCError {
	version := intArgument(args, 7)
	if version != -1 && (version < 2 || version > 4) {
		return rpcError(errBadArgs, "Unsupported encryption version")
	}
	magic, wrapped, salt := stringArgument(args, 4), stringArgument(args, 5), stringArgument(args, 6)
	digest, algorithm, params := stringArgument(args, 8), stringArgument(args, 9), stringArgument(args, 10)
	if algorithm != nil {
		if *algorithm != "pbkdf2_sha256" && *algorithm != "argon2id" {
			return rpcError(errBadArgs, "Unsupported encryption algothrims")
		}
		if !validEncryptionHex(digest, 64, true) {
			return rpcError(errBadArgs, "Bad pwd_hash")
		}
	}
	// The legacy create RPC also accepts -1 to publish an ordinary library
	// with the supplied repository ID.
	if version == -1 {
		return nil
	}
	if algorithm != nil && params != nil && !canCreateRepoPasswordHash(*algorithm, *params) {
		return rpcError(errBadArgs, "Invalid password hash parameters")
	}
	if algorithm == nil && !validEncryptionHex(magic, 64, true) {
		return rpcError(errBadArgs, "Bad magic")
	}
	if !validEncryptionHex(wrapped, 96, false) {
		return rpcError(errBadArgs, "Bad random key")
	}
	if version >= 3 {
		if !validEncryptionHex(salt, 64, false) {
			return rpcError(errBadArgs, "Bad salt")
		}
		commit.Salt = *salt
	}
	commit.Encrypted, commit.EncVersion, commit.RandomKey = "true", version, *wrapped
	if algorithm == nil {
		commit.Magic = *magic
	} else {
		commit.PwdHash = *digest
		commit.PwdHashAlgo = *algorithm
		if params != nil {
			commit.PwdHashParams = *params
		}
	}
	return nil
}

func validEncryptionHex(value *string, length int, lowercase bool) bool {
	if value == nil || len(*value) != length || (lowercase && strings.ToLower(*value) != *value) {
		return false
	}
	_, err := hex.DecodeString(*value)
	return err == nil
}
