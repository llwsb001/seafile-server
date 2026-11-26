package fsmgr

import (
	"bytes"
	"errors"
	"os"

	"github.com/haiwen/seafile-server/fileserver/objstore"
)

// WriteCompatibleDirectory writes a replacement directory for offline repair,
// using the repository's original on-disk format and flushing before publication.
func WriteCompatibleDirectory(objects *objstore.ObjectStore, storeID string, version int, entries []*SeafDirent) (string, error) {
	if len(entries) == 0 {
		return EmptySha1, nil
	}
	var data bytes.Buffer
	var id string
	if version == 0 {
		id = legacyDirID(entries)
		encodeLegacyDir(entries, &data)
	} else {
		dir, err := NewSeafdir(version, entries)
		if err != nil {
			return "", err
		}
		if err := dir.ToData(&data); err != nil {
			return "", err
		}
		id = dir.DirID
	}
	if _, err := ReadVerifiedSeafdir(objects, storeID, id, version); err == nil {
		return id, objects.Sync(storeID, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return id, objects.Write(storeID, id, &data, true)
}
