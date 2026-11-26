package seafserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

const (
	systemTemplateName  = "My Library Template"
	systemTemplateDesc  = "Template for creating 'My Library' for users"
	systemTemplateOwner = "System"
)

// EnsureSystemDefaultRepo publishes the first template and its pointer together.
// A template already readable through the pointer is left to the administrator.
func (r *Reader) EnsureSystemDefaultRepo(ctx context.Context, db *sql.DB, quota *UserQuota, dataDir string) error {
	exists, err := r.hasSystemDefaultRepo(ctx, r.db)
	if exists || err != nil {
		return err
	}

	id, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	repoID := id.String()
	tree := fileTree{
		reader: r,
		repo:   &repomgr.Repo{ID: repoID, StoreID: repoID, Version: initialRepoVersion},
		user:   systemTemplateOwner,
		now:    time.Now().Unix(),
	}
	// Build the template before taking SQLite's write lock. A failed object
	// write or a competing initialization cannot expose a half-built library.
	path := filepath.Join(dataDir, "library-template")
	entries, err := os.ReadDir(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read template directory %q: %w", path, err)
	}
	writer := fileWriter{quota: quota}
	root, err := writer.indexSystemTemplate(ctx, &tree, path, entries)
	if err != nil {
		return err
	}
	counts, err := fsmgr.ReadFileCountInfo(r.fs, repoID, root, initialRepoVersion)
	if err != nil {
		return err
	}
	commit := newInitialRepoCommit(repoID, root, systemTemplateName, systemTemplateDesc,
		"Created library", asciiLower(systemTemplateOwner), initialRepoVersion, tree.now)
	if err := r.writeInitialRepoCommit(commit); err != nil {
		return fmt.Errorf("write template commit: %w", err)
	}

	_, err = withSQLiteWrite(ctx, db, func(conn *sql.Conn) (bool, error) {
		exists, err := r.hasSystemDefaultRepo(ctx, conn)
		if exists || err != nil {
			return false, err
		}
		if err := publishInitialRepo(ctx, conn, commit, systemTemplateOwner); err != nil {
			return false, err
		}
		if err := writeSQLiteRepoStats(ctx, conn, repoID, commit.CommitID, counts.Size, counts.FileCount); err != nil {
			return false, err
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM SystemInfo WHERE info_key = 'default_repo_id'"); err != nil {
			return false, err
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO SystemInfo (info_key, info_value) VALUES ('default_repo_id', ?)", repoID)
		return err == nil, err
	})
	return err
}

func (r *Reader) hasSystemDefaultRepo(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (bool, error) {
	const query = `SELECT s.info_value, b.commit_id FROM SystemInfo s
		LEFT JOIN Repo r ON r.repo_id = s.info_value
		LEFT JOIN Branch b ON b.repo_id = r.repo_id AND b.name = 'master'
		WHERE s.info_key = 'default_repo_id'`
	var id, head sql.NullString
	err := db.QueryRowContext(ctx, query).Scan(&id, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read default library: %w", err)
	}
	if !validRepoID(id.String) || !utils.IsObjectIDValid(head.String) {
		return false, nil
	}
	_, err = r.Commit(id.String, head.String)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (w *fileWriter) indexSystemTemplate(ctx context.Context, tree *fileTree, path string, entries []os.DirEntry) (string, error) {
	dirents := make([]*fsmgr.SeafDirent, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := entry.Name()
		if len(name) >= 256 || !utf8.ValidString(name) {
			continue
		}
		childPath := filepath.Join(path, name)
		switch {
		case entry.IsDir():
			children, err := os.ReadDir(childPath)
			if err != nil {
				return "", fmt.Errorf("read template directory %q: %w", childPath, err)
			}
			childID, err := w.indexSystemTemplate(ctx, tree, childPath, children)
			if err != nil {
				return "", err
			}
			dirents = append(dirents, fsmgr.NewDirent(childID, name, dirMode, tree.now, "", 0))
		case entry.Type().IsRegular():
			dirent, err := w.indexSystemTemplateFile(ctx, tree, childPath, name)
			if err != nil {
				return "", err
			}
			dirents = append(dirents, dirent)
		}
	}
	id, rpcErr := tree.save(dirents)
	if rpcErr != nil {
		return "", fmt.Errorf("save template directory %q: %s", path, rpcErr.Message)
	}
	return id, nil
}

func (w *fileWriter) indexSystemTemplateFile(ctx context.Context, tree *fileTree, path, name string) (*fsmgr.SeafDirent, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open template file %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat template file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("template source %q is not a regular file", path)
	}
	id, rpcErr := w.indexLocalFile(ctx, tree, file, info.Size())
	if rpcErr != nil {
		return nil, fmt.Errorf("index template file %q: %s", path, rpcErr.Message)
	}
	return fsmgr.NewDirent(id, name, emptyFileMode, tree.now, systemTemplateOwner, info.Size()), nil
}
