package seafserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errInvalidStagedSource = errors.New("invalid input file")

type stagedSource struct {
	*os.File
	stat unix.Stat_t
}

func (r *Reader) openStagedSource(path string) (*stagedSource, error) {
	root, err := filepath.Abs(r.stagedImportRoot)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errInvalidStagedSource
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return nil, errInvalidStagedSource
	}
	// Traverse directory descriptors so replacing a path component cannot
	// redirect a subsequent open. Only the core/Seahub service UID can stage files.
	const dirFlags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_DIRECTORY
	uid := uint32(os.Geteuid())
	directory, err := unix.Open(root, dirFlags, 0)
	if err != nil {
		return nil, err
	}
	defer func() { unix.Close(directory) }()
	components := strings.Split(relative, "/")
	for _, name := range components[:len(components)-1] {
		if !privateStagingDirectory(directory, uid) {
			return nil, errInvalidStagedSource
		}
		child, err := unix.Openat(directory, name, dirFlags, 0)
		if err != nil {
			return nil, err
		}
		unix.Close(directory)
		directory = child
	}
	if !privateStagingDirectory(directory, uid) {
		return nil, errInvalidStagedSource
	}
	fd, err := unix.Openat(directory, components[len(components)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uid ||
		stat.Nlink != 1 || stat.Mode&0o022 != 0 {
		unix.Close(fd)
		return nil, errInvalidStagedSource
	}
	return &stagedSource{File: os.NewFile(uintptr(fd), path), stat: stat}, nil
}

func privateStagingDirectory(fd int, uid uint32) bool {
	var stat unix.Stat_t
	return unix.Fstat(fd, &stat) == nil && stat.Uid == uid && stat.Mode&0o077 == 0
}

func (s *stagedSource) unchanged() bool {
	var current unix.Stat_t
	return unix.Fstat(int(s.Fd()), &current) == nil && current.Size == s.stat.Size &&
		current.Mtim == s.stat.Mtim && current.Ctim == s.stat.Ctim
}
