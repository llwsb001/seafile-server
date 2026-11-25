package seafserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const (
	errRepoPassword      = 500
	repoPasswordLifetime = int64(3600)
	repoPasswordReap     = time.Minute
)

type repoPasswordID struct {
	repo string
	user string
}

type cachedRepoKey struct {
	key       []byte
	iv        []byte
	expiresAt int64
}

func (p *RepoPasswords) copyKey(repo, user string) ([]byte, []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key, exists := p.keys[repoPasswordID{repo: repo, user: user}]
	if !exists || time.Now().Unix() >= key.expiresAt {
		return nil, nil
	}
	return bytes.Clone(key.key), bytes.Clone(key.iv)
}

// RepoPasswords stores process-local decryption keys for encrypted repositories.
type RepoPasswords struct {
	reader *Reader
	mu     sync.Mutex
	keys   map[repoPasswordID]cachedRepoKey
}

// NewRepoPasswords starts with an empty repository key cache.
func NewRepoPasswords(reader *Reader) *RepoPasswords {
	return &RepoPasswords{reader: reader, keys: make(map[repoPasswordID]cachedRepoKey)}
}

// RPCHandlers exposes the encrypted repository password and key RPCs.
func (p *RepoPasswords) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_check_passwd":    p.check,
		"seafile_set_passwd":      p.set,
		"seafile_unset_passwd":    p.unset,
		"seafile_is_passwd_set":   p.isSet,
		"seafile_get_decrypt_key": p.getKey,
	}
}

// StartCleanup reaps expired keys every minute, like the C key cache.
func (p *RepoPasswords) StartCleanup(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(repoPasswordReap)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.reap(time.Now().Unix())
			}
		}
	}()
	return func() {
		cancel()
		<-done
		p.mu.Lock()
		defer p.mu.Unlock()
		for id := range p.keys {
			p.remove(id)
		}
	}
}

func (p *RepoPasswords) reap(now int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, key := range p.keys {
		if key.expiresAt <= now {
			p.remove(id)
		}
	}
}

// remove requires the cache lock.
func (p *RepoPasswords) remove(id repoPasswordID) {
	old := p.keys[id]
	clear(old.key)
	clear(old.iv)
	delete(p.keys, id)
}

func repoPasswordArgs(args []json.RawMessage) (*string, *string, *searpc.RPCError) {
	repo, second := stringArgument(args, 0), stringArgument(args, 1)
	if repo == nil || len(*repo) != 36 || second == nil {
		return nil, nil, rpcError(errBadArgs, "Bad arguments")
	}
	return repo, second, nil
}

func (p *RepoPasswords) encryptedRepo(ctx context.Context, id string) (*repomgr.Repo, *searpc.RPCError) {
	repo, err := p.reader.Repo(ctx, id)
	if err != nil {
		return nil, rpcError(errBadArgs, "Invalid repo")
	}
	if !repo.IsEncrypted {
		return nil, rpcError(errBadArgs, "Repo is not encrypted")
	}
	return repo, nil
}

func (p *RepoPasswords) check(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, magic, badArgs := repoPasswordArgs(args)
	if badArgs != nil {
		return -1, badArgs
	}
	repo, err := p.encryptedRepo(ctx, *id)
	if err != nil {
		return -1, err
	}
	if subtle.ConstantTimeCompare([]byte(*magic), []byte(repo.Magic)) != 1 {
		return -1, rpcError(errRepoPassword, "Incorrect password")
	}
	return 0, nil
}

func (p *RepoPasswords) set(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user, badArgs := repoPasswordArgs(args)
	if badArgs != nil {
		return -1, badArgs
	}
	password := stringArgument(args, 2)
	if password == nil {
		return -1, rpcError(errBadArgs, "Bad arguments")
	}
	repo, err := p.encryptedRepo(ctx, *id)
	if err != nil {
		return -1, err
	}
	if repo.EncVersion < 1 || repo.EncVersion > 4 {
		return -1, rpcError(errBadArgs, "Unsupported encryption version")
	}
	key, iv, ok := unlockRepo(repo, *password)
	if !ok {
		return -1, rpcError(errRepoPassword, "Incorrect password")
	}

	p.cache(repoPasswordID{repo: *id, user: *user}, key, iv, time.Now().Unix())
	return 0, nil
}

func (p *RepoPasswords) cache(id repoPasswordID, key, iv []byte, now int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.remove(id)
	p.keys[id] = cachedRepoKey{key: key, iv: iv, expiresAt: now + repoPasswordLifetime}
}

func (p *RepoPasswords) unset(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user, err := repoPasswordArgs(args)
	if err != nil {
		return -1, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.remove(repoPasswordID{repo: *id, user: *user})
	return 0, nil
}

func (p *RepoPasswords) isSet(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user, err := repoPasswordArgs(args)
	if err != nil {
		return -1, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.keys[repoPasswordID{repo: *id, user: *user}]; exists {
		return 1, nil
	}
	return 0, nil
}

func (p *RepoPasswords) getKey(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, user, err := repoPasswordArgs(args)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key, exists := p.keys[repoPasswordID{repo: *id, user: *user}]
	if !exists {
		return nil, rpcError(errRepoPassword, "Password was not set")
	}
	return map[string]string{"key": hex.EncodeToString(key.key), "iv": hex.EncodeToString(key.iv)}, nil
}
