package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/option"
)

func openDatabases(ctx context.Context, config *option.DBOption) (*sql.DB, *sql.DB, error) {
	if config.DBEngine == "sqlite" {
		return openSQLiteDatabases(ctx, absDataDir, ccnetDir)
	}
	if config.UseTLS && !config.SkipVerify {
		if err := registerCA(config.CaPath); err != nil {
			return nil, nil, err
		}
	}
	ccnet, err := openMySQLDatabase(config, config.CcnetDbName)
	if err != nil {
		return nil, nil, fmt.Errorf("open ccnet database: %w", err)
	}
	core, err := openMySQLDatabase(config, config.SeafileDbName)
	if err != nil {
		ccnet.Close()
		return nil, nil, fmt.Errorf("open seafile database: %w", err)
	}
	return core, ccnet, nil
}

func openSQLiteDatabases(ctx context.Context, dataDir, groupsDir string) (*sql.DB, *sql.DB, error) {
	if groupsDir == "" {
		groupsDir = os.Getenv("CCNET_CONF_DIR")
	}
	if groupsDir == "" {
		return nil, nil, fmt.Errorf("SQLite fileserver requires -c or CCNET_CONF_DIR")
	}

	corePath := filepath.Join(dataDir, "seafile.db")
	core, err := seafserver.OpenSQLiteReadWrite(corePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open SQLite database %q: %w", corePath, err)
	}
	// Fileserver shares this pool across readers and writers. Some existing
	// operations acquire another connection while they iterate a result set.
	core.SetMaxOpenConns(8)
	if err := verifySQLiteDatabase(ctx, core, corePath,
		"SELECT Repo.repo_id, Branch.commit_id FROM Repo JOIN Branch USING(repo_id) LIMIT 0"); err != nil {
		core.Close()
		return nil, nil, err
	}

	groupsPath := filepath.Join(groupsDir, "GroupMgr", "groupmgr.db")
	groups, err := seafserver.OpenSQLiteReadOnly(ctx, groupsPath)
	if err != nil {
		core.Close()
		return nil, nil, err
	}
	if err := verifySQLiteDatabase(ctx, groups, groupsPath,
		`SELECT g.group_id, u.user_name FROM "Group" g JOIN GroupUser u USING(group_id) LIMIT 0`); err != nil {
		groups.Close()
		core.Close()
		return nil, nil, err
	}
	return core, groups, nil
}

func verifySQLiteDatabase(ctx context.Context, db *sql.DB, path, query string) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("verify SQLite database %q: %w", path, err)
	}
	return rows.Close()
}

func openMySQLDatabase(config *option.DBOption, name string) (*sql.DB, error) {
	tlsMode := fmt.Sprintf("%t", config.UseTLS)
	if config.UseTLS {
		tlsMode = "custom"
		if config.SkipVerify {
			tlsMode = "skip-verify"
		}
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?tls=%s&readTimeout=60s&writeTimeout=60s",
		config.User, config.Password, config.Host, config.Port, name, tlsMode)
	if config.Charset != "" {
		dsn += "&charset=" + config.Charset
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	return db, nil
}

func registerCA(path string) error {
	pem, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read database CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return fmt.Errorf("failed to append database CA PEM")
	}
	return mysql.RegisterTLSConfig("custom", &tls.Config{RootCAs: roots})
}
