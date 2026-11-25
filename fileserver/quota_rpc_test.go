package main

import (
	"context"
	"database/sql"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

func TestUploadQuotaUsesManagementWrites(t *testing.T) {
	root, err := os.MkdirTemp("", "seaf-quota-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	path := filepath.Join(root, "seafile.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const schema = `
CREATE TABLE SeafileConf (cfg_group TEXT, cfg_key TEXT, value TEXT, property INTEGER);
CREATE TABLE UserQuota (user TEXT PRIMARY KEY, quota INTEGER);
CREATE TABLE RepoOwner (repo_id TEXT PRIMARY KEY, owner_id TEXT);
CREATE TABLE VirtualRepo (repo_id TEXT PRIMARY KEY, origin_repo TEXT, path TEXT, base_commit TEXT);
CREATE TABLE RepoSize (repo_id TEXT PRIMARY KEY, size INTEGER);
INSERT INTO RepoOwner VALUES ('origin', 'owner@example.test'), ('virtual', 'member@example.test');
INSERT INTO VirtualRepo VALUES ('virtual', 'origin', '/subdir', 'fixture-head');
INSERT INTO RepoSize VALUES ('origin', 150), ('virtual', 200);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	writer, err := seafserver.OpenSQLiteReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	config := seafserver.NewServerConfig(db, nil)
	quota := seafserver.NewUserQuota(db, config)
	handlers := quota.ManagementRPCHandlers(writer)
	maps.Copy(handlers, config.RPCHandlers(writer))
	endpoint := filepath.Join(root, "seafile.sock")
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- searpc.Serve(ctx, listener, seafserver.RPCService, handlers)
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	previousDB, previousTimeout := seafileDB, option.DBOpTimeout
	seafileDB, option.DBOpTimeout = db, time.Second
	repomgr.Init(db)
	defer func() {
		seafileDB, option.DBOpTimeout = previousDB, previousTimeout
		repomgr.Init(previousDB)
	}()
	client := searpc.Init(endpoint, seafserver.RPCService, 0)
	setQuota := func(user string, value int64) {
		t.Helper()
		if result, err := client.Call("set_user_quota", user, value); err != nil || result != float64(0) {
			t.Fatalf("setter returned %v, %v", result, err)
		}
	}
	setQuota("owner@example.test", 500)
	setQuota("member@example.test", 2000)
	request := httptest.NewRequest(http.MethodPost, "/upload-api/test", nil)
	if appErr := checkQuotaByContentLength(request, "virtual", 349); appErr != nil {
		t.Fatalf("upload one byte below the origin owner's quota was denied: %v", appErr)
	}
	if appErr := checkQuotaByContentLength(request, "virtual", 350); appErr == nil || appErr.Code != seafHTTPResNoQuota {
		t.Fatalf("upload at the origin owner's quota should return no-quota: %v", appErr)
	}
	setQuota("owner@example.test", 501)
	if appErr := checkQuotaByContentLength(request, "virtual", 350); appErr != nil {
		t.Fatalf("the consumer did not observe the committed quota update: %v", appErr)
	}
	setQuota("owner@example.test", 0)
	if result, err := client.Call("set_server_config_string", "quota", "default", "1KB"); err != nil || result != float64(0) {
		t.Fatalf("setting the default quota returned %v, %v", result, err)
	}
	if appErr := checkQuotaByContentLength(request, "virtual", 849); appErr != nil {
		t.Fatalf("upload below the SQLite default quota was denied: %v", appErr)
	}
	if appErr := checkQuotaByContentLength(request, "virtual", 850); appErr == nil || appErr.Code != seafHTTPResNoQuota {
		t.Fatalf("upload at the SQLite default quota should return no-quota: %v", appErr)
	}
	if _, err := db.Exec("UPDATE UserQuota SET quota = NULL WHERE user = ?", "owner@example.test"); err != nil {
		t.Fatal(err)
	}
	if appErr := checkQuotaByContentLength(request, "virtual", 849); appErr != nil {
		t.Fatalf("a NULL explicit quota should fall back to the SQLite default: %v", appErr)
	}
}
