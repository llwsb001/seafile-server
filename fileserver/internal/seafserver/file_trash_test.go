package seafserver

import (
	"slices"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

func TestDeletedEntriesOrder(t *testing.T) {
	scan := deletedScan{
		reader: NewReader(nil, t.TempDir()),
		repo:   &repomgr.Repo{StoreID: "08ab7900-bf37-4f6f-8b6f-fbca20800001", Version: 1},
		entries: map[string]deletedEntry{
			"/early.txt": {base: "/", dirent: &fsmgr.SeafDirent{Name: "early.txt"}, deleted: 10},
			"/a/z.txt":   {base: "/a/", dirent: &fsmgr.SeafDirent{Name: "z.txt"}, deleted: 20},
			"/b.txt":     {base: "/", dirent: &fsmgr.SeafDirent{Name: "b.txt"}, deleted: 20},
		},
	}
	page := scan.results(&commitmgr.Commit{RootID: emptyObjectID}, nil)
	var paths []string
	for _, entry := range page[:len(page)-1] {
		paths = append(paths, entry["basedir"].(string)+entry["obj-name"].(string))
	}
	if want := []string{"/a/z.txt", "/b.txt", "/early.txt"}; !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	if end := page[len(page)-1]; end["scan-stat"] != nil || end["obj-name"] != nil {
		t.Fatalf("invalid end of page: %v", end)
	}
}
