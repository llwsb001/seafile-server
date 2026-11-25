package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestParseWebAccessTokenRPC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")
	for _, mode := range []string{"enabled", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}

			var handlers map[string]searpc.Handler
			if mode == "enabled" {
				handlers = seafserver.NewWebAccessTokens(3600).RPCHandlers()
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- searpc.Serve(ctx, listener, seafserver.RPCService, handlers) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			original := rpcclient
			rpcclient = searpc.Init(path, seafserver.RPCService, 2)
			defer func() { rpcclient = original }()

			if mode == "disabled" {
				if _, appErr := parseWebaccessInfo("unissued"); appErr == nil || appErr.Code != http.StatusInternalServerError {
					t.Fatal("an unregistered query RPC should map to HTTP 500")
				}
				return
			}
			want := webaccessInfo{repoID: "repo", objID: "object", op: "download", user: "reader@example.test"}
			value, err := rpcclient.Call("seafile_web_get_access_token", want.repoID, want.objID, want.op, want.user, 1)
			if err != nil {
				t.Fatal(err)
			}
			token, ok := value.(string)
			if !ok {
				t.Fatal("the issue RPC should return a token string")
			}
			got, appErr := parseWebaccessInfo(token)
			if appErr != nil || got == nil || *got != want {
				t.Fatal("the current fileserver parser should receive all four access fields")
			}
			if _, appErr := parseWebaccessInfo(token); appErr == nil || appErr.Code != http.StatusForbidden {
				t.Fatal("a consumed token should map to HTTP 403")
			}
		})
	}
}
