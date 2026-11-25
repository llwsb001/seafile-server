package seafserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"golang.org/x/text/unicode/norm"
)

func fileNames(encoded string) ([]string, bool) {
	var names []string
	if err := json.Unmarshal([]byte(encoded), &names); err != nil || len(names) == 0 {
		return nil, false
	}
	for i, name := range names {
		names[i] = norm.NFC.String(name)
		if !validWriteName(names[i]) {
			return nil, false
		}
	}
	return names, true
}

func deleteFiles(t *fileTree, args []string) (string, *searpc.RPCError) {
	parent, rpcErr := fileWritePath(args[1])
	if rpcErr != nil {
		return "", rpcErr
	}
	names, ok := fileNames(args[2])
	if !ok {
		return "", rpcError(errGeneral, "Failed to del file")
	}
	deleted, rpcErr := t.remove(parent, names)
	if rpcErr != nil {
		return "", rpcErr
	}
	return describeDeleted(deleted), nil
}

func batchDeleteFiles(t *fileTree, args []string) (string, *searpc.RPCError) {
	var paths []string
	if err := json.Unmarshal([]byte(args[1]), &paths); err != nil || len(paths) == 0 {
		return "", rpcError(errBadArgs, "Invalid file paths")
	}
	var deleted []*fsmgr.SeafDirent
	for _, value := range paths {
		path, rpcErr := fileWritePath(value)
		if rpcErr != nil || len(path) == 0 {
			return "", rpcError(errBadArgs, "Invalid file path")
		}
		entries, rpcErr := t.remove(path[:len(path)-1], path[len(path)-1:])
		if rpcErr != nil && rpcErr.Code != errPathMissing {
			return "", rpcErr
		}
		deleted = append(deleted, entries...)
	}
	return describeDeleted(deleted), nil
}

func (t *fileTree) remove(parent, names []string) ([]*fsmgr.SeafDirent, *searpc.RPCError) {
	var deleted []*fsmgr.SeafDirent
	rpcErr := t.change(parent, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
		dir.Entries = slices.DeleteFunc(dir.Entries, func(entry *fsmgr.SeafDirent) bool {
			if !slices.Contains(names, entry.Name) {
				return false
			}
			deleted = append(deleted, entry)
			return true
		})
		return len(deleted) > 0, nil
	})
	return deleted, rpcErr
}

func describeDeleted(entries []*fsmgr.SeafDirent) string {
	switch len(entries) {
	case 0:
		return ""
	case 1:
		if fsmgr.IsDir(entries[0].Mode) {
			return fmt.Sprintf("Removed directory \"%s\"", entries[0].Name)
		}
		return fmt.Sprintf("Deleted \"%s\"", entries[0].Name)
	default:
		return fmt.Sprintf("Deleted \"%s\" and %d more files", entries[0].Name, len(entries)-1)
	}
}

func (w *fileWriter) moveFiles(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	if len(args) != 10 {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	values, rpcErr := fileArguments(args[:6], 6)
	user := stringArgument(args, 7)
	if rpcErr != nil || user == nil {
		return nil, rpcError(errBadArgs, "Argument should not be null")
	}
	if !validRepoID(values[0]) || !validRepoID(values[3]) {
		return nil, rpcError(errBadArgs, "Invalid repo id")
	}
	if values[0] != values[3] {
		return w.moveBetweenLibraries(ctx, values, *user, args)
	}
	replace := intArgument(args, 6) > 0
	rpcErr = w.update(ctx, values[0], *user, values, func(t *fileTree, args []string) (string, *searpc.RPCError) {
		return moveFilesWithinRepo(t, args, replace)
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	return map[string]any{"background": false, "task-id": nil}, nil
}

func moveFilesWithinRepo(t *fileTree, args []string, replace bool) (string, *searpc.RPCError) {
	from, rpcErr := fileWritePath(args[1])
	if rpcErr != nil {
		return "", rpcErr
	}
	to, rpcErr := fileWritePath(args[4])
	if rpcErr != nil {
		return "", rpcErr
	}
	sources, validSource := fileNames(args[2])
	destinations, validDestination := fileNames(args[5])
	if !validSource || !validDestination {
		return "", rpcError(errBadArgs, "Load filenames to json failed")
	}
	if len(sources) != len(destinations) {
		return "", rpcError(errBadArgs, "The number of files in the parameters does not match")
	}
	dir, rpcErr := t.reader.directoryByPath(t.repo, t.root, strings.Join(from, "/"))
	if rpcErr != nil {
		return "", rpcErr
	}
	entries := make([]*fsmgr.SeafDirent, 0, len(sources))
	removed := make([]string, 0, len(sources))
	seen := make(map[string]bool, len(sources))
	for i, name := range sources {
		index := findEntry(dir.Entries, name)
		if index == -1 {
			return "", rpcError(errBadArgs, fmt.Sprintf("File %s does not exist", name))
		}
		if seen[name] {
			return "", rpcError(errBadArgs, "Duplicate source filename")
		}
		seen[name] = true
		entry := dir.Entries[index]
		if len(to) > len(from) && slices.Equal(to[:len(from)], from) && to[len(from)] == name && fsmgr.IsDir(entry.Mode) {
			return "", rpcError(errGeneral, "Can not move directory to its subdirectory")
		}
		if slices.Equal(from, to) && destinations[i] == name {
			continue
		}
		entries = append(entries, fsmgr.NewDirent(entry.ID, destinations[i], entry.Mode, entry.Mtime, t.user, entry.Size))
		removed = append(removed, name)
	}
	if len(entries) == 0 {
		return "", nil
	}

	// Remove the source entries before inserting replacements. The branch only
	// changes when both edits and the new commit have reached storage.
	if _, rpcErr := t.remove(from, removed); rpcErr != nil {
		return "", rpcErr
	}
	rpcErr = t.insertMoved(to, entries, replace)
	if rpcErr != nil {
		return "", rpcErr
	}
	name := removed[0]
	if len(removed) > 1 {
		return fmt.Sprintf("Moved \"%s\" and %d more files", name, len(removed)-1), nil
	}
	if fsmgr.IsDir(entries[0].Mode) {
		return fmt.Sprintf("Moved directory \"%s\"", name), nil
	}
	return fmt.Sprintf("Moved \"%s\"", name), nil
}

func (t *fileTree) insertMoved(path []string, entries []*fsmgr.SeafDirent, replace bool) *searpc.RPCError {
	return t.change(path, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
		for _, entry := range entries {
			index := findEntry(dir.Entries, entry.Name)
			if index != -1 {
				if replace {
					dir.Entries[index] = entry
					continue
				}
				entry.Name = fsmgr.UniqueName(entry.Name, dir.Entries)
				if entry.Name == "" {
					return false, rpcError(errBadArgs, "move file failed")
				}
			}
			dir.Entries = append(dir.Entries, entry)
		}
		return true, nil
	})
}
