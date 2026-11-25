package fsmgr

import (
	"fmt"
	"strings"
)

const duplicateNamesCount = 1000

// UniqueName returns the first available name, retaining the existing extension.
func UniqueName(fileName string, entries []*SeafDirent) string {
	var uniqueName string
	var name string
	i := 1
	dot := strings.LastIndex(fileName, ".")
	if dot < 0 {
		name = fileName
	} else {
		name = fileName[:dot]
	}
	uniqueName = fileName
	for nameExists(entries, uniqueName) && i <= duplicateNamesCount {
		if dot < 0 {
			uniqueName = fmt.Sprintf("%s (%d)", name, i)
		} else {
			uniqueName = fmt.Sprintf("%s (%d).%s", name, i, fileName[dot+1:])
		}
		i++
	}

	if i <= duplicateNamesCount {
		return uniqueName
	}

	return ""
}

func nameExists(entries []*SeafDirent, fileName string) bool {
	for _, entry := range entries {
		if entry.Name == fileName {
			return true
		}
	}

	return false
}
