package seafserver

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

type fixtureRepo struct {
	ID        string `json:"repo_id"`
	Name      string `json:"name"`
	Owner     string `json:"owner"`
	Head      string `json:"head_commit_id"`
	Origin    string `json:"origin_repo_id"`
	Path      string `json:"origin_path"`
	Encrypted int    `json:"encrypted"`
}

type fixtureObject struct {
	Kind   string `json:"type"`
	ID     string `json:"id"`
	RepoID string `json:"repo_id"`
	SHA256 string `json:"sha256_content"`
}

func TestReadFixturesV1(t *testing.T) {
	fixtureDir := os.Getenv("SEAFILE_FIXTURES_V1")
	dataDir := os.Getenv("SEAFILE_FIXTURES_DATA")
	if fixtureDir == "" && dataDir == "" {
		t.Skip("set SEAFILE_FIXTURES_V1 and SEAFILE_FIXTURES_DATA for C fixture integration")
	}
	if fixtureDir == "" || dataDir == "" {
		t.Fatal("both fixture and extracted data directories are required")
	}
	ctx := context.Background()
	db, err := OpenSQLiteReadOnly(ctx, filepath.Join(fixtureDir, "db", "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reader := NewReader(db, dataDir)

	data, err := os.ReadFile(filepath.Join(fixtureDir, "repos.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expectedRepos []fixtureRepo
	if err := json.Unmarshal(data, &expectedRepos); err != nil {
		t.Fatal(err)
	}
	objects, err := os.Open(filepath.Join(fixtureDir, "expected", "objects.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	decoder := json.NewDecoder(objects)
	blockDigests := make(map[string]string)
	var commits int
	for {
		var object fixtureObject
		if err := decoder.Decode(&object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		switch object.Kind {
		case "blocks":
			blockDigests[object.ID] = object.SHA256
		case "commits":
			commit, err := reader.Commit(object.RepoID, object.ID)
			if err != nil {
				t.Fatal(err)
			}
			if commit.CommitID != object.ID || commit.RepoID != object.RepoID {
				t.Fatalf("wrong commit identity for %s/%s", object.RepoID, object.ID)
			}
			commits++
		}
	}
	if commits != 27 || len(blockDigests) != 8 {
		t.Fatalf("v1 fixture has %d commits and %d blocks", commits, len(blockDigests))
	}
	var repos, dirs, files int
	for _, expected := range expectedRepos {
		if expected.Owner == "System" {
			continue
		}
		t.Run(expected.Name, func(t *testing.T) {
			repo, err := reader.Repo(ctx, expected.ID)
			if err != nil {
				t.Fatal(err)
			}
			if repo.HeadCommitID != expected.Head || repo.Name != expected.Name || repo.IsEncrypted != (expected.Encrypted == 1) {
				t.Fatalf("wrong metadata for %s: %+v", expected.ID, repo)
			}
			if expected.Origin == "" {
				if repo.StoreID != repo.ID || repo.VirtualInfo != nil {
					t.Fatalf("wrong store for non-virtual repo %s", repo.ID)
				}
			} else if repo.VirtualInfo == nil || repo.StoreID != expected.Origin || repo.VirtualInfo.Path != expected.Path {
				t.Fatalf("wrong virtual repo mapping for %s: %+v", repo.ID, repo.VirtualInfo)
			}
			nDirs, nFiles := checkFixtureTree(t, reader, repo, repo.RootID, blockDigests)
			dirs += nDirs
			files += nFiles
			repos++
			t.Logf("%d directories, %d files", nDirs, nFiles)
		})
	}
	if repos != 3 || dirs != 15 || files != 8 {
		t.Fatalf("v1 coverage: %d repos, %d directories, %d files", repos, dirs, files)
	}
}

func checkFixtureTree(t *testing.T, reader *Reader, repo *repomgr.Repo, dirID string, blockDigests map[string]string) (int, int) {
	t.Helper()
	dir, err := reader.Directory(repo, dirID)
	if err != nil {
		t.Fatal(err)
	}
	var dirs, files int
	for _, entry := range dir.Entries {
		switch {
		case fsmgr.IsDir(entry.Mode):
			subdirs, subfiles := checkFixtureTree(t, reader, repo, entry.ID, blockDigests)
			dirs += subdirs + 1
			files += subfiles
		case fsmgr.IsRegular(entry.Mode):
			file, err := reader.File(repo, entry.ID)
			if err != nil {
				t.Fatal(err)
			}
			if int64(file.FileSize) != entry.Size {
				t.Fatalf("file %q size %d, want %d", entry.Name, file.FileSize, entry.Size)
			}
			for _, blockID := range file.BlkIDs {
				sha1Hash, sha256Hash := sha1.New(), sha256.New()
				if err := reader.ReadBlock(repo, blockID, io.MultiWriter(sha1Hash, sha256Hash)); err != nil {
					t.Fatal(err)
				}
				if hex.EncodeToString(sha1Hash.Sum(nil)) != blockID || hex.EncodeToString(sha256Hash.Sum(nil)) != blockDigests[blockID] {
					t.Fatalf("wrong block bytes %s in %q", blockID, entry.Name)
				}
			}
			files++
		default:
			t.Fatalf("unexpected entry type for %q: %o", entry.Name, entry.Mode)
		}
	}
	return dirs, files
}
