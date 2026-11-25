package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestGroupManagementRPC(t *testing.T) {
	t.Setenv("ENABLE_GO_FILESERVER", "false")
	t.Setenv("TMPDIR", "/tmp")
	for _, setup := range []struct {
		name       string
		management bool
		ccnet      bool
	}{
		{name: "default-with-ccnet", ccnet: true},
		{name: "management-without-ccnet", management: true},
		{name: "management-with-ccnet", management: true, ccnet: true},
	} {
		t.Run(setup.name, func(t *testing.T) {
			root := t.TempDir()
			const config = "[database]\ntype=sqlite\n[fileserver]\nuse_go_fileserver=false\n"
			if err := os.WriteFile(filepath.Join(root, "seafile.conf"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "seafile.db"), nil, 0o600); err != nil {
				t.Fatal(err)
			}

			args := []string{"-f", "-d", root}
			if setup.management {
				args = append(args, "--enable-management-rpcs")
			}
			if setup.ccnet {
				ccnet := filepath.Join(root, "ccnet")
				args = append(args, "-c", ccnet)
				for _, location := range []string{"PeerMgr/usermgr.db", "GroupMgr/groupmgr.db", "OrgMgr/orgmgr.db"} {
					path := filepath.Join(ccnet, location)
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				groups, err := sql.Open("sqlite", filepath.Join(ccnet, "GroupMgr", "groupmgr.db"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = groups.Exec(`CREATE TABLE "Group" (group_id INTEGER PRIMARY KEY, group_name TEXT);
INSERT INTO "Group" VALUES (1, 'Previous');`)
				groups.Close()
				if err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			stopped := make(chan error, 1)
			go func() { stopped <- run(ctx, args) }()
			defer func() {
				cancel()
				if err := <-stopped; err != nil {
					t.Error(err)
				}
			}()
			path := filepath.Join(root, "seafile.sock")
			waitForRPC(t, path, setup.management)
			client := searpc.Init(path, seafserver.RPCService, 0)
			result, err := client.Call("set_group_name", 1, "Updated")
			if setup.management && setup.ccnet {
				if err != nil || result != float64(0) {
					t.Fatalf("enabled group RPC returned (%v, %v)", result, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "cannot find function set_group_name") {
				t.Fatalf("disabled group RPC returned (%v, %v)", result, err)
			}
		})
	}
}
