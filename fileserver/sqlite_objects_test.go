package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/haiwen/seafile-server/fileserver/blockmgr"
	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

func TestSQLiteWriteFileObjects(t *testing.T) {
	originalDBType, originalSize, originalThreads := option.DBType, option.FixedBlockSize, option.MaxIndexingThreads
	option.DBType, option.FixedBlockSize, option.MaxIndexingThreads = "sqlite", 8<<20, 2
	t.Cleanup(func() {
		option.DBType, option.FixedBlockSize, option.MaxIndexingThreads = originalDBType, originalSize, originalThreads
	})

	dataDir := t.TempDir()
	// An explicitly requested export goes in a new directory so an integration
	// test cannot publish into an existing repository installation.
	if export := os.Getenv("SEAFILE_TEST_OBJECT_EXPORT"); export != "" {
		if err := os.Mkdir(export, 0700); err != nil {
			t.Fatal(err)
		}
		dataDir = export
	}
	blockmgr.Init(dataDir, dataDir)
	commitmgr.Init(dataDir, dataDir)
	fsmgr.Init(dataDir, dataDir, 1<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	readFile := func(t *testing.T, path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	writeFile := func(t *testing.T, path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	hashObject := func(data []byte) string {
		sum := sha1.Sum(data)
		return hex.EncodeToString(sum[:])
	}
	var heads = make(map[string]string)
	for _, variant := range []struct {
		name, id string
		key      *seafileCrypt
	}{
		{name: "plain", id: "abf220bb-fcb0-46a3-aed7-df07dcf918db"},
		{name: "encrypted", id: "e482c52d-7de4-4cdc-8858-c2cc4050ee92", key: &seafileCrypt{
			key: bytes.Repeat([]byte{0x22}, 32), iv: bytes.Repeat([]byte{0x33}, 16), version: 2,
		}},
	} {
		t.Run(variant.name, func(t *testing.T) {
			const owner, filename = "object-fixture@example.test", "海边 & 检查.txt"
			repo := &repomgr.Repo{
				ID: variant.id, StoreID: variant.id, Name: variant.name,
				Desc: "Description preserved across a fileserver commit.", Version: 1,
			}
			if variant.key != nil {
				repo.IsEncrypted, repo.EncVersion = true, 2
				repo.Magic, repo.RandomKey = strings.Repeat("0a", 32), strings.Repeat("0b", 48)
			}
			parent := commitmgr.NewCanonicalCommit(repo.ID, "", fsmgr.EmptySha1, owner, "Created library", 1700000000)
			repomgr.RepoToCommit(repo, parent)
			parent.RepoDesc, parent.RepoCategory, parent.NoLocalHistory = repo.Desc, "fixtures", 1
			if err := commitmgr.Save(parent); err != nil {
				t.Fatal(err)
			}

			plain := append(bytes.Repeat([]byte{'x'}, int(option.FixedBlockSize)), []byte("final block")...)
			filePath := filepath.Join(t.TempDir(), "input")
			writeFile(t, filePath, plain)
			result := make(chan *indexFileResult, 1)
			if err := indexFileWorker(result, &indexFileRequest{
				ctx: ctx, repoID: repo.StoreID, version: repo.Version, filePath: filePath, cryptKey: variant.key,
			}); err != nil {
				t.Fatal(err)
			}
			file := <-result
			if file.err != nil || file.size != int64(len(plain)) {
				t.Fatalf("index result: %v, %d", file.err, file.size)
			}

			fsObject := func(id string) []byte {
				t.Helper()
				encoded := readFile(t, filepath.Join(dataDir, "storage", "fs", repo.StoreID, id[:2], id[2:]))
				reader, err := zlib.NewReader(bytes.NewReader(encoded))
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				raw, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if hashObject(raw) != id {
					t.Fatalf("FS ID does not match stored object %s", id)
				}
				return raw
			}

			var expectedBlocks []string
			for _, content := range [][]byte{plain[:option.FixedBlockSize], plain[option.FixedBlockSize:]} {
				stored := content
				if variant.key != nil {
					var err error
					stored, err = variant.key.encrypt(bytes.Clone(content))
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Equal(stored, content) {
						t.Fatal("an encrypted block contains plaintext")
					}
				}
				id := hashObject(stored)
				var actual bytes.Buffer
				if err := blockmgr.Read(repo.StoreID, id, &actual); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(actual.Bytes(), stored) {
					t.Fatalf("stored block %s does not match indexed bytes", id)
				}
				expectedBlocks = append(expectedBlocks, id)
			}
			wantFile := fmt.Sprintf(`{"block_ids": ["%s", "%s"], "size": %d, "type": 1, "version": 1}`,
				expectedBlocks[0], expectedBlocks[1], len(plain))
			if got := string(fsObject(file.fileID)); got != wantFile {
				t.Fatalf("file object JSON = %s", got)
			}

			writeFile(t, filePath, nil)
			if err := indexFileWorker(result, &indexFileRequest{
				ctx: ctx, repoID: repo.StoreID, version: repo.Version, filePath: filePath, cryptKey: variant.key,
			}); err != nil {
				t.Fatal(err)
			}
			empty := <-result
			if empty.err != nil || empty.fileID != fsmgr.EmptySha1 || empty.size != 0 {
				t.Fatalf("empty file was not represented by the zero ID: %+v", empty)
			}
			root, err := fsmgr.NewSeafdir(repo.Version, []*fsmgr.SeafDirent{
				fsmgr.NewDirent(file.fileID, filename, syscall.S_IFREG|0644, 1700000000, owner, file.size),
				fsmgr.NewDirent(empty.fileID, "empty.txt", syscall.S_IFREG|0644, 1700000000, owner, 0),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := fsmgr.SaveSeafdir(repo.StoreID, root); err != nil {
				t.Fatal(err)
			}
			if got := string(fsObject(root.DirID)); !strings.Contains(got, filename) {
				t.Fatalf("directory JSON does not contain its original filename: %s", got)
			}

			commit := newFileServerCommit(repo, parent, root.DirID, owner, "Uploaded files")
			if err := commitmgr.Save(commit); err != nil {
				t.Fatal(err)
			}
			loaded, err := commitmgr.Load(repo.ID, commit.CommitID)
			if err != nil {
				t.Fatal(err)
			}
			var ctime [8]byte
			binary.BigEndian.PutUint64(ctime[:], uint64(loaded.Ctime))
			hashInput := []byte(loaded.RootID + "\x00" + loaded.CreatorID + "\x00" + owner + "\x00Uploaded files\x00")
			if got := hashObject(append(hashInput, ctime[:]...)); got != loaded.CommitID {
				t.Fatalf("commit ID = %s, reconstructed C ID = %s", loaded.CommitID, got)
			}
			if !loaded.ParentID.Valid || loaded.ParentID.String != parent.CommitID || loaded.RootID != root.DirID ||
				loaded.RepoDesc != repo.Desc || loaded.RepoCategory != parent.RepoCategory || loaded.NoLocalHistory != 1 {
				t.Fatal("stored commit lost its parent, tree, or repository metadata")
			}
			heads[repo.ID] = loaded.CommitID
		})
	}
	manifest, err := json.Marshal(heads)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dataDir, "heads.json"), manifest)
}
