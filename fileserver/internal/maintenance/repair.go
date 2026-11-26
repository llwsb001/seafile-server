package maintenance

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
)

type repairChanges struct {
	at          int64
	files, dirs []string
	blocks      map[string]bool
}

func (c *repoCheck) reportDamagedFile(entry *fsmgr.SeafDirent, path string) {
	if !c.opts.Repair {
		c.log("Repo[%.8s] file %s(%.8s) is damaged.", c.repo.ID, path, entry.ID)
		return
	}
	c.log("Repo[%.8s] file %s(%.8s) is damaged, recreate an empty file.", c.repo.ID, path, entry.ID)
	c.repair.files = append(c.repair.files, path)
	entry.ID = fsmgr.EmptySha1
	entry.Size = 0
	entry.Mtime = c.repair.at
}

func (c *repoCheck) reportDamagedDir(entry *fsmgr.SeafDirent, path string) {
	if !c.opts.Repair {
		c.log("Repo[%.8s] dir %s(%.8s) is damaged.", c.repo.ID, path, entry.ID)
		return
	}
	c.log("Repo[%.8s] dir %s(%.8s) is damaged, recreate an empty dir.", c.repo.ID, path, entry.ID)
	c.repair.dirs = append(c.repair.dirs, path)
	entry.ID = fsmgr.EmptySha1
}

func (c *repoCheck) reportDamagedBlock(id string) {
	c.log("Repo[%.8s] block %s is damaged.", c.repo.ID, id)
	if c.opts.Repair {
		c.repair.blocks[id] = true
	}
}

func (c *repoCheck) publishRepair(ctx context.Context, parent *commitmgr.Commit, root string) {
	metadata := parent
	if c.head != nil {
		metadata = c.head
	}
	author := *parent
	author.CreatorName = asciiLower(author.CreatorName)
	newCommit := commitmgr.NewCanonicalRepairCommit(c.repo.ID, root, &author, c.repair.description(), c.repair.at)
	copyRepairMetadata(newCommit, metadata)
	if err := commitmgr.SaveCanonicalToStore(c.store.commits, newCommit); err != nil {
		c.log("Failed to add commit for repo %.8s, abort repair: %v.", c.repo.ID, err)
		return
	}
	saved, err := c.store.Commit(c.repo, newCommit.CommitID)
	if err != nil || saved.RootID != newCommit.RootID || !saved.ParentID.Equal(newCommit.ParentID) {
		c.log("Failed to verify repair commit for repo %.8s, abort repair.", c.repo.ID)
		return
	}
	if _, err := seafserver.PublishSQLiteRepair(ctx, c.store.db, c.repo.ID, c.repo.HeadID, newCommit.CommitID); err != nil {
		c.log("Failed to update repo %.8s status to commit %.8s: %v.", c.repo.ID, newCommit.CommitID, err)
		return
	}
	c.log("Update repo %.8s status to commit %.8s.", c.repo.ID, newCommit.CommitID)
	c.removeDamagedBlocks()
}

func copyRepairMetadata(replacement, original *commitmgr.Commit) {
	replacement.RepoName = original.RepoName
	replacement.RepoDesc = original.RepoDesc
	replacement.Version = original.Version
	replacement.NoLocalHistory = original.NoLocalHistory
	replacement.Repaired = original.Repaired
	replacement.Encrypted = original.Encrypted
	replacement.EncVersion = original.EncVersion
	replacement.Magic = original.Magic
	replacement.RandomKey = original.RandomKey
	replacement.Salt = original.Salt
	replacement.PwdHash = original.PwdHash
	replacement.PwdHashAlgo = original.PwdHashAlgo
	replacement.PwdHashParams = original.PwdHashParams
}

func (changes repairChanges) description() string {
	var text strings.Builder
	text.WriteString("Repaired by system.")
	for _, section := range []struct {
		heading string
		paths   []string
	}{{"Damaged files", changes.files}, {"Damaged folders", changes.dirs}} {
		if len(section.paths) == 0 {
			continue
		}
		text.WriteString("\n" + section.heading + ":\n")
		for index := len(section.paths) - 1; index >= 0; index-- {
			text.WriteString(section.paths[index] + "\n")
		}
	}
	return text.String()
}

func (c *repoCheck) removeDamagedBlocks() {
	if len(c.repair.blocks) == 0 {
		return
	}
	root, err := canonicalPath(c.store.dataDir)
	if err != nil {
		c.log("Failed to resolve the source data directory: %v.", err)
		return
	}
	ids := make([]string, 0, len(c.repair.blocks))
	for id := range c.repair.blocks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		digest := sha1.New()
		err := c.store.ReadBlock(c.repo, id, digest)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			c.log("Failed to recheck block %s: %v.", id, err)
			continue
		}
		if hex.EncodeToString(digest.Sum(nil)) == id {
			continue
		}
		if err := removeGCObject(root, "blocks", c.repo.StoreID, id); err != nil {
			c.log("Failed to remove damaged block %s: %v.", id, err)
			continue
		}
		c.log("Repo[%.8s] block %s is damaged, remove it.", c.repo.ID, id)
	}
}
