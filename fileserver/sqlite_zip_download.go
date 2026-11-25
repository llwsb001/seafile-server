package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/haiwen/seafile-server/fileserver/blockmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	log "github.com/sirupsen/logrus"
)

type fileServerZipSource struct{}

func (fileServerZipSource) Directory(repo *repomgr.Repo, id string) (*fsmgr.SeafDir, error) {
	return fsmgr.GetSeafdir(repo.StoreID, id)
}

func (fileServerZipSource) File(repo *repomgr.Repo, id string) (*fsmgr.Seafile, error) {
	return fsmgr.GetSeafile(repo.StoreID, id)
}

func (fileServerZipSource) ReadBlock(repo *repomgr.Repo, id string, dest io.Writer) error {
	return blockmgr.Read(repo.StoreID, id, dest)
}

func downloadPreparedZIP(rsp http.ResponseWriter, r *http.Request, token string, access *webaccessInfo) *appError {
	ctx, cancel := context.WithTimeout(r.Context(), option.DBOpTimeout)
	defer cancel()
	info, err := rpcclient.CallContext(ctx, "seafile_fileserver_take_zip", token)
	archive, _ := info.(map[string]any)
	if err != nil || archive == nil {
		return &appError{err, "Internal server error\n", http.StatusInternalServerError}
	}
	defer finishPreparedZIP(token)

	path, pathOK := archive["path"].(string)
	name, nameOK := archive["name"].(string)
	if !pathOK || !nameOK {
		return &appError{fmt.Errorf("invalid prepared ZIP response"), "Internal server error\n", http.StatusInternalServerError}
	}
	src, err := os.Open(path)
	if err != nil {
		return &appError{err, "Internal server error\n", http.StatusInternalServerError}
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return &appError{err, "Internal server error\n", http.StatusInternalServerError}
	}

	if _, found := r.Header["If-Modified-Since"]; found {
		rsp.WriteHeader(http.StatusNotModified)
		return nil
	}
	encodedName := strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
	rsp.Header().Set("Cache-Control", "max-age=3600")
	rsp.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	rsp.Header().Set("Content-Type", "application/zip")
	rsp.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	rsp.Header().Set("Content-Disposition", fmt.Sprintf("attachment;filename*=utf-8''%s;filename=\"%s\"", encodedName, sanitizeHeaderFilename(name)))
	rsp.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	size, err := io.Copy(rsp, src)
	if err != nil {
		return nil
	}
	op := "web-file-download"
	if access.op == "download-dir-link" || access.op == "download-multi-link" {
		op = "link-file-download"
	}
	sendStatisticMsg(access.repoID, access.user, op, uint64(size))
	return nil
}

func finishPreparedZIP(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), option.DBOpTimeout)
	defer cancel()
	if _, err := rpcclient.CallContext(ctx, "seafile_fileserver_finish_zip", token); err != nil {
		log.Warnf("finish prepared ZIP: %v", err)
	}
}
