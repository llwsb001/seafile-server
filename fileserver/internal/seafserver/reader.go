// Package seafserver provides the data operations of the Go Seafile core.
package seafserver

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"path/filepath"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/objstore"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/utils"
	_ "modernc.org/sqlite"
)

// Reader reads repositories from the database and its companion object storage.
type Reader struct {
	db      *sql.DB
	commits *objstore.ObjectStore
	fs      *objstore.ObjectStore
	blocks  *objstore.ObjectStore
}

// NewReader uses an existing database connection and a seafile-data directory.
func NewReader(db *sql.DB, dataDir string) *Reader {
	return &Reader{
		db:      db,
		commits: objstore.OpenFilesystem(dataDir, "commits"),
		fs:      objstore.OpenFilesystem(dataDir, "fs"),
		blocks:  objstore.OpenFilesystem(dataDir, "blocks"),
	}
}

// OpenSQLiteReadOnly opens an existing Seafile SQLite database.
func OpenSQLiteReadOnly(ctx context.Context, dbPath string) (*sql.DB, error) {
	path, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open SQLite database %q: %w", path, err)
	}
	return db, nil
}

// Repo reads the current branch and head commit from storage.
func (r *Reader) Repo(ctx context.Context, repoID string) (*repomgr.Repo, error) {
	if len(repoID) != 36 || !utils.IsValidUUID(repoID) {
		return nil, fmt.Errorf("invalid repo id %q", repoID)
	}
	repo, err := repomgr.Load(ctx, r.db, repoID, r.Commit)
	if err != nil {
		return nil, err
	}
	if len(repo.StoreID) != 36 || !utils.IsValidUUID(repo.StoreID) {
		return nil, fmt.Errorf("repo %s has an invalid store id %q", repoID, repo.StoreID)
	}
	return repo, nil
}

// Commit reads a commit from the logical repository, including virtual repos.
func (r *Reader) Commit(repoID, commitID string) (*commitmgr.Commit, error) {
	if len(repoID) != 36 || !utils.IsValidUUID(repoID) || !utils.IsObjectIDValid(commitID) {
		return nil, fmt.Errorf("invalid commit reference %q/%q", repoID, commitID)
	}
	return commitmgr.LoadFromStore(r.commits, repoID, commitID)
}

// Directory reads a directory from the repository's FS store.
func (r *Reader) Directory(repo *repomgr.Repo, dirID string) (*fsmgr.SeafDir, error) {
	return fsmgr.ReadSeafdir(r.fs, repo.StoreID, dirID, repo.Version)
}

// File reads file metadata from the repository's FS store.
func (r *Reader) File(repo *repomgr.Repo, fileID string) (*fsmgr.Seafile, error) {
	return fsmgr.ReadSeafile(r.fs, repo.StoreID, fileID, repo.Version)
}

// ReadBlock copies the stored bytes, including ciphertext in encrypted repos.
func (r *Reader) ReadBlock(repo *repomgr.Repo, blockID string, dest io.Writer) error {
	if !utils.IsObjectIDValid(blockID) {
		return fmt.Errorf("invalid block id %q", blockID)
	}
	return r.blocks.Read(repo.StoreID, blockID, dest)
}
