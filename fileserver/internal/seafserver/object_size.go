package seafserver

import (
	"context"
	"encoding/json"

	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

func (r *Reader) fileSize(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	store, id := stringArgument(args, 0), stringArgument(args, 2)
	if store == nil || !validRepoID(*store) {
		return -1, rpcError(errBadArgs, "Invalid store id")
	}
	if id == nil || !utils.IsObjectIDValid(*id) {
		return -1, rpcError(errBadArgs, "Invalid file id")
	}
	size := r.readFileSize(*store, *id, intArgument(args, 1))
	if size < 0 {
		return -1, rpcError(errInternal, "failed to read file size")
	}
	return size, nil
}

func (r *Reader) dirSize(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	store, id := stringArgument(args, 0), stringArgument(args, 2)
	if store == nil || !validRepoID(*store) {
		return -1, rpcError(errBadArgs, "Invalid store id")
	}
	if id == nil || !utils.IsObjectIDValid(*id) {
		return -1, rpcError(errBadArgs, "Invalid dir id")
	}
	size := r.directorySize(*store, *id, intArgument(args, 1))
	if size < 0 {
		return -1, rpcError(errBadArgs, "Failed to caculate dir size")
	}
	return size, nil
}

func (r *Reader) readFileSize(store, id string, version int) int64 {
	file, err := fsmgr.ReadSeafile(r.fs, store, id, version)
	if err != nil {
		return -1
	}
	return int64(file.FileSize)
}

func (r *Reader) directorySize(store, id string, version int) int64 {
	dir, err := fsmgr.ReadSeafdir(r.fs, store, id, version)
	if err != nil {
		return -1
	}

	var total int64
	for _, entry := range dir.Entries {
		switch {
		case fsmgr.IsDir(entry.Mode):
			size := r.directorySize(store, entry.ID, version)
			if size < 0 {
				return -1
			}
			total += size
		case fsmgr.IsRegular(entry.Mode):
			if dir.Version > 0 {
				total += entry.Size
				continue
			}
			size := r.readFileSize(store, entry.ID, version)
			if size < 0 {
				return -1
			}
			total += size
		}
	}
	return total
}
