package fsmgr

import (
	"encoding/hex"
	"strings"
	"testing"
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
