package seafserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

func TestNotifyRepoUpdate(t *testing.T) {
	const key = "isolated-notification-test-key"
	const repo = "2526f1ca-635b-4eb4-baf3-0db7fc20ff77"
	const commit = "0123456789012345678901234567890123456789"
	const event = `{"type":"repo-update","content":{"repo_id":"` + repo + `","commit_id":"` + commit + `"}}`
	start := time.Now().Unix()
	var requests atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusOK)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/internal/events" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected notification request: %s %s %s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if agent := r.Header.Get("User-Agent"); agent != "Seafile Server" {
			t.Errorf("unexpected user agent %q", agent)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != event {
			t.Errorf("unexpected event payload %s, %v", body, err)
		}
		auth, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Token ")
		if !ok {
			t.Error("missing notification authorization")
		}
		claims := jwt.MapClaims{}
		_, err = jwt.ParseWithClaims(auth, claims, func(*jwt.Token) (any, error) {
			return []byte(key), nil
		}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
		if err != nil {
			t.Errorf("failed to verify notification JWT: %v", err)
		} else {
			expires, err := claims.GetExpirationTime()
			if err != nil || len(claims) != 1 || expires.Unix() < start+300 || expires.Unix() > time.Now().Unix()+300 {
				t.Errorf("unexpected notification JWT claims: %+v", claims)
			}
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer receiver.Close()
	ctx := context.Background()
	t.Setenv("INNER_NOTIFICATION_SERVER_URL", receiver.URL+"/internal")
	t.Setenv("ENABLE_NOTIFICATION_SERVER", "1")
	t.Setenv("JWT_PRIVATE_KEY", "")
	if err := CheckNotificationConfig(); err != nil {
		t.Fatalf("disabled notification should allow startup: %v", err)
	}
	if err := NotifyRepoUpdate(ctx, repo, commit); err != nil || requests.Load() != 0 {
		t.Fatalf("disabled notification made an outbound request: %v", err)
	}
	t.Setenv("ENABLE_NOTIFICATION_SERVER", "true")
	if err := CheckNotificationConfig(); err == nil {
		t.Fatal("enabled notification accepted an empty signing key")
	}
	if err := NotifyRepoUpdate(ctx, repo, commit); err == nil || requests.Load() != 0 {
		t.Fatal("notification without a signing key did not fail before sending")
	}
	t.Setenv("JWT_PRIVATE_KEY", key)
	if err := CheckNotificationConfig(); err != nil {
		t.Fatal(err)
	}
	if err := NotifyRepoUpdate(ctx, repo, commit); err != nil || requests.Load() != 1 {
		t.Fatalf("enabled notification failed: %v (requests %d)", err, requests.Load())
	}

	status.Store(http.StatusServiceUnavailable)
	if err := NotifyRepoUpdate(ctx, repo, commit); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected HTTP failure for a non-200 response, got %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := NotifyRepoUpdate(cancelled, repo, commit); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled notification to fail, got %v", err)
	}
	t.Setenv("INNER_NOTIFICATION_SERVER_URL", "")
	if err := NotifyRepoUpdate(ctx, repo, commit); err != nil || requests.Load() != 2 {
		t.Fatalf("notification without a URL made an outbound request: %v", err)
	}
}
