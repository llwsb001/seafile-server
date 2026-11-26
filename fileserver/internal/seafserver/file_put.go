package seafserver

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/repocrypt"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
	"golang.org/x/text/unicode/norm"
)

func (w *fileWriter) putFile(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	if len(args) != 6 {
		return nil, rpcError(errBadArgs, "Invalid arguments")
	}
	values, rpcErr := fileArguments(args[:5], 5)
	if rpcErr != nil {
		return nil, rpcErr
	}
	var expectedHead *string
	if err := json.Unmarshal(args[5], &expectedHead); err != nil ||
		(expectedHead != nil && !utils.IsObjectIDValid(*expectedHead)) {
		return nil, rpcError(errBadCommit, "Invalid commit id")
	}
	repoID, tempPath, parentPath, name, user := values[0], values[1], values[2], norm.NFC.String(values[3]), values[4]
	if !validWriteName(name) {
		return nil, rpcError(errBadArgs, "Invalid filename")
	}
	parents, rpcErr := fileWritePath(parentPath)
	if rpcErr != nil || strings.Contains(parentPath, "//") {
		return nil, rpcError(errBadArgs, "Invalid parent dir")
	}
	tree, rpcErr := w.writableTree(ctx, repoID, user, true)
	if rpcErr != nil {
		return nil, rpcErr
	}
	// The optional head makes this overwrite conditional. A retry must use
	// a fresh head so a concurrent write to this file stays visible.
	if expectedHead != nil && *expectedHead != tree.repo.HeadCommitID {
		return nil, rpcError(520, "Concurrent upload")
	}
	input, err := os.OpenFile(tempPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, rpcError(errBadArgs, "Invalid input file")
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, rpcError(errBadArgs, "Invalid input file")
	}
	var fileID string
	rpcErr = tree.change(parents, true, func(dir *fsmgr.SeafDir) (bool, *searpc.RPCError) {
		index := findEntry(dir.Entries, name)
		if index < 0 || !fsmgr.IsRegular(dir.Entries[index].Mode) {
			return false, rpcError(errBadArgs, "file does not exist")
		}
		entry := dir.Entries[index]
		if delta := info.Size() - entry.Size; delta > 0 && w.quota.check(ctx, repoID, delta) != 0 {
			return false, rpcError(519, "Quota is full")
		}
		var rpcErr *searpc.RPCError
		fileID, rpcErr = w.indexPutFile(ctx, tree, input, info.Size())
		if rpcErr != nil || fileID == entry.ID {
			return false, rpcErr
		}
		dir.Entries[index] = fsmgr.NewDirent(fileID, name, emptyFileMode, tree.now, user, info.Size())
		return true, nil
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	if tree.root == tree.repo.RootID {
		return fileID, nil
	}
	commit, err := w.prepareCommit(tree, fmt.Sprintf("Modified \"%s\"", name))
	if err != nil {
		return nil, fileWriteFailure("write PUT commit", err)
	}
	if err := w.publishStagedFileWrite(ctx, tree.repo.HeadCommitID, commit); errors.Is(err, ErrSQLiteBranchChanged) {
		return nil, rpcError(520, "Concurrent upload")
	} else if err != nil {
		return nil, fileWriteFailure("publish PUT commit", err)
	}
	w.reader.PublishRepoUpdate(ctx, repoID, commit.CommitID, tree.repo.VirtualInfo != nil)
	return fileID, nil
}

func (w *fileWriter) indexPutFile(ctx context.Context, tree *fileTree, input io.Reader, size int64) (string, *searpc.RPCError) {
	var key, iv []byte
	if tree.repo.IsEncrypted {
		key, iv = w.passwords.copyKey(tree.repo.ID, tree.user)
		if key == nil {
			return "", rpcError(errGeneral, "Passwd is not set")
		}
		defer clear(key)
		defer clear(iv)
	}
	if size == 0 {
		return fsmgr.EmptySha1, nil
	}
	// The C RPC reads its fixed block size directly from the startup file.
	// The quota object carries that same startup configuration.
	config := w.quota.config
	value, found := config.file[configKey{"fileserver", "fixed_block_size"}]
	blockSize := int64((configValue{text: value, found: found}).int()) << 20
	if blockSize <= 0 {
		blockSize = 8 << 20
	}
	block := make([]byte, min(size, blockSize))
	var blockIDs []string
	for remaining := size; remaining > 0; remaining -= blockSize {
		if err := ctx.Err(); err != nil {
			return "", fileWriteFailure("index PUT file", err)
		}
		content := block[:min(remaining, blockSize)]
		if _, err := io.ReadFull(input, content); err != nil {
			return "", fileWriteFailure("read PUT input", err)
		}
		if key != nil {
			ciphertext, err := repocrypt.Encrypt(tree.repo.EncVersion, key, iv, content)
			if err != nil {
				return "", fileWriteFailure("encrypt PUT block", err)
			}
			content = ciphertext
		}
		sum := sha1.Sum(content)
		id := hex.EncodeToString(sum[:])
		if err := tree.reader.blocks.Write(tree.repo.StoreID, id, bytes.NewReader(content), true); err != nil {
			return "", fileWriteFailure("write PUT block", err)
		}
		blockIDs = append(blockIDs, id)
	}
	file, err := fsmgr.NewSeafile(tree.repo.Version, size, blockIDs)
	if err != nil {
		return "", fileWriteFailure("encode PUT file", err)
	}
	var data bytes.Buffer
	if err := file.ToData(&data); err != nil {
		return "", fileWriteFailure("compress PUT file", err)
	}
	if err := tree.reader.fs.Write(tree.repo.StoreID, file.FileID, &data, true); err != nil {
		return "", fileWriteFailure("write PUT file", err)
	}
	return file.FileID, nil
}
