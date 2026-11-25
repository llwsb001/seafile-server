package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const syncFixtureRepo = "33ce892b-56e2-4352-8608-79075c6511cd"

func TestSQLitePermissionHonorsRequestCancellation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oldDB, oldClient, oldTimeout := seafileDB, rpcclient, option.DBOpTimeout
	repomgr.Init(db)
	rpcclient = searpc.Init(filepath.Join(t.TempDir(), "seafile.sock"), seafserver.RPCService, 0)
	option.DBOpTimeout = time.Second
	defer func() {
		repomgr.Init(oldDB)
		rpcclient, option.DBOpTimeout = oldClient, oldTimeout
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	appErr := checkPermission(ctx, syncFixtureRepo, "reader@example.test", "download", false)
	if appErr == nil || !errors.Is(appErr.Error, context.Canceled) {
		t.Fatalf("expected cancelled permission lookup, got %v", appErr)
	}
}

func TestSQLiteSyncTokenRevocation(t *testing.T) {
	if path := os.Getenv("SEAFILE_TEST_SYNC_CONSUMER_DB"); path != "" {
		runSyncTokenConsumer(t, path)
		return
	}
	t.Setenv("TMPDIR", "/tmp")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "seafile.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("../scripts/sql/sqlite/seafile.sql")
	if err != nil {
		t.Fatal(err)
	}
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(string(schema))

	writer, err := seafserver.OpenSQLiteReadWrite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	socket := filepath.Join(dir, "rpc")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	rpcCtx, cancelRPC := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		handlers := seafserver.NewReader(db, dir).SyncTokenRPCHandlers(writer)
		done <- searpc.Serve(rpcCtx, listener, seafserver.RPCService, handlers)
	}()
	defer func() {
		cancelRPC()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	rpc := searpc.Init(socket, seafserver.RPCService, 0)
	issue := func(email string) string {
		t.Helper()
		value, err := rpc.Call("seafile_generate_repo_token", syncFixtureRepo, email)
		token, ok := value.(string)
		if err != nil || !ok || len(token) != 40 {
			t.Fatal("issuer did not return a 40-character sync token")
		}
		return token
	}
	revoke := func(method string, args ...any) {
		t.Helper()
		value, err := rpc.Call(method, args...)
		if err != nil || value != float64(0) {
			t.Fatalf("%s did not acknowledge revocation: %v", method, err)
		}
	}

	const alice, bob = "alice@example.test", "bob@example.test"
	const device = "a123456789012345678901234567890123456789"
	legacy := "0123456789abcdef0123456789abcdef01234567"
	execSQL("INSERT INTO RepoUserToken (repo_id, email, token) VALUES (?, ?, ?)", syncFixtureRepo, alice, legacy)
	addPeer := func(token string) {
		execSQL("INSERT INTO RepoTokenPeerInfo (token, peer_id) VALUES (?, ?)", token, device)
	}
	addPeer(legacy)
	withoutPeer := issue(alice)
	unrelated := issue(bob)
	addPeer(unrelated)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	consumer := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteSyncTokenRevocation$")
	consumer.Env = append(os.Environ(), "SEAFILE_TEST_SYNC_CONSUMER_DB="+dbPath)
	consumer.Stderr = os.Stderr
	requests, err := consumer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := consumer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := requests.Close(); err != nil {
			t.Error(err)
		}
		if err := consumer.Wait(); err != nil {
			t.Error(err)
		}
	}()
	statuses := bufio.NewScanner(output)
	request := func(token string, expected int) {
		t.Helper()
		if _, err := fmt.Fprintln(requests, token); err != nil {
			t.Fatal(err)
		}
		if !statuses.Scan() || statuses.Text() != strconv.Itoa(expected) {
			t.Fatalf("sync HTTP status = %q, want %d", statuses.Text(), expected)
		}
	}

	request(legacy, http.StatusOK)
	request(withoutPeer, http.StatusOK)
	request(unrelated, http.StatusOK)
	if _, err := rpc.Call("seafile_delete_repo_token", syncFixtureRepo, legacy, bob); err == nil {
		t.Fatal("another account can revoke the token")
	}
	request(legacy, http.StatusOK)

	execSQL(`CREATE TRIGGER reject_peer_delete BEFORE DELETE ON RepoTokenPeerInfo
		BEGIN SELECT RAISE(ABORT, 'injected peer cleanup failure'); END`)
	if _, err := rpc.Call("seafile_delete_repo_token", syncFixtureRepo, legacy, alice); err == nil {
		t.Fatal("expected a failed revocation")
	}
	request(legacy, http.StatusOK)
	var stored int
	if err := db.QueryRow("SELECT COUNT(*) FROM RepoTokenPeerInfo WHERE token = ?", legacy).Scan(&stored); err != nil || stored != 1 {
		t.Fatal("failed revocation must preserve the peer row")
	}
	execSQL("DROP TRIGGER reject_peer_delete")

	revoke("seafile_delete_repo_token", syncFixtureRepo, legacy, alice)
	request(legacy, http.StatusForbidden)
	revoke("delete_repo_tokens_by_email", alice)
	request(withoutPeer, http.StatusForbidden)
	request(unrelated, http.StatusOK)

	attached := issue(alice)
	addPeer(attached)
	request(attached, http.StatusOK)
	revoke("seafile_delete_repo_tokens_by_peer_id", alice, device)
	request(attached, http.StatusForbidden)
	request(unrelated, http.StatusOK)
	if err := db.QueryRow("SELECT COUNT(*) FROM RepoTokenPeerInfo").Scan(&stored); err != nil || stored != 1 {
		t.Fatal("revocation must clear only its peer rows")
	}
}

func runSyncTokenConsumer(t *testing.T, path string) {
	db, err := seafserver.OpenSQLiteReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repomgr.Init(db)
	option.DBOpTimeout = 2 * time.Second
	option.EnableNotification = true
	option.JWTPrivateKey = "sqlite-compat-test"
	router := mux.NewRouter()
	router.Handle("/repo/{repoid}/jwt-token", appHandler(getJWTTokenCB)).Methods(http.MethodGet)
	requests := bufio.NewScanner(os.Stdin)
	for requests.Scan() {
		token := requests.Text()
		req := httptest.NewRequest(http.MethodGet, "/repo/"+syncFixtureRepo+"/jwt-token", nil)
		req.Header.Set("Seafile-Repo-Token", token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if _, cached := tokenCache.Load(token); cached {
			t.Fatal("SQLite sync endpoint cached a token that can be revoked by the core")
		}
		fmt.Println(response.Code)
	}
	if err := requests.Err(); err != nil {
		t.Fatal(err)
	}
}
