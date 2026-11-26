// Package maintenance reads SQLite repositories and their filesystem-backed objects.
package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/objstore"
	"github.com/haiwen/seafile-server/fileserver/utils"
	"gopkg.in/ini.v1"
)

// Repo names the logical commit store and the physical FS and block store.
type Repo struct {
	ID, StoreID, HeadID string
}

// Store reads the SQLite database and three companion object stores.
type Store struct {
	*objectStores
	db          *sql.DB
	snapshotDir string
	defaultDays int
	write       bool
}

type objectStores struct {
	dataDir string
	commits *objstore.ObjectStore
	fs      *objstore.ObjectStore
	blocks  *objstore.ObjectStore
}

func openObjectStores(dataDir string) *objectStores {
	return &objectStores{
		dataDir: dataDir,
		commits: objstore.OpenFilesystem(dataDir, "commits"),
		fs:      objstore.OpenFilesystem(dataDir, "fs"),
		blocks:  objstore.OpenFilesystem(dataDir, "blocks"),
	}
}

// Open opens a stopped local SQLite installation for maintenance.
func Open(ctx context.Context, dataDir, configDir string) (*Store, error) {
	return openSQLiteMaintenance(ctx, dataDir, configDir, false)
}

// OpenForRepair opens a stopped SQLite installation for explicit repair.
func OpenForRepair(ctx context.Context, dataDir, configDir string) (*Store, error) {
	return openSQLiteMaintenance(ctx, dataDir, configDir, true)
}

func openSQLiteMaintenance(ctx context.Context, dataDir, configDir string, write bool) (*Store, error) {
	conf, err := loadMaintenanceConfig(dataDir, configDir)
	if err != nil {
		return nil, err
	}

	if !strings.EqualFold(conf.Section("database").Key("type").String(), "sqlite") {
		return nil, errors.New("maintenance requires database type sqlite")
	}
	path := filepath.Join(dataDir, "seafile.db")
	var db *sql.DB
	var snapshotDir string
	if write {
		db, err = openRepairDB(ctx, path)
	} else {
		db, snapshotDir, err = openSQLiteSnapshot(ctx, path)
	}
	if err != nil {
		return nil, err
	}
	return &Store{
		objectStores: openObjectStores(dataDir),
		db:           db,
		snapshotDir:  snapshotDir,
		defaultDays:  conf.Section("history").Key("keep_days").MustInt(-1),
		write:        write,
	}, nil
}

func loadMaintenanceConfig(dataDir, configDir string) (*ini.File, error) {
	if configDir == "" {
		configDir = dataDir
	}
	for _, dir := range []string{dataDir, filepath.Join(dataDir, "tmpfiles")} {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", dir)
		}
	}

	configPath := filepath.Join(configDir, "seafile.conf")
	conf, err := ini.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config file %s: %w", configPath, err)
	}
	return conf, nil
}

// Close closes the database connection pool.
func (s *Store) Close() error {
	return errors.Join(s.db.Close(), closeSQLiteSnapshot(s.snapshotDir))
}

// RepoIDs returns the active repositories in the requested SQLite LIKE selection.
func (s *Store) RepoIDs(ctx context.Context, prefix *string) ([]string, error) {
	query := "SELECT repo_id FROM Repo"
	var args []any
	if prefix != nil {
		query += " WHERE repo_id LIKE ?"
		args = append(args, *prefix+"%")
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Repo reads the master branch and virtual store mapping even if the head is damaged.
func (s *Store) Repo(ctx context.Context, id string) (Repo, error) {
	if !ValidRepoID(id) {
		return Repo{}, fmt.Errorf("invalid repo id %q", id)
	}
	const query = `SELECT COALESCE(v.origin_repo, r.repo_id), COALESCE(b.commit_id, '')
		FROM Repo r LEFT JOIN Branch b ON b.repo_id = r.repo_id AND b.name = 'master'
		LEFT JOIN VirtualRepo v ON v.repo_id = r.repo_id WHERE r.repo_id = ?`
	repo := Repo{ID: id}
	if err := s.db.QueryRowContext(ctx, query, id).Scan(&repo.StoreID, &repo.HeadID); err != nil {
		return Repo{}, err
	}
	if !ValidRepoID(repo.StoreID) {
		return Repo{}, fmt.Errorf("repo %s has invalid store id %q", id, repo.StoreID)
	}
	return repo, nil
}

// ValidRepoID accepts the hyphenated repository IDs used by the C CLI.
func ValidRepoID(id string) bool {
	return len(id) == 36 && utils.IsValidUUID(id)
}

// Commit reads a commit from the logical repository.
func (s *objectStores) Commit(repo Repo, id string) (*commitmgr.Commit, error) {
	if !utils.IsObjectIDValid(id) {
		return nil, fmt.Errorf("invalid commit id %q", id)
	}
	return commitmgr.LoadFromStore(s.commits, repo.ID, id)
}

// CommitIDs lists candidate commit files for head recovery.
func (s *objectStores) CommitIDs(repo Repo) ([]string, error) {
	root := filepath.Join(s.dataDir, "storage", "commits", repo.ID)
	shards, err := readStorageDirectory(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, shard := range shards {
		if len(shard.Name()) != 2 || !shard.IsDir() {
			continue
		}
		objects, err := readStorageDirectory(filepath.Join(root, shard.Name()))
		if err != nil {
			return nil, err
		}
		for _, object := range objects {
			if id := shard.Name() + object.Name(); utils.IsObjectIDValid(id) && !object.IsDir() {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

func readStorageDirectory(path string) ([]os.DirEntry, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	// The C scanner sees the filesystem order through g_dir_read_name.
	return dir.ReadDir(-1)
}

// Directory reads and verifies directory metadata in the physical store.
func (s *objectStores) Directory(repo Repo, version int, id string) (*fsmgr.SeafDir, error) {
	return fsmgr.ReadVerifiedSeafdir(s.fs, repo.StoreID, id, version)
}

// File reads and verifies file metadata in the physical store.
func (s *objectStores) File(repo Repo, version int, id string) (*fsmgr.Seafile, error) {
	return fsmgr.ReadVerifiedSeafile(s.fs, repo.StoreID, id, version)
}

// ReadBlock copies the raw block, including ciphertext, from the physical store.
func (s *objectStores) ReadBlock(repo Repo, id string, dest io.Writer) error {
	if !utils.IsObjectIDValid(id) {
		return fmt.Errorf("invalid block id %q", id)
	}
	return s.blocks.Read(repo.StoreID, id, dest)
}

func (s *objectStores) blockExists(repo Repo, id string) bool {
	exists, err := s.blocks.Exists(repo.StoreID, id)
	return exists && err == nil
}

func (s *objectStores) candidateCommits(repo Repo) ([]*commitmgr.Commit, error) {
	ids, scanErr := s.CommitIDs(repo)
	var commits []*commitmgr.Commit
	for _, id := range ids {
		commit, err := s.Commit(repo, id)
		if err == nil {
			commits = append(commits, commit)
		}
	}
	// The C scanner prepends each commit before its stable time sort.
	slices.Reverse(commits)
	slices.SortStableFunc(commits, newestCommitFirst)
	return commits, scanErr
}
