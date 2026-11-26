package seafserver

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

func TestTranscodeRejectsCorruptedSourceBlock(t *testing.T) {
	reader := NewReader(nil, t.TempDir())
	from := &fileTree{reader: reader, repo: &repomgr.Repo{StoreID: "source"}}
	to := &fileTree{reader: reader, repo: &repomgr.Repo{StoreID: "target"}}
	checksum := sha1.Sum([]byte("original contents"))
	id := hex.EncodeToString(checksum[:])
	if err := reader.blocks.Write(from.repo.StoreID, id, strings.NewReader("damaged contents"), true); err != nil {
		t.Fatal(err)
	}
	copy := libraryCopy{from: from, to: to, targetKey: transferKey{version: 4, key: make([]byte, 32), iv: make([]byte, 16)}}
	if _, err := copy.transcodeBlock(id); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("transcode with a damaged source block returned %v", err)
	}
}

func TestTransferUsesUnexpiredKeysForTheRequestUser(t *testing.T) {
	const owner = "owner@example.test"
	source := &repomgr.Repo{ID: "source", IsEncrypted: true, EncVersion: 3}
	target := &repomgr.Repo{ID: "target", IsEncrypted: true, EncVersion: 4}
	passwords := NewRepoPasswords(nil)
	now := time.Now().Unix()
	sourceKey, targetKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	iv := make([]byte, 16)
	passwords.cache(repoPasswordID{repo: source.ID, user: owner}, sourceKey, bytes.Clone(iv), now)
	passwords.cache(repoPasswordID{repo: target.ID, user: "other@example.test"}, bytes.Clone(targetKey), bytes.Clone(iv), now)
	passwords.cache(repoPasswordID{repo: target.ID, user: owner}, bytes.Clone(targetKey), bytes.Clone(iv), now-repoPasswordLifetime)
	newCopier := func() *libraryCopy {
		return &libraryCopy{from: &fileTree{repo: source, user: owner}, to: &fileTree{repo: target, user: owner}}
	}
	expired := newCopier()
	if rpcErr := expired.loadKeys(passwords); rpcErr == nil || rpcErr.Message != "Internal error when copy or move" {
		t.Fatalf("transfer accepted a missing or expired target key: %v", rpcErr)
	}
	expired.clearKeys()

	passwords.cache(repoPasswordID{repo: target.ID, user: owner}, targetKey, bytes.Clone(iv), now)
	unlocked := newCopier()
	if rpcErr := unlocked.loadKeys(passwords); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if !bytes.Equal(unlocked.sourceKey.key, sourceKey) || !bytes.Equal(unlocked.targetKey.key, targetKey) {
		t.Fatal("transfer did not obtain the keys for both libraries")
	}
	unlocked.clearKeys()
	if key, _ := passwords.copyKey(target.ID, owner); !bytes.Equal(key, targetKey) {
		t.Fatal("clearing the transfer keys changed the shared credential cache")
	}
}
