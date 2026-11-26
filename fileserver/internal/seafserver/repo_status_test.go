package seafserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
)

func TestSetSQLiteRepoStatus(t *testing.T) {
	ctx := context.Background()
	r, db, _ := virtualTestOrigin(t)
	create := r.RepoCreationRPCHandlers(db)["create_virtual_repo"]
	value, rpcErr := create(ctx, virtualTestArgs("/team"))
	child, ok := value.(string)
	if !ok || rpcErr != nil {
		t.Fatalf("create child: %v, %v", value, rpcErr)
	}
	const absent = "99999999-9999-4999-8999-999999999999"
	set := SetRepoStatusRPC(db)
	call := func(id, status string) (any, bool) {
		t.Helper()
		args := []json.RawMessage{json.RawMessage(`"` + id + `"`), json.RawMessage(status)}
		out, rpcErr := set(ctx, args)
		if rpcErr != nil && (rpcErr.Code != errBadArgs || rpcErr.Message != "Arguments error") {
			t.Fatalf("set status %s for %s: %v", status, id, rpcErr)
		}
		return out, rpcErr != nil
	}
	checkStatus := func(id string, want int) {
		t.Helper()
		var stored int
		if err := db.QueryRow("SELECT status FROM RepoInfo WHERE repo_id=?", id).Scan(&stored); err != nil || stored != want {
			t.Fatalf("stored status for %s = %d, %v; want %d", id, stored, err, want)
		}
	}
	for _, value := range []string{"-1", "2"} {
		if result, failed := call(testVirtualOrigin, value); result != -1 || !failed {
			t.Fatalf("invalid status %s: %v, %v", value, result, failed)
		}
	}
	if result, failed := call("invalid", "1"); result != -1 || !failed {
		t.Fatalf("invalid repo: %v, %v", result, failed)
	}
	if result, failed := call(absent, "1"); result != 0 || failed {
		t.Fatalf("absent repo: %v, %v", result, failed)
	}
	if result, failed := call(testVirtualOrigin, "1"); result != 0 || failed {
		t.Fatalf("make read-only: %v, %v", result, failed)
	}
	for _, id := range []string{testVirtualOrigin, child} {
		checkStatus(id, 1)
		if status, err := repomgr.LoadRepoStatus(ctx, db, id); err != nil || status != 1 {
			t.Fatalf("effective read-only status for %s = %d, %v", id, status, err)
		}
	}
	if result, failed := call(child, "0"); result != 0 || failed {
		t.Fatalf("update virtual repo: %v, %v", result, failed)
	}
	checkStatus(testVirtualOrigin, 1)
	checkStatus(child, 0)
	if status, err := repomgr.LoadRepoStatus(ctx, db, child); err != nil || status != 1 {
		t.Fatalf("effective status for child = %d, %v", status, err)
	}
	if result, failed := call(testVirtualOrigin, "null"); result != 0 || failed {
		t.Fatalf("restore normal with marshaled null: %v, %v", result, failed)
	}
	checkStatus(testVirtualOrigin, 0)
	checkStatus(child, 0)
	if _, err := db.Exec("DELETE FROM RepoInfo WHERE repo_id=?", testVirtualOrigin); err != nil {
		t.Fatal(err)
	}
	if result, failed := call(testVirtualOrigin, "1"); result != 0 || failed {
		t.Fatalf("update with legacy missing metadata: %v, %v", result, failed)
	}
	var stored int
	if err := db.QueryRow("SELECT status FROM RepoInfo WHERE repo_id=?", testVirtualOrigin).Scan(&stored); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing metadata was not preserved: %d, %v", stored, err)
	}
	checkStatus(child, 1)
}
