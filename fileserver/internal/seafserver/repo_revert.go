package seafserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

var fixedPOSIXTZ = regexp.MustCompile(`^([A-Za-z]{3,})([+-]?)([0-9]{1,2})(:([0-9]{2})(:([0-9]{2}))?)?$`)

func repoRevertTime(ctime int64) time.Time {
	timestamp := time.Unix(ctime, 0)
	parts := fixedPOSIXTZ.FindStringSubmatch(os.Getenv("TZ"))
	if parts == nil {
		return timestamp
	}
	hour, _ := strconv.Atoi(parts[3])
	minute, _ := strconv.Atoi(parts[5])
	second, _ := strconv.Atoi(parts[7])
	if hour > 24 || minute > 59 || second > 59 {
		return timestamp
	}
	offset := hour*3600 + minute*60 + second
	// A POSIX offset is added to local time to get UTC.
	if parts[2] != "-" {
		offset = -offset
	}
	return timestamp.In(time.FixedZone(parts[1], offset))
}

func (w *fileWriter) revertRepo(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	id, target, user := stringArgument(args, 0), stringArgument(args, 1), stringArgument(args, 2)
	if id == nil || len(*id) != 36 || target == nil || len(*target) != 40 || user == nil {
		return -1, rpcError(errBadArgs, "Bad arguments")
	}
	if !validRepoID(*id) {
		return -1, rpcError(errBadArgs, "Invalid repo id")
	}
	if !utils.IsObjectIDValid(*target) {
		return -1, rpcError(errBadArgs, "Invalid commit id")
	}
	for range maxFileWriteRetries {
		tree, rpcErr := w.writableTree(ctx, *id, *user, true)
		if rpcErr != nil {
			if rpcErr.Code == errBadArgs && rpcErr.Message == "Invalid repo" {
				return -1, rpcError(errGeneral, "No such repo")
			}
			return -1, rpcErr
		}
		commit, err := w.reader.Commit(tree.repo.ID, *target)
		if err != nil {
			return -1, rpcError(errGeneral, "Commit doesn't exist")
		}
		tree.root = commit.RootID
		description := "Reverted repo to status at " + repoRevertTime(commit.Ctime).Format(time.DateTime) + "."
		if err := w.publish(ctx, tree, description); errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		} else if err != nil {
			return -1, fileWriteFailure("publish repository revert", err)
		}
		return 0, nil
	}
	return -1, rpcError(520, "Concurrent update")
}
