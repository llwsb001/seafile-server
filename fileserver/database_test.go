package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haiwen/seafile-server/fileserver/option"
)

func TestSQLiteDatabaseBootstrap(t *testing.T) {
	root := t.TempDir()
	conf := filepath.Join(root, "seafile.conf")
	if err := os.WriteFile(conf, []byte("[database]\ntype = sqlite\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEAFILE_MYSQL_DB_HOST", "mysql.example.test")
	t.Setenv("CCNET_CONF_DIR", "")
	config, err := option.LoadDBOption(root)
	if err != nil || config.DBEngine != "sqlite" || config.Host != "" {
		t.Fatalf("load SQLite configuration: %+v, %v", config, err)
	}
	corePath := filepath.Join(root, "seafile.db")
	groupsDir := filepath.Join(root, "ccnet")
	groupsPath := filepath.Join(groupsDir, "GroupMgr", "groupmgr.db")
	ctx := context.Background()

	_, _, err = openSQLiteDatabases(ctx, root, groupsDir)
	if err == nil || !strings.Contains(err.Error(), "seafile.db") {
		t.Fatalf("missing core database error = %v", err)
	}
	if _, err := os.Stat(corePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup created a core database: %v", err)
	}
	initializeStartupDB(t, corePath, "seafile.sql")
	_, _, err = openSQLiteDatabases(ctx, root, groupsDir)
	if err == nil || !strings.Contains(err.Error(), "groupmgr.db") {
		t.Fatalf("missing group database error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(groupsPath), 0700); err != nil {
		t.Fatal(err)
	}
	initializeStartupDB(t, groupsPath, "groupmgr.sql")

	t.Setenv("CCNET_CONF_DIR", groupsDir)
	core, groups, err := openSQLiteDatabases(ctx, root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	defer groups.Close()
	if _, err := core.Exec("INSERT INTO Repo (repo_id) VALUES ('synthetic-repo')"); err != nil {
		t.Fatalf("core database should accept writes: %v", err)
	}
	if _, err := groups.Exec("INSERT INTO GroupUser (group_id, user_name) VALUES (1, 'member@example.test')"); err == nil {
		t.Fatal("fileserver opened its group database in write mode")
	}
}

func initializeStartupDB(t *testing.T, path, schemaName string) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join("..", "scripts", "sql", "sqlite", schemaName))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
}
