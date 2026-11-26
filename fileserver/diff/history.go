package diff

import (
	"slices"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
)

// DiffHistoryRoots compares two roots or a merge root followed by its two parents.
// It returns the collected entries even if a directory could not be read.
func DiffHistoryRoots(roots []string, loadDir func(string) (*fsmgr.SeafDir, error), foldDirs bool) ([]*DiffEntry, error) {
	var entries []*DiffEntry
	opt := DiffOptions{
		FileCB: twowayDiffFiles,
		DirCB:  twowayDiffDirs,
		Data:   diffData{foldDirs, &entries},
	}
	if len(roots) == 3 {
		opt.FileCB = threewayDiffFiles
		opt.DirCB = threewayDiffDirs
	}
	err := diffTrees(len(roots), roots, &opt, loadDir)
	slices.Reverse(entries) // C prepends each entry during traversal.
	return resolveHistoryRenames(entries), err
}

func resolveHistoryRenames(entries []*DiffEntry) []*DiffEntry {
	emptyCounts := make(map[rune]int)
	for _, entry := range entries {
		if entry.Sha1 == EmptySha1 {
			emptyCounts[entry.Status]++
		}
	}
	eligible := func(entry *DiffEntry, added, deleted rune) bool {
		return entry.Sha1 != EmptySha1 || (emptyCounts[added] == 1 && emptyCounts[deleted] == 1)
	}

	deletedFiles := make(map[string]int)
	deletedDirs := make(map[string]int)
	for i, entry := range entries {
		switch {
		case entry.Status == DiffStatusDeleted && eligible(entry, DiffStatusAdded, DiffStatusDeleted):
			deletedFiles[entry.Sha1] = i
		case entry.Status == DiffStatusDirDeleted && eligible(entry, DiffStatusDirAdded, DiffStatusDirDeleted):
			deletedDirs[entry.Sha1] = i
		}
	}

	var renamed []*DiffEntry
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		var deleted map[string]int
		var status rune
		switch {
		case entry.Status == DiffStatusAdded && eligible(entry, DiffStatusAdded, DiffStatusDeleted):
			deleted, status = deletedFiles, DiffStatusRenamed
		case entry.Status == DiffStatusDirAdded && eligible(entry, DiffStatusDirAdded, DiffStatusDirDeleted):
			deleted, status = deletedDirs, DiffStatusDirRenamed
		default:
			continue
		}
		if old, ok := deleted[entry.Sha1]; ok {
			move := diffEntryNew(DiffTypeCommits, status, entry.Sha1, entries[old].Name)
			move.NewName = entry.Name
			renamed = append(renamed, move)
			entries[i], entries[old] = nil, nil
			delete(deleted, entry.Sha1)
		}
	}

	slices.Reverse(renamed)
	for _, entry := range entries {
		if entry != nil {
			renamed = append(renamed, entry)
		}
	}
	return renamed
}
