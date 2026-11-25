package main

import (
	"database/sql"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func openSQLiteGroupDB(file string) (*sql.DB, error) {
	path, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	params := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)"}}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: params.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	rows, err := db.Query("SELECT group_id, user_name FROM GroupUser LIMIT 0")
	if err != nil {
		db.Close()
		return nil, err
	}
	rows.Close()
	return db, nil
}
