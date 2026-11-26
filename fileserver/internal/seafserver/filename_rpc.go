package seafserver

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func isValidFilename(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, name := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || name == nil {
		return -1, rpcError(errBadArgs, "Argument should not be null")
	}
	if len(*name) >= 256 || *name == ".." || strings.ContainsAny(*name, "/\x00") || !utf8.ValidString(*name) {
		return 0, nil
	}
	return 1, nil
}
