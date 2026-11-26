package seafserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

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
		description := "Reverted repo to status at " + time.Unix(commit.Ctime, 0).Local().Format(time.DateTime) + "."
		if err := w.publish(ctx, tree, description); errors.Is(err, ErrSQLiteBranchChanged) {
			continue
		} else if err != nil {
			return -1, fileWriteFailure("publish repository revert", err)
		}
		return 0, nil
	}
	return -1, rpcError(520, "Concurrent update")
}
