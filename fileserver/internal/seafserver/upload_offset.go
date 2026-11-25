package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
	"golang.org/x/sys/unix"
)

const UploadTempFileMtimeAttr = "user.seafile.file_mtime"

// UploadOffsetRPC returns the number of bytes stored for a resumable upload
// and discards an obsolete partial file when Seahub reports a different mtime.
func UploadOffsetRPC(db *sql.DB) searpc.Handler {
	return func(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
		repoID, path := stringArgument(args, 0), stringArgument(args, 1)
		if repoID == nil || !validRepoID(*repoID) {
			return nil, rpcError(errBadArgs, "Invalid repo id")
		}
		if path == nil || *path == "" {
			return nil, rpcError(errBadArgs, "Invalid file path")
		}
		offset, err := uploadOffset(ctx, db, *repoID, formatPath(*path), int64Argument(args, 2))
		if err != nil {
			return nil, rpcError(errInternal, err.Error())
		}
		return offset, nil
	}
}

func uploadOffset(ctx context.Context, db *sql.DB, repoID, path string, mtime int64) (int64, error) {
	relativePath := strings.TrimPrefix(path, "/")
	var tempPath string
	err := db.QueryRowContext(ctx, `SELECT tmp_file_path FROM WebUploadTempFiles
		WHERE repo_id = ? AND file_path IN (?, ?) ORDER BY file_path = ? DESC LIMIT 1`,
		repoID, path, relativePath, path).Scan(&tempPath)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, errors.New("Failed to get upload temp file path from db.")
	}

	info, err := os.Stat(tempPath)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return 0, errors.New("Failed to stat temp file.")
	}
	if !missing {
		if mtime <= 0 {
			return info.Size(), nil
		}
		var attr [32]byte
		n, err := unix.Getxattr(tempPath, UploadTempFileMtimeAttr, attr[:])
		if err != nil {
			return info.Size(), nil
		}
		storedMtime, _ := strconv.ParseInt(string(attr[:n]), 10, 64)
		if storedMtime == mtime {
			return info.Size(), nil
		}
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, errors.New("Failed to remove temp file.")
		}
	}
	_, err = db.ExecContext(ctx, `DELETE FROM WebUploadTempFiles
		WHERE repo_id = ? AND file_path IN (?, ?) AND tmp_file_path = ?`,
		repoID, path, relativePath, tempPath)
	if err != nil {
		return 0, errors.New("Failed to remove upload temp file from db.")
	}
	return 0, nil
}
