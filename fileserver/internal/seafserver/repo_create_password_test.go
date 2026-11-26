package seafserver

import (
	"context"
	"testing"
)

func TestSQLiteCreateRepoWithPassword(t *testing.T) {
	ctx := context.Background()
	db, data := openFileWriteFixture(t)
	create := NewReader(db, data).RepoCreationRPCHandlers(db)["seafile_create_repo"]
	keys := make(map[string]bool)
	pbkdf2Cost, emptyCost, argon2Cost := "2500", "", "2,4096,2"
	for _, test := range []struct {
		name      string
		version   int
		algorithm string
		params    *string
	}{
		{name: "v2-magic", version: 2},
		{name: "v3-magic", version: 3},
		{name: "v4-magic", version: 4},
		{name: "v2-pbkdf2", version: 2, algorithm: "pbkdf2_sha256", params: &pbkdf2Cost},
		{name: "v3-default-cost", version: 3, algorithm: "pbkdf2_sha256"},
		{name: "v3-empty-cost", version: 3, algorithm: "pbkdf2_sha256", params: &emptyCost},
		{name: "v4-argon2", version: 4, algorithm: "argon2id", params: &argon2Cost},
	} {
		t.Run(test.name, func(t *testing.T) {
			var algorithm *string
			if test.algorithm != "" {
				algorithm = &test.algorithm
			}
			result, rpcErr := create(ctx,
				governanceArgs(t, test.name, "description", testVirtualOwner, createEncryptionPassword, test.version, algorithm, test.params))
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			id := result.(string)
			if !validRepoID(id) {
				t.Fatal("create did not generate a repository ID")
			}
			reader := NewReader(db, data)
			repo, err := reader.Repo(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if repo.Name != test.name || repo.Desc != "description" || !repo.IsEncrypted || repo.EncVersion != test.version ||
				repo.PwdHashAlgo != test.algorithm || repo.HasPwdHashParams != (test.params != nil) ||
				(test.params != nil && repo.PwdHashParams != *test.params) {
				t.Fatal("unexpected repository metadata")
			}
			if (test.version < 3 && repo.Salt != "") || (test.version >= 3 && len(repo.Salt) != 64) ||
				len(repo.RandomKey) != 96 || keys[repo.RandomKey] ||
				(test.algorithm == "" && (len(repo.Magic) != 64 || repo.PwdHash != "")) ||
				(test.algorithm != "" && (repo.Magic != "" || len(repo.PwdHash) != 64)) {
				t.Fatal("unexpected encryption material")
			}
			keys[repo.RandomKey] = true
			if key, iv, ok := unlockRepo(repo, createEncryptionPassword); !ok || len(key) != 32 || len(iv) != 16 {
				t.Fatal("creds cannot unlock the published repository")
			}
			if _, _, ok := unlockRepo(repo, "wrong fixture password"); ok {
				t.Fatal("wrong password unlocked the repository")
			}
			var encrypted int
			if err := db.QueryRow("SELECT is_encrypted FROM RepoInfo WHERE repo_id = ?", id).Scan(&encrypted); err != nil || encrypted != 1 {
				t.Fatalf("RepoInfo.is_encrypted = %d, %v", encrypted, err)
			}
		})
	}
}

func TestSQLiteCreateRepoWithPasswordRejectsBadArguments(t *testing.T) {
	db, data := openFileWriteFixture(t)
	create := NewReader(db, data).RepoCreationRPCHandlers(db)["seafile_create_repo"]
	for _, test := range []struct {
		name      string
		password  any
		version   int
		algorithm any
		params    any
	}{
		{"legacy encryption version", createEncryptionPassword, 1, nil, nil},
		{"unencrypted version with password", createEncryptionPassword, -1, nil, nil},
		{"unsupported future version", createEncryptionPassword, 5, nil, nil},
		{"empty password", "", 2, nil, nil},
		{"non-string password", 123, 2, nil, nil},
		{"nul in password", "first\x00second", 2, nil, nil},
		{"unknown hash algorithm", createEncryptionPassword, 3, "unsupported", ""},
		{"invalid hash params", createEncryptionPassword, 4, "argon2id", "2,262145,8"},
		{"non-string hash params", createEncryptionPassword, 3, "pbkdf2_sha256", 2500},
		{"nul in hash params", createEncryptionPassword, 4, "argon2id", "1\x00,4096,2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, rpcErr := create(context.Background(),
				governanceArgs(t, test.name, "", testVirtualOwner, test.password, test.version, test.algorithm, test.params))
			if result != nil || rpcErr == nil || rpcErr.Code != errBadArgs {
				t.Fatalf("invalid encryption arguments returned %v, %v", result, rpcErr)
			}
		})
	}
	if state := virtualTestState(t, db); state != ([6]int{}) {
		t.Fatalf("invalid requests created visible state: %v", state)
	}
}
