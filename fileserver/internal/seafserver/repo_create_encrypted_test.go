package seafserver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const createEncryptionPassword = "synthetic encrypted library 密码"

func encryptedCreateArgs(t *testing.T, id, name string, version int, algorithm, params string) []json.RawMessage {
	t.Helper()
	value, err := encryptionBootstrap(context.Background(), governanceArgs(t, version, id, createEncryptionPassword))
	if err != nil {
		t.Fatal(err)
	}
	material := value.(map[string]any)
	magic := material["magic"]
	var digest, algo, hashParams any
	if algorithm != "" {
		magic = nil
		var salt [32]byte
		if version < 3 {
			copy(salt[:], legacyRepoSalt[:])
		} else {
			decoded, err := hex.DecodeString(material["salt"].(string))
			if err != nil {
				t.Fatal("invalid fixture salt")
			}
			copy(salt[:], decoded)
		}
		digest = hex.EncodeToString(deriveRepoPasswordHash(algorithm, params, salt[:], []byte(id+createEncryptionPassword)))
		algo, hashParams = algorithm, params
	}
	return governanceArgs(t, id, name, "description", testVirtualOwner, magic, material["random-key"],
		material["salt"], version, digest, algo, hashParams)
}

func TestSQLiteCreateEncryptedRepo(t *testing.T) {
	ctx := context.Background()
	db, root := openFileWriteFixture(t)
	r := NewReader(db, root)
	create := r.RepoCreationRPCHandlers(db)["seafile_create_enc_repo"]
	for _, test := range []struct {
		name            string
		version         int
		algorithm, cost string
	}{
		{"v2", 2, "", ""},
		{"v3", 3, "", ""},
		{"v4", 4, "", ""},
		{"v2-pbkdf2", 2, "pbkdf2_sha256", "2500"},
		{"v4-argon2", 4, "argon2id", "2,4096,2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := uuid.NewString()
			args := encryptedCreateArgs(t, id, test.name, test.version, test.algorithm, test.cost)
			if result, err := create(ctx, args); result != id || err != nil {
				t.Fatalf("create result %v, error %v", result, err)
			}
			repo, err := r.Repo(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if repo.Name != test.name || repo.Desc != "description" || !repo.IsEncrypted || repo.EncVersion != test.version ||
				repo.PwdHashAlgo != test.algorithm || repo.PwdHashParams != test.cost || repo.RootID != emptyObjectID {
				t.Fatal("unexpected repository metadata")
			}
			if _, _, ok := unlockRepo(repo, createEncryptionPassword); !ok {
				t.Fatal("published repository cannot be unlocked")
			}
			commit, err := r.Commit(id, repo.HeadCommitID)
			if err != nil || commit.ParentID.Valid || commit.CreatorName != testVirtualOwner || commit.NoLocalHistory != 1 {
				t.Fatal("unexpected initial commit metadata")
			}
			var encrypted int
			if err := db.QueryRow("SELECT is_encrypted FROM RepoInfo WHERE repo_id=?", id).Scan(&encrypted); err != nil || encrypted != 1 {
				t.Fatalf("RepoInfo.is_encrypted = %d, %v", encrypted, err)
			}
			args[4] = json.RawMessage("null")
			if result, err := create(ctx, args); result != nil || err == nil || err.Code != errBadArgs || err.Message != "Repo already exists" {
				t.Fatalf("duplicate create result %v, error %v", result, err)
			}
		})
	}
	id := uuid.NewString()
	args := governanceArgs(t, id, "ordinary", "", testVirtualOwner, nil, nil, nil, -1, nil, nil, nil)
	if result, err := create(ctx, args); result != id || err != nil {
		t.Fatalf("legacy ordinary create result %v, error %v", result, err)
	}
	if repo, err := r.Repo(ctx, id); err != nil || repo.IsEncrypted {
		t.Fatal("legacy ordinary create must not publish encryption metadata")
	}
}

