package commitmgr

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/haiwen/seafile-server/fileserver/objstore"
)

// NewCanonicalCommit computes the object ID using the C server's commit format.
func NewCanonicalCommit(repoID, parentID, rootID, creator, description string, timestamp int64) *Commit {
	commit := newCommit(repoID, parentID, rootID, creator, description, timestamp)
	hash := sha1.New()
	var root, creatorID [41]byte // The C hash includes the NUL terminator of each fixed ID.
	copy(root[:40], commit.RootID)
	copy(creatorID[:40], commit.CreatorID)
	hash.Write(root[:])
	hash.Write(creatorID[:])
	hash.Write(append([]byte(commit.CreatorName), 0))
	hash.Write(append([]byte(commit.Desc), 0))
	var ctime [8]byte
	binary.BigEndian.PutUint64(ctime[:], uint64(commit.Ctime))
	hash.Write(ctime[:])
	commit.CommitID = hex.EncodeToString(hash.Sum(nil))
	return commit
}

// SaveCanonicalToStore serializes and flushes a server-generated C format commit.
func SaveCanonicalToStore(objects *objstore.ObjectStore, commit *Commit) error {
	// The hash includes creator_name even when empty. An empty category is null
	// in a server-generated commit, and an unencrypted commit omits encrypted.
	object := struct {
		*Commit
		CreatorName  string  `json:"creator_name"`
		RepoCategory *string `json:"repo_category"`
		Encrypted    string  `json:"encrypted,omitempty"`
	}{Commit: commit, CreatorName: commit.CreatorName}
	if commit.RepoCategory != "" {
		object.RepoCategory = &commit.RepoCategory
	}
	if commit.Encrypted == "true" {
		object.Encrypted = commit.Encrypted
	}
	payload, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return saveIfMissing(objects, commit.RepoID, commit.CommitID, bytes.NewReader(payload), true)
}
