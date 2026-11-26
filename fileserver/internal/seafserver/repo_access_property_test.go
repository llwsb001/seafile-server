package seafserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSQLiteRepoAccessProperty(t *testing.T) {
	ctx := context.Background()
	r, db, _ := virtualTestOrigin(t)
	read := r.RPCHandlers(nil)["seafile_repo_query_access_property"]
	write := SetRepoAccessPropertyRPC(db)
	id := strings.Repeat("x", 36) // Legacy C accepts a non-UUID key without a Repo row.
	args := []json.RawMessage{json.RawMessage(`"` + id + `"`), nil}
	if result, err := read(ctx, args[:1]); result != nil || err != nil {
		t.Fatalf("absent property: %v, %v", result, err)
	}
	for _, property := range []string{"public", "own", "private"} {
		args[1] = json.RawMessage(`"` + property + `"`)
		if result, err := write(ctx, args); result != 0 || err != nil {
			t.Fatalf("write %s: %v, %v", property, result, err)
		}
		if result, err := read(ctx, args[:1]); result != property || err != nil {
			t.Fatalf("read %s: %v, %v", property, result, err)
		}
	}
	args[1] = json.RawMessage(`"PUBLIC"`)
	if result, err := write(ctx, args); result != -1 || err == nil || err.Code != errBadArgs || err.Message != "Wrong access property" {
		t.Fatalf("invalid property: %v, %v", result, err)
	}
	if result, err := read(ctx, []json.RawMessage{json.RawMessage(`"bad"`)}); result != nil || err == nil || err.Code != errBadArgs ||
		err.Message != "Wrong repo id" {
		t.Fatalf("invalid property id: %v, %v", result, err)
	}
	if _, err := db.Exec("UPDATE WebAP SET access_property=NULL WHERE repo_id=?", id); err != nil {
		t.Fatal(err)
	}
	args[1] = json.RawMessage(`"own"`)
	if result, err := write(ctx, args); result != -1 || err == nil || err.Code != errGeneral || err.Message != "Internal server error" {
		t.Fatalf("write legacy null row: %v, %v", result, err)
	}
	if result, err := read(ctx, args[:1]); result != nil || err != nil {
		t.Fatalf("read legacy null row: %v, %v", result, err)
	}
}