func TestSQLiteCreateEncryptedRepoRejectsBadMaterial(t *testing.T) {
	db, root := openFileWriteFixture(t)
	r := NewReader(db, root)
	create := r.RepoCreationRPCHandlers(db)["seafile_create_enc_repo"]
	for _, test := range []struct {
		name, message string
		position      int
		value         any
		algorithm     string
	}{
		{"id", "Invalid repo id", 0, "invalid", ""},
		{"owner", "Argument should not be null", 3, nil, ""},
		{"short magic", "Bad magic", 4, strings.Repeat("0", 63), ""},
		{"malformed magic", "Bad magic", 4, strings.Repeat("x", 64), ""},
		{"uppercase verifier", "Bad magic", 4, strings.Repeat("A", 64), ""},
		{"malformed key", "Bad random key", 5, strings.Repeat("x", 96), ""},
		{"malformed salt", "Bad salt", 6, strings.Repeat("x", 64), ""},
		{"v1", "Unsupported encryption version", 7, 1, ""},
		{"v5", "Unsupported encryption version", 7, 5, ""},
		{"malformed digest", "Bad pwd_hash", 8, strings.Repeat("x", 64), "pbkdf2_sha256"},
		{"unknown algorithm", "Unsupported encryption algothrims", 9, "pbkdf2", "pbkdf2_sha256"},
		{"PBKDF2 cost", "Invalid password hash parameters", 10, "10000001", "pbkdf2_sha256"},
		{"Argon2 cost", "Invalid password hash parameters", 10, "2,262145,8", "argon2id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := encryptedCreateArgs(t, uuid.NewString(), test.name, 3, test.algorithm, "")
			args[test.position] = governanceArgs(t, test.value)[0]
			if result, err := create(context.Background(), args); result != nil || err == nil || err.Code != errBadArgs || err.Message != test.message {
				t.Fatalf("bad material create result %v, error %v", result, err)
			}
		})
	}
	if state := virtualTestState(t, db); state != ([6]int{}) {
		t.Fatalf("invalid requests created visible state: %v", state)
	}
}

func TestSQLiteCreateEncryptedRepoRetryAfterCommitFailure(t *testing.T) {
	ctx := context.Background()
	db, root := openFileWriteFixture(t)
	r := NewReader(db, root)
	create := r.RepoCreationRPCHandlers(db)["seafile_create_enc_repo"]
	id := uuid.NewString()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;
		CREATE TABLE RequiredParent (id INTEGER PRIMARY KEY);
		CREATE TABLE DeferredChild (parent_id INTEGER REFERENCES RequiredParent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_encrypted_commit AFTER INSERT ON RepoInfo
		BEGIN INSERT INTO DeferredChild (parent_id) VALUES (1); END;`); err != nil {
		t.Fatal(err)
	}
	args := encryptedCreateArgs(t, id, "rollback", 4, "", "")
	if result, err := create(ctx, args); result != nil || err == nil || err.Code != errGeneral {
		t.Fatalf("failed write result %v, error %v", result, err)
	}
	if state := virtualTestState(t, db); state != ([6]int{}) {
		t.Fatalf("failed commit published records: %v", state)
	}
	objects, err := filepath.Glob(filepath.Join(root, "storage", "commits", id, "*", "*"))
	if err != nil || len(objects) != 1 {
		t.Fatalf("expected one unreachable fixture commit, got %d: %v", len(objects), err)
	}
	oldID := filepath.Base(filepath.Dir(objects[0])) + filepath.Base(objects[0])
	old, err := r.Commit(id, oldID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := r.unusedInitialRepoCommit(ctx, id, "retry", "description", testVirtualOwner, old.Ctime)
	if err != nil || next.CommitID == oldID || next.Ctime != old.Ctime+1 {
		t.Fatal("retry must skip an earlier unpublished initial commit")
	}
	if _, err := db.Exec("DROP TRIGGER fail_encrypted_commit"); err != nil {
		t.Fatal(err)
	}
	args = encryptedCreateArgs(t, id, "retry", 4, "", "")
	if result, err := create(ctx, args); result != id || err != nil {
		t.Fatalf("retry result %v, error %v", result, err)
	}
	if repo, err := r.Repo(ctx, id); err != nil || repo.Name != "retry" || repo.RandomKey == old.RandomKey || repo.HeadCommitID == oldID {
		t.Fatal("retry published stale encryption material")
	}
}

func TestSQLiteCreateEncryptedRepoSerializesDuplicateRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, root := openFileWriteFixture(t)
	second, err := OpenSQLiteReadWrite(filepath.Join(root, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	r := NewReader(db, root)
	id := uuid.NewString()
	type result struct {
		name  string
		value any
		err   *searpc.RPCError
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, request := range []struct {
		name   string
		handle searpc.Handler
	}{
		{"first", r.RepoCreationRPCHandlers(db)["seafile_create_enc_repo"]},
		{"second", r.RepoCreationRPCHandlers(second)["seafile_create_enc_repo"]},
	} {
		args := encryptedCreateArgs(t, id, request.name, 2, "", "")
		go func() {
			<-start
			value, err := request.handle(ctx, args)
			results <- result{request.name, value, err}
		}()
	}
	close(start)
	var winner string
	for range 2 {
		answer := <-results
		if answer.value == id && answer.err == nil && winner == "" {
			winner = answer.name
			continue
		}
		if answer.value != nil || answer.err == nil || answer.err.Code != errBadArgs || answer.err.Message != "Repo already exists" {
			t.Fatalf("second create returned %v, %v", answer.value, answer.err)
		}
	}
	repo, err := r.Repo(ctx, id)
	if err != nil || repo.Name != winner {
		t.Fatal("the duplicate request did not preserve the winning repository")
	}
	if state := virtualTestState(t, db); state != ([6]int{1, 1, 1, 1, 1, 0}) {
		t.Fatalf("parallel creates published inconsistent records: %v", state)
	}
}
