package seafserver

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/repocrypt"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

type transferKey struct {
	version int
	key, iv []byte
}

func (c *libraryCopy) loadKeys(passwords *RepoPasswords) *searpc.RPCError {
	for _, side := range []struct {
		tree *fileTree
		key  *transferKey
	}{{c.from, &c.sourceKey}, {c.to, &c.targetKey}} {
		if side.tree.repo.IsEncrypted {
			key, iv := passwords.copyKey(side.tree.repo.ID, side.tree.user)
			if key == nil {
				return copyFailure(errors.New("copy or move requires an unlocked library"))
			}
			*side.key = transferKey{version: side.tree.repo.EncVersion, key: key, iv: iv}
		}
	}
	return nil
}

func (c *libraryCopy) clearKeys() {
	clear(c.sourceKey.key)
	clear(c.sourceKey.iv)
	clear(c.targetKey.key)
	clear(c.targetKey.iv)
}

func (c *libraryCopy) copyFile(file *fsmgr.Seafile) (string, error) {
	if file.FileID == fsmgr.EmptySha1 {
		return fsmgr.EmptySha1, nil
	}
	if c.sourceKey.version != 0 || c.targetKey.version != 0 {
		return c.transcodeFile(file)
	}
	fromStore, toStore := c.from.repo.StoreID, c.to.repo.StoreID
	if fromStore == toStore {
		return file.FileID, nil
	}
	for _, id := range file.BlkIDs {
		if err := c.ctx.Err(); err != nil {
			return "", err
		}
		if exists, err := c.to.reader.blocks.Exists(toStore, id); err == nil && exists {
			// A previous transfer can leave a block in place after a failed directory sync.
			if err := c.to.reader.blocks.Sync(toStore, id); err != nil {
				return "", err
			}
			continue
		}
		if err := c.from.reader.blocks.Copy(fromStore, toStore, id); err != nil {
			return "", err
		}
	}
	return file.FileID, c.from.reader.fs.Copy(fromStore, toStore, file.FileID)
}

func (c *libraryCopy) transcodeFile(file *fsmgr.Seafile) (string, error) {
	ids := make([]string, len(file.BlkIDs))
	for i, id := range file.BlkIDs {
		if err := c.ctx.Err(); err != nil {
			return "", err
		}
		var err error
		ids[i], err = c.transcodeBlock(id)
		if err != nil {
			return "", err
		}
	}
	output, err := fsmgr.NewSeafile(file.Version, int64(file.FileSize), ids)
	if err != nil {
		return "", err
	}
	var data bytes.Buffer
	if err := output.ToData(&data); err != nil {
		return "", err
	}
	return output.FileID, c.to.reader.fs.Write(c.to.repo.StoreID, output.FileID, &data, true)
}

func (c *libraryCopy) transcodeBlock(id string) (string, error) {
	var input bytes.Buffer
	if err := c.from.reader.blocks.Read(c.from.repo.StoreID, id, &input); err != nil {
		return "", err
	}
	data := input.Bytes()
	if key := c.sourceKey; key.version != 0 {
		var err error
		data, err = repocrypt.Decrypt(key.version, key.key, key.iv, data)
		if err != nil {
			return "", err
		}
	}
	if key := c.targetKey; key.version != 0 {
		var err error
		data, err = repocrypt.Encrypt(key.version, key.key, key.iv, data)
		if err != nil {
			return "", err
		}
	}
	checksum := sha1.Sum(data)
	targetID := hex.EncodeToString(checksum[:])
	target := c.to.repo.StoreID
	if exists, err := c.to.reader.blocks.Exists(target, targetID); err == nil && exists {
		return targetID, c.to.reader.blocks.Sync(target, targetID)
	}
	return targetID, c.to.reader.blocks.Write(target, targetID, bytes.NewReader(data), true)
}
