package seafserver

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

func TestEncryptionBootstrap(t *testing.T) {
	const repoID = "994251af-776b-4aaf-ad08-17b4c151d0fe"
	const password = "synthetic encryption password"
	handler := NewRepoPasswords(nil).RPCHandlers()["generate_magic_and_random_key"]
	args := []json.RawMessage{nil, json.RawMessage(strconv.Quote(repoID)), json.RawMessage(strconv.Quote(password))}
	for _, version := range []int{2, 3, 4} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			args[0] = json.RawMessage(strconv.Itoa(version))
			value, err := handler(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			material := value.(map[string]any)
			magic := material["magic"].(string)
			wrapped := material["random-key"].(string)
			salt, _ := material["salt"].(string)
			if len(magic) != 64 || len(wrapped) != 96 || (version == 2 && material["salt"] != nil) ||
				(version >= 3 && len(salt) != 64) || material["repo-id"] != repoID || material["enc-version"] != version {
				t.Fatal("unexpected encryption material format")
			}
			repo := &repomgr.Repo{ID: repoID, EncVersion: version, Magic: magic, RandomKey: wrapped, Salt: salt}
			if key, iv, ok := unlockRepo(repo, password); !ok || len(key) != 32 || len(iv) != 16 {
				t.Fatal("generated material cannot unlock the repository")
			}
			if _, _, ok := unlockRepo(repo, "different synthetic password"); ok {
				t.Fatal("unlocked with a different password")
			}
			again, err := handler(context.Background(), args)
			if err != nil || again.(map[string]any)["random-key"] == wrapped {
				t.Fatal("repeated call did not produce a fresh random key")
			}
		})
	}
	for _, version := range []int{-1, 0, 1, 5} {
		args[0] = json.RawMessage(strconv.Itoa(version))
		if result, err := handler(context.Background(), args); result != nil || err == nil || err.Code != errBadArgs {
			t.Fatalf("unsupported version %d was accepted", version)
		}
	}
	args[0], args[2] = json.RawMessage("2"), json.RawMessage("null")
	if result, err := handler(context.Background(), args); result != nil || err == nil || err.Code != errBadArgs {
		t.Fatal("null password was accepted")
	}
}
