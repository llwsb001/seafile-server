package fsmgr

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/objstore"
	"github.com/haiwen/seafile-server/fileserver/option"
)

func TestDecodeLegacyFile(t *testing.T) {
	blockID := strings.Repeat("ab", 20)
	data, err := hex.DecodeString("00000001" + "0000000000000100" + blockID)
	if err != nil {
		t.Fatal(err)
	}
	file, err := decodeLegacyFile(blockID, data)
	if err != nil {
		t.Fatal(err)
	}
	if file.Version != 0 || file.FileSize != 256 || len(file.BlkIDs) != 1 || file.BlkIDs[0] != blockID {
		t.Fatalf("unexpected file: %+v", file)
	}
	if _, err := decodeLegacyFile(blockID, data[:len(data)-1]); err == nil {
		t.Fatal("truncated block reference accepted")
	}
}

func TestDecodeLegacyDir(t *testing.T) {
	data, err := hex.DecodeString("00000003" + "000081a4" + hex.EncodeToString([]byte(EmptySha1)) + "00000001" + "78")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := decodeLegacyDir(EmptySha1, data)
	if err != nil {
		t.Fatal(err)
	}
	if dir.Version != 0 || len(dir.Entries) != 1 {
		t.Fatalf("unexpected directory: %+v", dir)
	}
	entry := dir.Entries[0]
	if entry.Name != "x" || entry.Mode != 0100644 || entry.ID != EmptySha1 {
		t.Fatalf("unexpected dirent: %+v", entry)
	}
	if _, err := decodeLegacyDir(EmptySha1, data[:len(data)-1]); err == nil {
		t.Fatal("truncated entry accepted")
	}
}

func TestSQLiteFileserverReadsLegacyFSObjects(t *testing.T) {
	previousStore, previousType := store, option.DBType
	store, option.DBType = objstore.OpenFilesystem(t.TempDir(), "fs"), "sqlite"
	t.Cleanup(func() {
		store, option.DBType = previousStore, previousType
	})

	const repoID = "4594277f-b542-4ea1-9c22-bcf2d59d4e3c"
	fileID, blockID, dirID := strings.Repeat("ab", 20), strings.Repeat("cd", 20), strings.Repeat("ef", 20)
	objects := map[string]string{
		fileID: "00000001" + "0000000000000100" + blockID,
		dirID:  "00000003" + "000081a4" + hex.EncodeToString([]byte(fileID)) + "00000001" + "78",
	}
	for id, encoded := range objects {
		data, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Write(repoID, id, bytes.NewReader(data), false); err != nil {
			t.Fatal(err)
		}
	}

	entry, err := GetDirentByPath(repoID, dirID, "/x")
	if err != nil || entry.ID != fileID {
		t.Fatalf("legacy path lookup failed: %v", err)
	}
	file, err := GetSeafile(repoID, entry.ID)
	if err != nil || file.Version != 0 || file.FileSize != 256 || len(file.BlkIDs) != 1 || file.BlkIDs[0] != blockID {
		t.Fatalf("legacy file read failed: %+v, %v", file, err)
	}
	if _, err := NewSeafile(0, 256, []string{blockID}); err != errLegacyObjectWrite {
		t.Fatalf("legacy file creation did not reject an incompatible write: %v", err)
	}
	if _, err := NewSeafdir(0, []*SeafDirent{entry}); err != errLegacyObjectWrite {
		t.Fatalf("legacy dir creation did not reject an incompatible write: %v", err)
	}
}
