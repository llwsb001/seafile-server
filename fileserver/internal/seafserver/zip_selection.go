package seafserver

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/repocrypt"
	"github.com/haiwen/seafile-server/fileserver/internal/ziparchive"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"golang.org/x/text/encoding/ianaindex"
)

const defaultZIPLimit = 100_000_000

var errZIPSize = errors.New("zip size limit exceeded")

type zipInput struct {
	ObjectID string   `json:"obj_id"`
	DirPath  string   `json:"dir"`
	DirName  string   `json:"dir_name"`
	Parent   string   `json:"parent_dir"`
	Files    []string `json:"file_list"`
	Windows  int      `json:"is_windows"`
}

type zipSelection struct {
	repo     *repomgr.Repo
	user     string
	filename string
	windows  bool
	entries  []*fsmgr.SeafDirent
}

func (m *ZipDownloads) selection(ctx context.Context, access webAccess) (zipSelection, *searpc.RPCError) {
	repo, err := m.reader.Repo(ctx, access.RepoID)
	if err != nil {
		return zipSelection{}, rpcError(errWebAccessToken, "Failed to get repo.")
	}
	result := zipSelection{repo: repo, user: access.Username}
	var input zipInput
	parseErr := json.Unmarshal([]byte(access.ObjectID), &input)
	result.windows = input.Windows != 0
	if access.Op == "download-dir" || access.Op == "download-dir-link" {
		if parseErr != nil || !ziparchive.ValidName(input.DirName) || (input.ObjectID == "" && input.DirPath == "") {
			return zipSelection{}, rpcError(errWebAccessToken, "Failed to parse download dir data.")
		}
		if input.ObjectID == "" {
			dir, rpcErr := m.reader.directoryByPath(repo, repo.RootID, input.DirPath)
			if rpcErr != nil {
				return zipSelection{}, rpcError(errWebAccessToken, "Dir doesn't exist.")
			}
			input.ObjectID = dir.DirID
		}
		if _, err := m.reader.Directory(repo, input.ObjectID); err != nil {
			return zipSelection{}, rpcError(errWebAccessToken, "Dir doesn't exist.")
		}
		result.filename = input.DirName + ".zip"
		result.entries = []*fsmgr.SeafDirent{{ID: input.ObjectID, Name: input.DirName, Mode: syscall.S_IFDIR}}
		return result, nil
	}
	if parseErr != nil || input.Parent == "" || len(input.Files) == 0 {
		return zipSelection{}, rpcError(errWebAccessToken, "Failed to parse download multi data.")
	}
	entries, ok := m.selectedEntries(repo, input.Parent, input.Files)
	if !ok {
		return zipSelection{}, rpcError(errWebAccessToken, "Failed to parse download multi data.")
	}
	result.entries = entries
	return result, nil
}

func (m *ZipDownloads) selectedEntries(repo *repomgr.Repo, parent string, names []string) ([]*fsmgr.SeafDirent, bool) {
	base, err := m.reader.directoryByPath(repo, repo.RootID, parent)
	if err != nil {
		return nil, false
	}
	entries := make([]*fsmgr.SeafDirent, 0, len(names))
	for _, name := range names {
		dir := base
		if relative, baseName := path.Split(name); relative != "" {
			dir, err = m.reader.directoryByPath(repo, repo.RootID, strings.TrimRight(parent, "/")+"/"+strings.Trim(relative, "/"))
			if err != nil {
				return nil, false
			}
			name = baseName
		}
		matched := slices.IndexFunc(dir.Entries, func(entry *fsmgr.SeafDirent) bool { return entry.Name == name })
		if matched < 0 {
			return nil, false
		}
		entries = append(entries, dir.Entries[matched])
	}
	// The C preparation loop prepends each selected entry.
	slices.Reverse(entries)
	return entries, true
}

func (m *ZipDownloads) buildArchive(ctx context.Context, job *zipJob, selection zipSelection) (string, error) {
	pack := ziparchive.Writer{Source: m.reader, Repo: selection.repo, Modified: time.Now()}
	if selection.repo.IsEncrypted {
		if m.passwords == nil {
			return "", errors.New("repository password cache unavailable")
		}
		key, iv := m.passwords.copyKey(selection.repo.ID, selection.user)
		if key == nil {
			return "", errors.New("repository password was not set")
		}
		defer clear(key)
		defer clear(iv)
		pack.Decode = func(block []byte) ([]byte, error) {
			return repocrypt.Decrypt(selection.repo.EncVersion, key, iv, block)
		}
	}
	if selection.windows {
		if name, ok := m.config.file[configKey{"zip", "windows_encoding"}]; ok && name != "" {
			encoding, err := ianaindex.IANA.Encoding(strings.TrimSpace(name))
			if err != nil || encoding == nil {
				return "", fmt.Errorf("invalid ZIP filename encoding %q", name)
			}
			pack.EncodeName = encoding.NewEncoder().String
		}
	}
	total, err := m.measureArchive(ctx, selection)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	job.progress.Total = total
	m.mu.Unlock()
	pack.FileDone = func() {
		m.mu.Lock()
		job.progress.Zipped++
		m.mu.Unlock()
	}

	out, err := os.CreateTemp(m.dir, "*.zip")
	if err != nil {
		return "", err
	}
	pack.Archive = zip.NewWriter(out)
	writeErr := addZIPEntries(ctx, pack, selection.entries)
	err = errors.Join(writeErr, pack.Archive.Close(), out.Close())
	if err != nil {
		removeZIPFile(out.Name())
		return "", err
	}
	return out.Name(), nil
}

func addZIPEntries(ctx context.Context, pack ziparchive.Writer, entries []*fsmgr.SeafDirent) error {
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !ziparchive.ValidName(entry.Name) {
			return fmt.Errorf("invalid archive entry %q", entry.Name)
		}
		name := ziparchive.UniqueName(entry.Name, names)
		var err error
		if fsmgr.IsDir(entry.Mode) {
			err = pack.Directory(ctx, entry.ID, name)
		} else if fsmgr.IsRegular(entry.Mode) {
			err = pack.File(ctx, entry, name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *ZipDownloads) measureArchive(ctx context.Context, selection zipSelection) (int, error) {
	const mb = 1_000_000
	configured := m.config.value(ctx, "fileserver", "max_download_dir_size").int64()
	limit := uint64(defaultZIPLimit)
	if configured > 0 {
		limit = uint64(min(configured, math.MaxInt64/mb)) * mb
	}
	var count int
	var measure func([]*fsmgr.SeafDirent) error
	measure = func(entries []*fsmgr.SeafDirent) error {
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch {
			case fsmgr.IsDir(entry.Mode):
				child, err := m.reader.Directory(selection.repo, entry.ID)
				if err != nil {
					return err
				}
				if err := measure(child.Entries); err != nil {
					return err
				}
			case fsmgr.IsRegular(entry.Mode):
				file, err := m.reader.File(selection.repo, entry.ID)
				if err != nil {
					return err
				}
				if file.FileSize > limit {
					return errZIPSize
				}
				limit -= file.FileSize
				count++
			}
		}
		return nil
	}
	err := measure(selection.entries)
	return count, err
}
