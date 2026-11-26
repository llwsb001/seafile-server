package seafserver

import (
	"context"
	"encoding/json"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/diff"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

type fileRevision struct {
	commit *commitmgr.Commit
	file   *fsmgr.SeafDirent
}

type fileRevisionState struct {
	reader      *Reader
	repo        *repomgr.Repo
	path        string
	cutoff      int64
	changes     int
	missingFile bool
	lookupError *searpc.RPCError
	entries     map[string]*fsmgr.SeafDirent
	revisions   []fileRevision
}

func (r *Reader) fileRevisions(config *ServerConfig) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		repoID, start, path := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
		if repoID == nil || path == nil {
			return nil, rpcError(errBadArgs, "Bad arguments")
		}
		if !validRepoID(*repoID) {
			return nil, rpcError(errBadArgs, "Invalid repo id")
		}
		repo, err := r.Repo(ctx, *repoID)
		if err != nil {
			return nil, rpcError(errGeneral, "No such repo "+*repoID)
		}
		headID := repo.HeadCommitID
		if start != nil {
			headID = *start
		}
		head, err := r.Commit(*repoID, headID)
		if err != nil {
			return nil, rpcError(errGeneral, "failed to traverse commit of repo "+*repoID)
		}
		state := fileRevisionState{
			reader: r, repo: repo, path: formatPath(*path),
			cutoff:  r.historyTruncateTime(ctx, *repoID, config),
			entries: make(map[string]*fsmgr.SeafDirent),
		}
		queue := newCommitQueue(r, *repoID, head)
		limit, count := intArgument(args, 3), 0
		for len(queue.pending) > 0 {
			commit := queue.pop()
			if state.visit(commit) {
				continue
			}
			queue.addParents(commit)
			count++
			// A cursor can identify only one starting commit. Finish a merge
			// before returning a cursor that would lose a queued branch.
			if limit > 0 && count >= limit && len(queue.pending) <= 1 {
				break
			}
		}
		var next string
		if len(queue.pending) > 0 && !state.missingFile {
			next = queue.pending[0].CommitID
		}
		return state.results(next), state.lookupError
	}
}

func (s *fileRevisionState) visit(commit *commitmgr.Commit) (stop bool) {
	if s.changes > 0 && s.cutoff == 0 {
		return true
	}
	if s.changes >= 2 && s.cutoff > 0 && commit.Ctime < s.cutoff {
		s.missingFile = true
		return true
	}
	defer delete(s.entries, commit.CommitID)
	entry, err := s.entry(commit)
	s.lookupError = err
	if err != nil {
		return false
	}
	if entry == nil {
		s.missingFile = true
		return true
	}
	if commit.ParentID.Valid {
		for _, parent := range []commitmgr.String{commit.ParentID, commit.SecondParentID} {
			if !parent.Valid {
				continue
			}
			previous, err := s.reader.Commit(s.repo.ID, parent.String)
			if err != nil {
				return false
			}
			oldEntry, lookupErr := s.entry(previous)
			s.lookupError = lookupErr
			if lookupErr != nil || (oldEntry != nil && oldEntry.ID == entry.ID) {
				return false
			}
		}
		s.changes++
	}
	s.revisions = append(s.revisions, fileRevision{commit, entry})
	return false
}

func (s *fileRevisionState) entry(commit *commitmgr.Commit) (*fsmgr.SeafDirent, *searpc.RPCError) {
	if s.path == "/" {
		_, rpcErr := s.reader.directoryByPath(s.repo, commit.RootID, s.path)
		return nil, rpcErr
	}
	if entry := s.entries[commit.CommitID]; entry != nil {
		return entry, nil
	}
	entry, rpcErr := s.reader.entryAtHistoryPath(s.repo, commit.RootID, s.path)
	if entry == nil || !fsmgr.IsRegular(entry.Mode) {
		return nil, rpcErr
	}
	s.entries[commit.CommitID] = entry
	return entry, nil
}

func (s *fileRevisionState) results(next string) []map[string]any {
	results := make([]map[string]any, 0, len(s.revisions)+1)
	for _, revision := range s.revisions {
		object := commitObject(revision.commit)
		object["rev-file-id"] = revision.file.ID
		size := revision.file.Size
		if s.repo.Version == 0 {
			size = -1
			if file, err := s.reader.File(s.repo, revision.file.ID); err == nil {
				size = int64(file.FileSize)
			}
		}
		object["rev-file-size"] = size
		results = append(results, object)
	}
	if len(s.revisions) > 0 {
		last := s.revisions[len(s.revisions)-1].commit
		if parent, oldPath := s.renameAt(last); parent != "" {
			results[len(results)-1]["rev-renamed-old-path"] = oldPath
			next = parent
		}
	}
	cursor := commitObject(&commitmgr.Commit{})
	cursor["creator"], cursor["desc"], cursor["repo-id"], cursor["root-id"] = nil, nil, nil, nil
	cursor["next-start-commit"] = optionalString(next)
	return append(results, cursor)
}

func (s *fileRevisionState) renameAt(commit *commitmgr.Commit) (parentID, oldPath string) {
	changes, err := s.reader.diffHistory(s.repo, nil, commit, false)
	if err != nil {
		return "", ""
	}
	for _, change := range changes {
		if change.Status != diff.DiffStatusRenamed || "/"+change.NewName != s.path {
			continue
		}
		path := formatPath(change.Name)
		if !commit.SecondParentID.Valid {
			return commit.ParentID.String, path
		}
		for _, parent := range []commitmgr.String{commit.ParentID, commit.SecondParentID} {
			old, err := s.reader.Commit(s.repo.ID, parent.String)
			if err == nil {
				entry, _ := s.reader.entryAtHistoryPath(s.repo, old.RootID, path)
				if entry != nil && utils.IsObjectIDValid(entry.ID) {
					return parent.String, path
				}
			}
		}
	}
	return "", ""
}
