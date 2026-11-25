package seafserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestWebAccessTokenExpiresAtUnixSecond(t *testing.T) {
	store := NewWebAccessTokens(3)
	now := time.Unix(100, 900_000_000)
	store.now = func() time.Time { return now }

	info := webAccess{RepoID: "repo", ObjectID: "object", Op: "download", Username: "reader@example.test"}
	token, err := store.issue(info, false)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.issue(info, true)
	if err != nil {
		t.Fatal(err)
	}
	request := []json.RawMessage{json.RawMessage(`"` + token + `"`)}
	now = time.Unix(102, 999_999_999)
	for range 2 {
		got, rpcErr := store.queryToken(context.Background(), request)
		if got != info || rpcErr != nil {
			t.Fatal("reusable token should still yield access before the expiration second")
		}
	}

	now = time.Unix(103, 0)
	got, rpcErr := store.queryToken(context.Background(), request)
	if got != nil || rpcErr != nil {
		t.Fatal("reusable token should expire at the start of the expiration second")
	}
	store.prune()
	if _, exists := store.tokens[stale]; exists {
		t.Fatal("periodic cleanup should remove an expired one-time token")
	}
}
