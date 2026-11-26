package seafserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/utils"
)

func (r *Reader) listFileBlocks(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	repoID, fileID := stringArgument(args, 0), stringArgument(args, 1)
	if repoID == nil || !validRepoID(*repoID) {
		return nil, rpcError(errBadDirID, "Bad repo id")
	}
	if fileID == nil || !utils.IsObjectIDValid(*fileID) {
		return nil, rpcError(errBadDirID, "Bad file id")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errBadArgs, "Bad repo id")
	}
	file, err := r.File(repo, *fileID)
	if err != nil {
		return nil, rpcError(errBadDirID, "Bad file id")
	}

	start, end := max(0, intArgument(args, 2)), len(file.BlkIDs)
	if limit := intArgument(args, 3); limit > 0 {
		// The locked C implementation computes the end of a page as a signed 32-bit integer.
		end = min(end, int(int32(start)+int32(limit)))
	}
	if start >= end {
		return "", nil
	}
	return strings.Join(file.BlkIDs[start:end], "\n") + "\n", nil
}

// The response is a serialized JSON array. Preserve Jansson's original line
// separators while leaving escaped backslashes alone.
var blockResultEscapes = strings.NewReplacer(`\\`, `\\`, `\u2028`, "\u2028", `\u2029`, "\u2029")

func (r *Reader) checkRepoBlocksMissing(ctx context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	encoded := stringArgument(args, 1)
	if encoded == nil {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	var collection any
	if err := json.Unmarshal([]byte(*encoded), &collection); err != nil {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	values, array := collection.([]any)
	_, object := collection.(map[string]any)
	// C accepts JSON objects and iterates them as empty arrays.
	if !array && !object {
		return nil, rpcError(errBadArgs, "Bad arguments")
	}
	repoID := stringArgument(args, 0)
	if repoID == nil {
		return nil, rpcError(errBadArgs, "Repo not found")
	}
	repo, err := r.Repo(ctx, *repoID)
	if err != nil {
		return nil, rpcError(errBadArgs, "Repo not found")
	}

	var result strings.Builder
	var quoted bytes.Buffer
	encoder := json.NewEncoder(&quoted)
	encoder.SetEscapeHTML(false)
	result.WriteByte('[')
	for _, value := range values {
		id, ok := value.(string)
		if !ok {
			continue
		}
		// The C upload probe checks the logical repository's block namespace.
		if utils.IsObjectIDValid(id) {
			if exists, err := r.blocks.Exists(repo.ID, id); exists && err == nil {
				continue
			}
		}
		if result.Len() > 1 {
			result.WriteString(", ")
		}
		quoted.Reset()
		_ = encoder.Encode(id) // Encoding a string to a buffer cannot fail.
		blockResultEscapes.WriteString(&result, strings.TrimSuffix(quoted.String(), "\n"))
	}
	result.WriteByte(']')
	return result.String(), nil
}
