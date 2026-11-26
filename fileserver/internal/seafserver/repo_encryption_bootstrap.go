package seafserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

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

	salt := legacyRepoSalt[:]
	var saltHex any
	if version >= 3 {
		salt = make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return nil, rpcError(errInternal, "Failed to generate repository salt")
		}
		saltHex = hex.EncodeToString(salt)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, rpcError(errInternal, "Failed to generate repository key")
	}
	defer clear(secret[:])
	key, iv := deriveRepoKey(version, salt, []byte(*password))
	defer clear(key)
	defer clear(iv)
	wrapped, err := repocrypt.Encrypt(version, key, iv, secret[:])
	if err != nil {
		return nil, rpcError(errInternal, "Failed to wrap repository key")
	}
	magic, magicIV := deriveRepoKey(version, salt, []byte(*repoID+*password))
	defer clear(magic)
	defer clear(magicIV)
	return map[string]any{
		"repo-id": *repoID, "passwd": *password, "enc-version": version,
		"magic": hex.EncodeToString(magic), "random-key": hex.EncodeToString(wrapped), "salt": saltHex,
		"pwd-hash": nil, "pwd-hash-algo": nil, "pwd-hash-params": nil,
	}, nil
}
