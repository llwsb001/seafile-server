package seafserver

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const (
	errWebAccessToken   = 500
	webTokenSweepPeriod = 5 * time.Minute
)

type webAccess struct {
	RepoID   string `json:"repo-id"`
	ObjectID string `json:"obj-id"`
	Op       string `json:"op"`
	Username string `json:"username"`
}

type webAccessToken struct {
	access    webAccess
	expiresAt int64
	oneTime   bool
}

// WebAccessTokens manages the transient credentials for the Go fileserver.
type WebAccessTokens struct {
	ttl  int64
	now  func() time.Time
	zips *ZipDownloads

	mu     sync.Mutex
	tokens map[string]webAccessToken
}

// WebAccessTokens attaches ZIP preparation to tokens issued by this core.
func (m *ZipDownloads) WebAccessTokens(lifetime int64) *WebAccessTokens {
	tokens := NewWebAccessTokens(lifetime)
	tokens.zips = m
	return tokens
}

// NewWebAccessTokens creates an empty store with a lifetime in seconds.
func NewWebAccessTokens(lifetime int64) *WebAccessTokens {
	return &WebAccessTokens{
		ttl:    lifetime,
		now:    time.Now,
		tokens: make(map[string]webAccessToken),
	}
}

// RPCHandlers registers both operations with the same token store.
func (s *WebAccessTokens) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_web_get_access_token":   s.getToken,
		"seafile_web_query_access_token": s.queryToken,
	}
}

// StartCleanup runs periodic expiration checks until the context is canceled or the returned stop function is called.
func (s *WebAccessTokens) StartCleanup(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(webTokenSweepPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.prune()
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *WebAccessTokens) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().Unix()
	for id, entry := range s.tokens {
		if now >= entry.expiresAt {
			delete(s.tokens, id)
		}
	}
}

func validWebAccessOperation(op string) bool {
	switch op {
	case "view", "download", "downloadblks", "download-dir", "download-multi",
		"download-link", "download-dir-link", "download-multi-link",
		"upload", "update", "upload-link", "upload-blks-api", "upload-blks-aj",
		"update-blks-api", "update-blks-aj":
		return true
	}
	return false
}

func (s *WebAccessTokens) getToken(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, objectID := stringArgument(args, 0), stringArgument(args, 1)
	op, username := stringArgument(args, 2), stringArgument(args, 3)
	if repoID == nil || objectID == nil || op == nil || username == nil {
		return nil, rpcError(errBadArgs, "Missing args")
	}
	if !validWebAccessOperation(*op) {
		return nil, rpcError(errWebAccessToken, "Invalid operation type.")
	}
	access := webAccess{RepoID: *repoID, ObjectID: *objectID, Op: *op, Username: *username}
	token, err := s.issue(access, intArgument(args, 4) != 0)
	if err != nil {
		log.Printf("generate web access token: %v", err)
		return nil, rpcError(errWebAccessToken, "Internal error")
	}
	if s.zips != nil && zipOperation(access.Op) {
		if rpcErr := s.zips.prepare(ctx, token, access); rpcErr != nil {
			s.mu.Lock()
			delete(s.tokens, token)
			s.mu.Unlock()
			return nil, rpcErr
		}
	}
	return token, nil
}

func (s *WebAccessTokens) issue(access webAccess, oneTime bool) (string, error) {
	for {
		id, err := uuid.NewRandom()
		if err != nil {
			return "", err
		}
		token := id.String()
		s.mu.Lock()
		if _, exists := s.tokens[token]; !exists {
			s.tokens[token] = webAccessToken{
				access: access, expiresAt: s.now().Unix() + s.ttl, oneTime: oneTime,
			}
			s.mu.Unlock()
			return token, nil
		}
		s.mu.Unlock()
	}
}

func (s *WebAccessTokens) queryToken(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	token := stringArgument(args, 0)
	if token == nil {
		return nil, rpcError(errBadArgs, "Token should not be null")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.tokens[*token]
	if !exists {
		return nil, nil
	}
	if s.now().Unix() >= entry.expiresAt {
		delete(s.tokens, *token)
		return nil, nil
	}
	if entry.oneTime {
		delete(s.tokens, *token)
	}
	return entry.access, nil
}
