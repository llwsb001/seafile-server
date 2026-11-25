// Package ziparchive writes repository objects to ZIP archives.
package ziparchive

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

// Source loads files, directories and blocks from a repository's object store.
type Source interface {
	Directory(*repomgr.Repo, string) (*fsmgr.SeafDir, error)
	File(*repomgr.Repo, string) (*fsmgr.Seafile, error)
	ReadBlock(*repomgr.Repo, string, io.Writer) error
}

// Writer adds repository entries to an existing ZIP writer.
type Writer struct {
	Archive    *zip.Writer
	Source     Source
	Repo       *repomgr.Repo
	Decode     func([]byte) ([]byte, error)
	EncodeName func(string) (string, error)
	Modified   time.Time
	FileDone   func()
}

// Directory appends the contents of a directory and explicit entries for empty directories.
func (w Writer) Directory(ctx context.Context, id, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := w.Source.Directory(w.Repo, id)
	if err != nil {
		return fmt.Errorf("read directory %s: %w", id, err)
	}
	if len(dir.Entries) == 0 {
		header, err := w.header(strings.TrimRight(name, "/")+"/", w.Modified)
		if err != nil {
			return err
		}
		_, err = w.Archive.CreateHeader(header)
		return err
	}
	for _, entry := range dir.Entries {
		if !ValidName(entry.Name) {
			return fmt.Errorf("invalid archive entry %q", entry.Name)
		}
		entryPath := path.Join(name, entry.Name)
		if fsmgr.IsDir(entry.Mode) {
			err = w.Directory(ctx, entry.ID, entryPath)
		} else {
			err = w.File(ctx, entry, entryPath)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// File appends a file, optionally decrypting its blocks before writing.
func (w Writer) File(ctx context.Context, entry *fsmgr.SeafDirent, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := w.Source.File(w.Repo, entry.ID)
	if err != nil {
		return fmt.Errorf("read file %s: %w", entry.ID, err)
	}
	modified := w.Modified
	if modified.IsZero() {
		modified = time.Unix(entry.Mtime, 0)
	}
	header, err := w.header(name, modified)
	if err != nil {
		return err
	}
	header.Method = zip.Deflate
	dest, err := w.Archive.CreateHeader(header)
	if err != nil {
		return err
	}
	for _, id := range file.BlkIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.writeBlock(id, dest); err != nil {
			return fmt.Errorf("archive block %s: %w", id, err)
		}
	}
	if w.FileDone != nil && fsmgr.IsRegular(entry.Mode) {
		w.FileDone()
	}
	return nil
}

func (w Writer) header(name string, modified time.Time) (*zip.FileHeader, error) {
	header := &zip.FileHeader{Name: strings.TrimLeft(name, "/"), Modified: modified}
	if w.EncodeName != nil {
		encoded, err := w.EncodeName(header.Name)
		if err != nil {
			return nil, err
		}
		header.Name = encoded
		header.NonUTF8 = true
	}
	return header, nil
}

func (w Writer) writeBlock(id string, dest io.Writer) error {
	if w.Decode == nil {
		return w.Source.ReadBlock(w.Repo, id, dest)
	}
	var block bytes.Buffer
	if err := w.Source.ReadBlock(w.Repo, id, &block); err != nil {
		return err
	}
	data, err := w.Decode(block.Bytes())
	if err != nil {
		return err
	}
	_, err = dest.Write(data)
	return err
}

// ValidName reports whether an entry is a single repository path component.
func ValidName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

// UniqueName reserves the next available name, preserving the file extension.
func UniqueName(name string, used map[string]struct{}) string {
	result := name
	stem, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	for index := 1; ; index++ {
		if _, exists := used[result]; !exists {
			used[result] = struct{}{}
			return result
		}
		result = fmt.Sprintf("%s (%d)%s", stem, index, ext)
	}
}
