// Package objstore provides operations for commit, fs and block objects.
// It is low-level package used by commitmgr, fsmgr, blockmgr packages to access storage.
package objstore

import (
	"io"
)

// ObjectStore is a container to access storage backend
type ObjectStore struct {
	// can be "commit", "fs", or "block"
	ObjType string
	backend storageBackend
}

// storageBackend is the interface implemented by storage backends.
// An object store may have one or multiple storage backends.
type storageBackend interface {
	// Read an object from backend and write the contents into w.
	read(repoID string, objID string, w io.Writer) (err error)
	// Write the contents from r to the object.
	write(repoID string, objID string, r io.Reader, sync bool) (err error)
	// syncObject flushes an already written object without changing its contents.
	syncObject(repoID, objID string) error
	// exists checks whether an object exists.
	exists(repoID string, objID string) (res bool, err error)
	// stat calculates an object's size
	stat(repoID string, objID string) (res int64, err error)
}

// New returns a new object store for a given type of objects.
// objType can be "commit", "fs", or "block".
func New(seafileConfPath string, seafileDataDir string, objType string) *ObjectStore {
	obj := new(ObjectStore)
	obj.ObjType = objType
	obj.backend, _ = newFSBackend(seafileDataDir, objType)
	return obj
}

// OpenFilesystem opens an object store without creating directories.
func OpenFilesystem(dataDir, objType string) *ObjectStore {
	return &ObjectStore{ObjType: objType, backend: filesystemAt(dataDir, objType)}
}

// Read data from storage backends.
func (s *ObjectStore) Read(repoID string, objID string, w io.Writer) (err error) {
	return s.backend.read(repoID, objID, w)
}

// Write data to storage backends.
func (s *ObjectStore) Write(repoID string, objID string, r io.Reader, sync bool) (err error) {
	return s.backend.write(repoID, objID, r, sync)
}

// Sync flushes an existing object and its directory entries.
func (s *ObjectStore) Sync(repoID, objID string) error {
	return s.backend.syncObject(repoID, objID)
}

// Copy streams one immutable object into another library's storage namespace.
func (s *ObjectStore) Copy(srcRepo, dstRepo, objID string) error {
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		writer.CloseWithError(s.Read(srcRepo, objID, writer))
	}()
	return s.Write(dstRepo, objID, reader, true)
}

// Check whether object exists.
func (s *ObjectStore) Exists(repoID string, objID string) (res bool, err error) {
	return s.backend.exists(repoID, objID)
}

// Stat calculates object size.
func (s *ObjectStore) Stat(repoID string, objID string) (res int64, err error) {
	return s.backend.stat(repoID, objID)
}
