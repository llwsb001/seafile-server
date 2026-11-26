package seafserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"syscall"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

func TestFileAndDirectorySizes(t *testing.T) {
	ctx := context.Background()
	r := NewReader(nil, t.TempDir())
	handlers := r.RPCHandlers(nil)
	const origin = "409baaaa-e00f-4911-a261-57fe5a67a7b6"
	const virtual = "84784f21-3921-4612-acb0-751665f89b54"
	block1, block2, missing := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	const contentSize = 9000000
	file, err := fsmgr.NewSeafile(1, contentSize, []string{block1, block2})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := file.ToData(&data); err != nil {
		t.Fatal(err)
	}
	if err := r.fs.Write(origin, file.FileID, &data, false); err != nil {
		t.Fatal(err)
	}
	writeDir := func(entries ...*fsmgr.SeafDirent) string {
		t.Helper()
		dir, err := fsmgr.NewSeafdir(1, entries)
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		if err := dir.ToData(&data); err != nil {
			t.Fatal(err)
		}
		if err := r.fs.Write(origin, dir.DirID, &data, false); err != nil {
			t.Fatal(err)
		}
		return dir.DirID
	}
	child := writeDir(
		&fsmgr.SeafDirent{Mode: syscall.S_IFREG, Name: "payload", ID: file.FileID, Size: contentSize},
		&fsmgr.SeafDirent{Mode: syscall.S_IFREG, Name: "missing-fs-object", ID: missing, Size: 12},
	)
	root := writeDir(
		&fsmgr.SeafDirent{Mode: syscall.S_IFDIR, Name: "one", ID: child},
		&fsmgr.SeafDirent{Mode: syscall.S_IFDIR, Name: "two", ID: child},
	)
	broken := writeDir(&fsmgr.SeafDirent{Mode: syscall.S_IFDIR, Name: "missing-dir", ID: missing})

	legacyFile, legacyDir := strings.Repeat("d", 40), strings.Repeat("e", 40)
	for id, encoded := range map[string]string{
		legacyFile: "00000001" + "0000000000000100" + block1,
		legacyDir:  "00000003" + "000081a4" + hex.EncodeToString([]byte(legacyFile)) + "00000001" + "78",
	} {
		data, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.fs.Write(origin, id, bytes.NewReader(data), false); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		method, store, version, id string
		size                       any
		code                       int
	}{
		{"seafile_get_file_size", origin, "1", file.FileID, int64(contentSize), 0},
		{"seafile_get_file_size", virtual, "1", file.FileID, -1, errInternal},
		{"seafile_get_file_size", origin, "1", emptyObjectID, int64(0), 0},
		{"seafile_get_file_size", origin, "1", "bad-id", -1, errBadArgs},
		{"seafile_get_dir_size", origin, "1", root, int64(2 * (contentSize + 12)), 0},
		{"seafile_get_dir_size", origin, "1", broken, -1, errBadArgs},
		{"seafile_get_dir_size", origin, "1", emptyObjectID, int64(0), 0},
		{"seafile_get_dir_size", origin, "0", legacyDir, int64(256), 0},
	} {
		args := fileWriteRPCArgs(t, tt.store, "", tt.id)
		args[1] = json.RawMessage(tt.version)
		got, rpcErr := handlers[tt.method](ctx, args)
		code := 0
		if rpcErr != nil {
			code = rpcErr.Code
		}
		if got != tt.size || code != tt.code {
			t.Fatalf("%s(%s, %s) = (%v, %v), want (%v, %d)", tt.method, tt.store, tt.id, got, rpcErr, tt.size, tt.code)
		}
	}
}
