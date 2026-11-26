package seafserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/repocrypt"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func encryptionBootstrap(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	version := intArgument(args, 0)
	repoID, password := stringArgument(args, 1), stringArgument(args, 2)
	if repoID == nil || password == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if version < 2 || version > 4 {
		return nil, rpcError(errBadArgs, "Unsupported encryption version")
	}

	salt, wrapped, rpcErr := generateRepoKeyMaterial(version, *password)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var saltHex any
	if version >= 3 {
		saltHex = hex.EncodeToString(salt)
	}
	magic := repoPasswordVerifier(version, salt, *repoID, *password, "", "")
	defer clear(magic)
	return map[string]any{
		"repo-id": *repoID, "passwd": *password, "enc-version": version,
		"magic": hex.EncodeToString(magic), "random-key": wrapped, "salt": saltHex,
		"pwd-hash": nil, "pwd-hash-algo": nil, "pwd-hash-params": nil,
	}, nil
}

func generateRepoKeyMaterial(version int, password string) ([]byte, string, *searpc.RPCError) {
	salt := legacyRepoSalt[:]
	if version >= 3 {
		salt = make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return nil, "", rpcError(errInternal, "Failed to generate repository salt")
		}
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, "", rpcError(errInternal, "Failed to generate repository key")
	}
	defer clear(secret[:])
	passwordBytes := []byte(password)
	defer clear(passwordBytes)
	key, iv := deriveRepoKey(version, salt, passwordBytes)
	defer clear(key)
	defer clear(iv)
	wrapped, err := repocrypt.Encrypt(version, key, iv, secret[:])
	if err != nil {
		return nil, "", rpcError(errInternal, "Failed to wrap repository key")
	}
	return salt, hex.EncodeToString(wrapped), nil
}

func setGeneratedRepoEncryption(commit *commitmgr.Commit, password string, args []json.RawMessage) *searpc.RPCError {
	version := intArgument(args, 4)
	if version < 2 || version > 4 {
		return rpcError(errBadArgs, "Unsupported encryption version")
	}
	var algorithm, params *string
	if len(args) > 5 {
		if err := json.Unmarshal(args[5], &algorithm); err != nil {
			return rpcError(errBadArgs, "Unsupported encryption algothrims")
		}
	}
	if algorithm != nil {
		if *algorithm != "pbkdf2_sha256" && *algorithm != "argon2id" {
			return rpcError(errBadArgs, "Unsupported encryption algothrims")
		}
		if len(args) > 6 {
			if err := json.Unmarshal(args[6], &params); err != nil {
				return rpcError(errBadArgs, "Invalid password hash parameters")
			}
		}
		commit.PwdHashAlgo = *algorithm
		if params != nil {
			commit.PwdHashParams, commit.HasPwdHashParams = *params, true
		}
		if strings.ContainsRune(commit.PwdHashParams, 0) ||
			!canCreateRepoPasswordHash(commit.PwdHashAlgo, commit.PwdHashParams) {
			return rpcError(errBadArgs, "Invalid password hash parameters")
		}
	}
	salt, wrapped, rpcErr := generateRepoKeyMaterial(version, password)
	if rpcErr != nil {
		return rpcErr
	}
	verifier := repoPasswordVerifier(version, salt, commit.RepoID, password, commit.PwdHashAlgo, commit.PwdHashParams)
	defer clear(verifier)
	commit.Encrypted, commit.EncVersion, commit.RandomKey = "true", version, wrapped
	if version >= 3 {
		commit.Salt = hex.EncodeToString(salt)
	}
	if algorithm == nil {
		commit.Magic = hex.EncodeToString(verifier)
	} else {
		commit.PwdHash = hex.EncodeToString(verifier)
	}
	return nil
}
