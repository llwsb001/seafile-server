package seafserver

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

func TestBlockDiagnostics(t *testing.T) {
	ctx := context.Background()
	db, dataDir := openFileWriteFixture(t)
	writer := NewReader(db, dataDir)
	const owner = "owner@example.test"
	created, rpcErr := writer.createRepo(ctx, db, fileWriteRPCArgs(t, "blocks", "fixture", owner))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	origin := created.(string)
	config := NewServerConfig(db, nil)
	fileWrites, stop := writer.FileWriteRPCHandlers(ctx, db, nil, false, NewUserQuota(db, config), NewRepoPasswords(writer), FileMoveLimits{})
	defer stop()
	if _, rpcErr := fileWrites["seafile_mkdir_with_parents"](ctx, fileWriteRPCArgs(t, origin, "/", "view", owner)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	created, rpcErr = writer.createVirtualRepo(ctx, db, fileWriteRPCArgs(t, origin, "/view", "view", "fixture", owner, ""))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	view := created.(string)
	const (
		first      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		second     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		absent     = "ffffffffffffffffffffffffffffffffffffffff"
		badFS      = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		unknown    = "00000000-1000-4000-8000-000000000000"
		listRPC    = "seafile_list_file_blocks"
		missingRPC = "seafile_check_repo_blocks_missing"
	)
	file, err := fsmgr.NewSeafile(1, 4, []string{first, second, first, first})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	if err := file.ToData(&compressed); err != nil {
		t.Fatal(err)
	}
	if err := writer.fs.Write(origin, file.FileID, &compressed, false); err != nil {
		t.Fatal(err)
	}
	if err := writer.fs.Write(origin, badFS, strings.NewReader("broken compressed metadata"), false); err != nil {
		t.Fatal(err)
	}
	if err := writer.blocks.Write(origin, first, strings.NewReader("synthetic block"), false); err != nil {
		t.Fatal(err)
	}

	readonly, err := OpenSQLiteReadOnly(ctx, filepath.Join(dataDir, "seafile.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { readonly.Close() })
	handlers := NewReader(readonly, dataDir).RPCHandlers(config)
	for _, test := range []struct {
		name, method string
		args         []any
		want         string
		errCode      int
		errText      string
	}{
		{"all in wire order with duplicates", listRPC, []any{origin, file.FileID, 0, 0},
			first + "\n" + second + "\n" + first + "\n" + first + "\n", 0, ""},
		{"virtual file uses origin FS", listRPC, []any{view, file.FileID, 0, 0},
			first + "\n" + second + "\n" + first + "\n" + first + "\n", 0, ""},
		{"negative start", listRPC, []any{origin, file.FileID, -5, 1}, first + "\n", 0, ""},
		{"page", listRPC, []any{origin, file.FileID, 1, 2}, second + "\n" + first + "\n", 0, ""},
		{"negative limit", listRPC, []any{origin, file.FileID, 2, -1}, first + "\n" + first + "\n", 0, ""},
		{"after end", listRPC, []any{origin, file.FileID, 8, 1}, "", 0, ""},
		{"C int range", listRPC, []any{origin, file.FileID, 1, 2147483647}, "", 0, ""},
		{"empty file", listRPC, []any{origin, fsmgr.EmptySha1, 0, 0}, "", 0, ""},
		{"missing metadata", listRPC, []any{origin, absent, 0, 0}, "", errBadDirID, "Bad file id"},
		{"corrupted metadata", listRPC, []any{origin, badFS, 0, 0}, "", errBadDirID, "Bad file id"},
		{"invalid file id", listRPC, []any{origin, strings.ToUpper(first), 0, 0}, "", errBadDirID, "Bad file id"},
		{"invalid repo id", listRPC, []any{nil, file.FileID, 0, 0}, "", errBadDirID, "Bad repo id"},
		{"unknown repo", listRPC, []any{unknown, file.FileID, 0, 0}, "", errBadArgs, "Bad repo id"},
		{"missing with duplicates", missingRPC, []any{origin, `["` + first + `", "` + second + `", "` + second + `"]`},
			`["` + second + `", "` + second + `"]`, 0, ""},
		{"virtual probe checks logical store", missingRPC, []any{view, `["` + first + `"]`},
			`["` + first + `"]`, 0, ""},
		{"empty array", missingRPC, []any{origin, `[]`}, "[]", 0, ""},
		{"JSON object", missingRPC, []any{origin, `{"key":["` + second + `"]}`}, "[]", 0, ""},
		{"keep invalid strings and skip non-strings", missingRPC,
			[]any{origin, `[false, null, 1, {}, [], "", "bad", "` + strings.ToUpper(first) + `"]`},
			`["", "bad", "` + strings.ToUpper(first) + `"]`, 0, ""},
		{"C string escaping", missingRPC, []any{origin, `["<>&\"","中文","escaped\\u2028:actual\u2028"]`},
			`["<>&\"", "中文", "escaped\\u2028:actual` + "\u2028" + `"]`, 0, ""},
		{"JSON string is not a collection", missingRPC, []any{origin, `"not an array"`}, "", errBadArgs, "Bad arguments"},
		{"JSON syntax error", missingRPC, []any{origin, `["bad"}`}, "", errBadArgs, "Bad arguments"},
		{"missing JSON argument", missingRPC, []any{origin, nil}, "", errBadArgs, "Bad arguments"},
		{"unknown repo", missingRPC, []any{unknown, `[]`}, "", errBadArgs, "Repo not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var args []json.RawMessage
			for _, arg := range test.args {
				encoded, err := json.Marshal(arg)
				if err != nil {
					t.Fatal(err)
				}
				args = append(args, encoded)
			}
			value, rpcErr := handlers[test.method](ctx, args)
			if test.errCode == 0 {
				if value != test.want || rpcErr != nil {
					t.Fatalf("RPC returned %q, %v, want %q", value, rpcErr, test.want)
				}
			} else if value != nil || rpcErr == nil || rpcErr.Code != test.errCode || rpcErr.Message != test.errText {
				t.Fatalf("RPC returned %q, %v, want error %d %q", value, rpcErr, test.errCode, test.errText)
			}
		})
	}
}
