package main

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestManagementRPCBootstrap(t *testing.T) {
	t.Setenv("ENABLE_GO_FILESERVER", "false")
	for _, enabled := range []bool{false, true} {
		name := "default"
		if enabled {
			name = "management"
		}
		t.Run(name, func(t *testing.T) {
			// Keep the path within the Unix socket address limit on macOS.
			root, err := os.MkdirTemp("", "seaf-rpc-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			const conf = "[database]\ntype=sqlite\n[fileserver]\nuse_go_fileserver=false\n"
			if err := os.WriteFile(filepath.Join(root, "seafile.conf"), []byte(conf), 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", filepath.Join(root, "seafile.db"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec("CREATE TABLE SeafileConf (cfg_group TEXT, cfg_key TEXT, value TEXT, property INTEGER)")
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			ready := make(chan error, 1)
			args := []string{"-f", "-d", root}
			if enabled {
				args = append(args, "--enable-management-rpcs")
			}
			go func() { ready <- run(ctx, args) }()
			defer func() {
				cancel()
				if err := <-ready; err != nil {
					t.Errorf("server stopped with error: %v", err)
				}
			}()

			path := filepath.Join(root, "seafile.sock")
			waitForRPC(t, path, enabled)
			client := searpc.Init(path, seafserver.RPCService, 0)
			for _, method := range []string{
				"seafile_check_passwd", "seafile_set_passwd", "seafile_unset_passwd",
				"seafile_is_passwd_set", "seafile_get_decrypt_key",
			} {
				wantError := "cannot find function " + method
				if enabled {
					wantError = "Bad arguments"
				}
				if _, err := client.Call(method); err == nil || !strings.Contains(err.Error(), wantError) {
					t.Errorf("%s returned %v, want %q", method, err, wantError)
				}
			}
			result, err := client.Call("set_server_config_string", "example", "value", "SQLite")
			if !enabled {
				if err == nil || !strings.Contains(err.Error(), "cannot find function set_server_config_string") {
					t.Fatalf("management available with default flags: %v, %v", result, err)
				}
				return
			}
			if err != nil || result != float64(0) {
				t.Fatalf("enabled setter returned %v, %v", result, err)
			}
			result, err = client.Call("get_server_config_string", "example", "value")
			if err != nil || result != "SQLite" {
				t.Fatalf("enabled getter returned %v, %v", result, err)
			}
			if _, err := client.Call("seafile_web_get_access_token"); err == nil ||
				!strings.Contains(err.Error(), "cannot find function seafile_web_get_access_token") {
				t.Fatalf("management flag changed Web token registration: %v", err)
			}
		})
	}
}

func waitForRPC(t *testing.T, path string, private bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err != nil {
			if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ECONNREFUSED) {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}
		conn.Close()
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if private && (info.Mode().Perm() != 0o700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid())) {
			t.Fatalf("connectable management socket has mode %v and uid %d", info.Mode(), info.Sys().(*syscall.Stat_t).Uid)
		}
		return
	}
	t.Fatal("server socket never became connectable")
}
