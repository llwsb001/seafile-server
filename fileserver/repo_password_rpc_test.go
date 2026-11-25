package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestEncryptedFixtureV1RPC(t *testing.T) {
	fixtureDir, dataDir := os.Getenv("SEAFILE_FIXTURES_V1"), os.Getenv("SEAFILE_FIXTURES_DATA")
	if fixtureDir == "" && dataDir == "" {
		t.Skip("set SEAFILE_FIXTURES_V1 and SEAFILE_FIXTURES_DATA for C fixture integration")
	}
	if fixtureDir == "" || dataDir == "" {
		t.Fatal("both fixture and extracted data directories are required")
	}
	var manifest struct {
		Repos map[string]string `json:"repos"`
	}
	data, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	db, err := seafserver.OpenSQLiteReadOnly(ctx, filepath.Join(fixtureDir, "db", "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reader := seafserver.NewReader(db, dataDir)
	repoID := manifest.Repos["encrypted"]
	repo, err := reader.Repo(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	filename := frozenFixtureChild(t, reader, repo, repo.RootID, "secret")
	fileID := frozenFixtureChild(t, reader, repo, filename, "secret.txt")
	file, err := reader.File(repo, fileID)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		handlers := seafserver.NewRepoPasswords(reader).RPCHandlers()
		done <- searpc.Serve(ctx, listener, seafserver.RPCService, handlers)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	original := rpcclient
	rpcclient = searpc.Init(path, seafserver.RPCService, 0)
	t.Cleanup(func() { rpcclient = original })

	const owner = "fixture@local"
	if _, appErr := parseCryptKey(nil, repoID, owner, repo.EncVersion); appErr == nil ||
		appErr.Code != http.StatusBadRequest {
		t.Fatal("locked repository should ask the fileserver caller for a password")
	}
	if result, err := rpcclient.Call("seafile_set_passwd", repoID, owner, "123"); err != nil || result != float64(0) {
		t.Fatalf("cannot unlock the encrypted C fixture: %v", err)
	}
	key, appErr := parseCryptKey(nil, repoID, owner, repo.EncVersion)
	if appErr != nil {
		t.Fatalf("fileserver could not parse the key, HTTP status %d", appErr.Code)
	}

	hash := sha256.New()
	for _, block := range file.BlkIDs {
		var ciphertext bytes.Buffer
		if err := reader.ReadBlock(repo, block, &ciphertext); err != nil {
			t.Fatal(err)
		}
		plaintext, err := key.decrypt(ciphertext.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		hash.Write(plaintext)
	}
	const expected = "86f171daa2afa652e1478e750021548b6dec798795a3f4d5d6b48291aef70277"
	if digest := hex.EncodeToString(hash.Sum(nil)); digest != expected {
		t.Fatalf("plaintext SHA-256 = %s, want %s", digest, expected)
	}
}

func frozenFixtureChild(t *testing.T, reader *seafserver.Reader, repo *repomgr.Repo, parent, name string) string {
	t.Helper()
	dir, err := reader.Directory(repo, parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range dir.Entries {
		if entry.Name == name {
			return entry.ID
		}
	}
	t.Fatalf("missing fixture path component %s", name)
	return ""
}
