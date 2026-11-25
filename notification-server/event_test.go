package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

func TestRepoUpdateEventEndpoint(t *testing.T) {
	const fixtureKey = "isolated-notification-test-key"
	const repo = "2526f1ca-635b-4eb4-baf3-0db7fc20ff77"
	const commit = "0123456789012345678901234567890123456789"
	const event = `{"type":"repo-update","content":{"repo_id":"` + repo + `","commit_id":"` + commit + `"}}`
	oldKey, oldSubscriptions := privateKey, subscriptions
	defer func() { privateKey, subscriptions = oldKey, oldSubscriptions }()
	privateKey = fixtureKey
	client := &Client{ID: 1, WCh: make(chan interface{}, 1)}
	subscriptions = map[string]*Subscribers{repo: newSubscribers(client)}
	router := newHTTPRouter()

	tests := []struct {
		name string
		key  string
		ttl  time.Duration
		want int
	}{
		{"authenticated", fixtureKey, 300 * time.Second, http.StatusOK},
		{"expired", fixtureKey, -time.Second, http.StatusBadRequest},
		{"incorrect signature", "incorrect", 300 * time.Second, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(test.ttl))}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(test.key))
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(event))
			request.Header.Set("Authorization", "Token "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("notification receiver returned HTTP %d, want %d", response.Code, test.want)
			}
			if test.want != http.StatusOK {
				select {
				case unexpected := <-client.WCh:
					t.Fatalf("rejected event reached subscribers: %+v", unexpected)
				default:
				}
				return
			}
			select {
			case received := <-client.WCh:
				msg := received.(*Message)
				var payload RepoUpdateEvent
				if err := json.Unmarshal(msg.Content, &payload); err != nil || msg.Type != "repo-update" ||
					payload.RepoID != repo || payload.CommitID != commit {
					t.Fatalf("unexpected notification: %+v, %v", msg, err)
				}
			case <-time.After(time.Second):
				t.Fatal("notification did not reach subscribed client")
			}
		})
	}
}
