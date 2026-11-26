package seafserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestIsValidFilename(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		want int
	}{
		{"", 1}, {".", 1}, {"..", 0}, {"a/b", 0}, {"a\\b", 1}, {"a\x00b", 0},
		{".DS_Store", 1}, {"k.tmp", 1}, {strings.Repeat("a", 255), 1},
		{strings.Repeat("a", 256), 0}, {strings.Repeat("é", 127), 1}, {strings.Repeat("é", 128), 0},
	} {
		result, rpcErr := isValidFilename(ctx, fileWriteRPCArgs(t, "", tt.name))
		if result != tt.want || rpcErr != nil {
			t.Fatalf("filename %q = (%v, %v), want %d", tt.name, result, rpcErr, tt.want)
		}
	}
	result, rpcErr := isValidFilename(ctx, []json.RawMessage{json.RawMessage("null"), json.RawMessage(`"name"`)})
	if result != -1 || rpcErr == nil || rpcErr.Code != errBadArgs {
		t.Fatalf("null repository = (%v, %v), want invalid arguments", result, rpcErr)
	}
}
