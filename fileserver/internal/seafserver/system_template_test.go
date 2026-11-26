package seafserver

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

func TestSystemDefaultRepoImportsAndRetainsTemplate(t *testing.T) {
	ctx := context.Background()
	db, dataDir := openFileWriteFixture(t)
	reader := NewReader(db, dataDir)
	template := filepath.Join(dataDir, "library-template")
	for _, folder := range []string{"help", "empty-directory"} {
		if err := os.MkdirAll(filepath.Join(template, folder), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	content := bytes.Repeat([]byte("template contents\n"), 64*1024)
	if err := os.WriteFile(filepath.Join(template, "help", "guide.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, "empty.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	const config = "[fileserver]\nfixed_block_size=1\n"
	quota := NewUserQuota(db, NewServerConfig(db, []byte(config)))
	if err := reader.EnsureSystemDefaultRepo(ctx, db, quota, dataDir); err != nil {
		t.Fatal(err)
	}

	repo := loadDefaultTemplate(t, db, reader)
	if repo.Name != "My Library Template" || repo.Desc != "Template for creating 'My Library' for users" ||
		repo.Size != int64(len(content)) || repo.FileCount != 2 {
		t.Fatalf("unexpected template metadata: %+v", repo)
	}
	dir, err := reader.Directory(repo, repo.RootID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"help", "empty-directory", "empty.txt"} {
		if findEntry(dir.Entries, name) < 0 {
			t.Fatalf("missing template entry %s", name)
		}
	}
	emptyDir := dir.Entries[findEntry(dir.Entries, "empty-directory")]
	emptyFile := dir.Entries[findEntry(dir.Entries, "empty.txt")]
	if !fsmgr.IsDir(emptyDir.Mode) || !fsmgr.IsRegular(emptyFile.Mode) ||
		emptyDir.ID != fsmgr.EmptySha1 || emptyFile.ID != fsmgr.EmptySha1 {
		t.Fatalf("unexpected empty template entries: %+v, %+v", emptyDir, emptyFile)
	}
	help, err := reader.Directory(repo, dir.Entries[findEntry(dir.Entries, "help")].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(help.Entries) != 1 || help.Entries[0].Name != "guide.txt" || help.Entries[0].Modifier != "System" {
		t.Fatalf("invalid nested directory contents: %+v", help.Entries)
	}
	file, err := reader.File(repo, help.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.BlkIDs) != 2 {
		t.Fatalf("guide has %d blocks, want 2", len(file.BlkIDs))
	}
	var actual bytes.Buffer
	for _, id := range file.BlkIDs {
		if err := reader.ReadBlock(repo, id, &actual); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(actual.Bytes(), content) {
		t.Fatal("stored template content differs")
	}

	if err := EnsureFileWriteQueue(ctx, db); err != nil {
		t.Fatal(err)
	}
	handlers, stop := reader.FileWriteRPCHandlers(ctx, db, nil, false, quota, nil, FileMoveLimits{})
	defer stop()
	if _, rpcErr := handlers["seafile_post_empty_file"](ctx, fileWriteRPCArgs(t, repo.ID, "/", "admin.txt", "system")); rpcErr != nil {
		t.Fatalf("edit system template: %v", rpcErr)
	}
	beforeRestart := loadDefaultTemplate(t, db, reader)
	restarted := NewReader(db, dataDir)
	if err := restarted.EnsureSystemDefaultRepo(ctx, db, quota, dataDir); err != nil {
		t.Fatal(err)
	}
	afterRestart := loadDefaultTemplate(t, db, restarted)
	if afterRestart.ID != beforeRestart.ID || afterRestart.HeadCommitID != beforeRestart.HeadCommitID {
		t.Fatal("restart changed the administrator's template")
	}
}

func TestSystemDefaultRepoReplacesStalePointerAndRollsBackPublication(t *testing.T) {
	ctx := context.Background()
	db, dataDir := openFileWriteFixture(t)
	reader := NewReader(db, dataDir)
	quota := NewUserQuota(db, NewServerConfig(db, nil))
	const stale = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	if _, err := db.Exec(`
		INSERT INTO SystemInfo VALUES ('default_repo_id', ?), ('test_setting', 'keep');
		CREATE TRIGGER interrupt_template BEFORE INSERT ON SystemInfo
		WHEN new.info_key = 'default_repo_id'
		BEGIN SELECT RAISE(ABORT, 'interrupt template publication'); END
	`, stale); err != nil {
		t.Fatal(err)
	}
	if err := reader.EnsureSystemDefaultRepo(ctx, db, quota, dataDir); err == nil ||
		!strings.Contains(err.Error(), "interrupt template publication") {
		t.Fatalf("interrupted startup returned %v", err)
	}
	var id string
	if err := db.QueryRow("SELECT info_value FROM SystemInfo WHERE info_key = 'default_repo_id'").Scan(&id); err != nil || id != stale {
		t.Fatalf("pointer after failed startup = %q, %v", id, err)
	}
	for _, table := range []string{"Repo", "Branch", "RepoHead", "RepoOwner", "RepoInfo", "RepoSize", "RepoFileCount"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s has %d rows after rollback, %v", table, count, err)
		}
	}
	if _, err := db.Exec("DROP TRIGGER interrupt_template"); err != nil {
		t.Fatal(err)
	}
	if err := reader.EnsureSystemDefaultRepo(ctx, db, quota, dataDir); err != nil {
		t.Fatal(err)
	}
	repo := loadDefaultTemplate(t, db, reader)
	if repo.ID == stale || repo.RootID != fsmgr.EmptySha1 || repo.FileCount != 0 || repo.Size != 0 {
		t.Fatalf("unexpected replacement template: %+v", repo)
	}
	var setting string
	if err := db.QueryRow("SELECT info_value FROM SystemInfo WHERE info_key = 'test_setting'").Scan(&setting); err != nil || setting != "keep" {
		t.Fatalf("unrelated setting = %q, %v", setting, err)
	}
}

func loadDefaultTemplate(t *testing.T, db *sql.DB, reader *Reader) *repomgr.Repo {
	t.Helper()
	ctx := context.Background()
	const query = `SELECT s.info_value, o.owner_id FROM SystemInfo s
		JOIN RepoOwner o ON o.repo_id = s.info_value WHERE s.info_key = 'default_repo_id'`
	var id, owner string
	if err := db.QueryRow(query).Scan(&id, &owner); err != nil || owner != "System" {
		t.Fatalf("template %q has owner %q, %v", id, owner, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM Repo").Scan(&count); err != nil || count != 1 {
		t.Fatalf("template initialization published %d repositories, %v", count, err)
	}
	repo, err := reader.Repo(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
