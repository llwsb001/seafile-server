package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/haiwen/seafile-server/fileserver/repomgr"
	log "github.com/sirupsen/logrus"
)

var uploadProgress sync.Map
var uploadCallbackName = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)

const uploadJSON = "application/json; charset=utf-8"

type countedUpload struct {
	io.ReadCloser
	length int64
	read   atomic.Int64
}

func (p *countedUpload) Read(buf []byte) (int, error) {
	n, err := p.ReadCloser.Read(buf)
	p.read.Add(int64(n))
	return n, err
}

func uploadHTTPHandler(next appHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !repomgr.UsesSQLite() {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodOptions && r.URL.Query().Has("X-Progress-ID") {
			if r.ContentLength < 0 {
				writeUploadError(w, http.StatusBadRequest, "No progress info")
				return
			}
			id := r.URL.Query().Get("X-Progress-ID")
			reader := &countedUpload{ReadCloser: r.Body, length: r.ContentLength}
			if _, exists := uploadProgress.LoadOrStore(id, reader); exists {
				writeUploadError(w, http.StatusBadRequest, "Duplicate progress id.\n")
				return
			}
			defer uploadProgress.Delete(id)
			r.Body = reader
		}
		if err := next(w, r); err != nil {
			if err.Error != nil && err.Code == http.StatusInternalServerError {
				log.Errorf("upload request failed: %v", err.Error)
			}
			w.Header().Set("Content-Type", uploadJSON)
			w.WriteHeader(err.Code)
			io.WriteString(w, err.Message)
		}
	})
}

func writeUploadError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", uploadJSON)
	w.WriteHeader(status)
	fmt.Fprintf(w, "{\"error\": \"%s\"}", message)
}

func uploadProgressCB(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !query.Has("X-Progress-ID") {
		writeUploadError(w, http.StatusBadRequest, "Progress id not found")
		return
	}
	callback := query.Get("callback")
	if !query.Has("callback") {
		writeUploadError(w, http.StatusBadRequest, "Callback not found")
		return
	}
	if !uploadCallbackName.MatchString(callback) {
		writeUploadError(w, http.StatusBadRequest, "Invalid callback")
		return
	}
	entry, ok := uploadProgress.Load(query.Get("X-Progress-ID"))
	if !ok {
		writeUploadError(w, http.StatusBadRequest, "No progress found.\n")
		return
	}
	progress := entry.(*countedUpload)
	w.Header().Set("Content-Type", uploadJSON)
	fmt.Fprintf(w, `%s({"uploaded": %d, "length": %d});`, callback, progress.read.Load(), progress.length)
}

func indexProgressCB(w http.ResponseWriter, r *http.Request) {
	// The C server's upload handlers do not start asynchronous indexing.
	if !r.URL.Query().Has("task_id") {
		writeUploadError(w, http.StatusBadRequest, "task id not found")
		return
	}
	writeUploadError(w, http.StatusNotFound, "Failed to get index progress")
}
