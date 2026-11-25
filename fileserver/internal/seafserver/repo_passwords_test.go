package seafserver

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestRepoPasswordReap(t *testing.T) {
	passwords := NewRepoPasswords(nil)
	repo := "00000000-0000-0000-0000-000000000003"
	owner := repoPasswordID{repo: repo, user: "owner@example.test"}
	other := repoPasswordID{repo: repo, user: "other@example.test"}
	key, iv := []byte{1, 2, 3}, []byte{4, 5, 6}
	const start = int64(1000)
	passwords.cache(owner, key, iv, start)
	passwords.cache(other, []byte{7}, []byte{8}, start+1)
	args := []json.RawMessage{json.RawMessage(`"` + repo + `"`), json.RawMessage(`"` + owner.user + `"`)}
	ctx := context.Background()

	passwords.reap(start + 3599)
	if status, rpcErr := passwords.isSet(ctx, args); status != 1 || rpcErr != nil {
		t.Fatal("key expired before its one-hour lifetime")
	}
	if _, rpcErr := passwords.getKey(ctx, args); rpcErr != nil {
		t.Fatal("key should be readable before expiry")
	}
	passwords.reap(start + 3600)
	if status, rpcErr := passwords.isSet(ctx, args); status != 0 || rpcErr != nil {
		t.Fatal("reap must expire the entry at the original deadline")
	}
	if result, rpcErr := passwords.getKey(ctx, args); result != nil || rpcErr == nil ||
		rpcErr.Code != errRepoPassword || rpcErr.Message != "Password was not set" {
		t.Fatal("expired key should have the same response as a missing key")
	}
	if _, exists := passwords.keys[other]; !exists {
		t.Fatal("reaping the owner's key also removed the other user")
	}
	if !bytes.Equal(key, []byte{0, 0, 0}) || !bytes.Equal(iv, []byte{0, 0, 0}) {
		t.Fatal("the removed entry still retains its key or IV")
	}
}
