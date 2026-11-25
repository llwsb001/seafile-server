package seafserver

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/haiwen/seafile-server/fileserver/searpc"
)

const (
	zipProgressLifetime = 5 * time.Hour
	zipWorkerCount      = 5
)

type zipProgress struct {
	Zipped       int    `json:"zipped"`
	Total        int    `json:"total"`
	Failed       int    `json:"failed"`
	FailedReason string `json:"failed_reason"`
	Canceled     int    `json:"canceled"`
}

type preparedZIP struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type zipJob struct {
	progress zipProgress
	archive  preparedZIP
	expires  time.Time
	cancel   context.CancelFunc
	taken    bool
}

// ZipDownloads manages preparation and transfer of the transient browser archives.
type ZipDownloads struct {
	ctx       context.Context
	cancel    context.CancelFunc
	reader    *Reader
	config    *ServerConfig
	passwords *RepoPasswords
	dir       string
	workers   chan struct{}
	running   sync.WaitGroup

	mu   sync.Mutex
	jobs map[string]*zipJob
}

// NewZipDownloads gives this process a private archive directory in the shared data tree.
func NewZipDownloads(ctx context.Context, dataDir string, reader *Reader, config *ServerConfig, passwords *RepoPasswords) (*ZipDownloads, error) {
	temp, err := filepath.Abs(filepath.Join(dataDir, "httptemp"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(temp, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(temp, "go-zip-")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	manager := &ZipDownloads{
		ctx: ctx, cancel: cancel, reader: reader, config: config, passwords: passwords,
		dir: dir, workers: make(chan struct{}, zipWorkerCount), jobs: make(map[string]*zipJob),
	}
	manager.running.Add(1)
	go manager.sweep()
	return manager, nil
}

// Close stops preparation and removes the process's temporary files.
func (m *ZipDownloads) Close() {
	m.cancel()
	m.running.Wait()
	for token := range m.jobs {
		m.remove(token)
	}
	if err := os.Remove(m.dir); err != nil {
		log.Printf("remove ZIP temporary directory: %v", err)
	}
}

// RPCHandlers provides Seahub's progress and cancellation and the private HTTP handoff.
func (m *ZipDownloads) RPCHandlers() map[string]searpc.Handler {
	return map[string]searpc.Handler{
		"seafile_query_zip_progress":    m.query,
		"cancel_zip_task":               m.cancelTask,
		"seafile_fileserver_take_zip":   m.take,
		"seafile_fileserver_finish_zip": m.finish,
	}
}

func zipOperation(op string) bool {
	return op == "download-dir" || op == "download-dir-link" ||
		op == "download-multi" || op == "download-multi-link"
}

func (m *ZipDownloads) prepare(ctx context.Context, token string, access webAccess) *searpc.RPCError {
	selection, rpcErr := m.selection(ctx, access)
	if rpcErr != nil {
		return rpcErr
	}
	expires := time.Now().Add(zipProgressLifetime)
	ctx, cancel := context.WithDeadline(m.ctx, expires)
	job := &zipJob{progress: zipProgress{Total: 1}, expires: expires, cancel: cancel}
	m.mu.Lock()
	m.jobs[token] = job
	m.mu.Unlock()
	m.running.Add(1)
	go m.build(ctx, token, job, selection)
	return nil
}

func (m *ZipDownloads) build(ctx context.Context, token string, job *zipJob, selection zipSelection) {
	defer m.running.Done()
	select {
	case m.workers <- struct{}{}:
		defer func() { <-m.workers }()
	case <-ctx.Done():
		return
	}
	path, err := m.buildArchive(ctx, job, selection)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs[token] != job || ctx.Err() != nil {
		removeZIPFile(path)
		return
	}
	if err != nil {
		job.progress.Failed = 1
		job.progress.FailedReason = "internal error"
		if errors.Is(err, errZIPSize) {
			job.progress.FailedReason = "size too large"
		} else {
			log.Printf("prepare ZIP for repo %s: %v", selection.repo.ID, err)
		}
		return
	}
	job.archive = preparedZIP{Name: selection.filename, Path: path}
	job.progress.Total = job.progress.Zipped
}

func (m *ZipDownloads) query(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	token := stringArgument(args, 0)
	if token == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.lookup(*token)
	if job == nil || job.taken {
		return nil, nil
	}
	progress := job.progress
	if job.archive.Path == "" && progress.Zipped == progress.Total {
		// Do not report completion until the ZIP central directory has been written.
		progress.Total++
	}
	if progress.Failed != 0 || progress.Canceled != 0 {
		m.remove(*token)
	}
	body, err := json.Marshal(progress)
	if err != nil {
		return nil, rpcError(errWebAccessToken, "Internal error")
	}
	return string(body), nil
}

func (m *ZipDownloads) cancelTask(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	token := stringArgument(args, 0)
	if token != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if job := m.lookup(*token); job != nil && !job.taken {
			job.progress.Canceled = 1
			job.cancel()
			removeZIPFile(job.archive.Path)
		}
	}
	return 0, nil
}

func (m *ZipDownloads) take(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	token := stringArgument(args, 0)
	if token == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.lookup(*token)
	if job == nil || job.archive.Path == "" || job.taken || job.progress.Canceled != 0 {
		return nil, nil
	}
	job.taken = true
	archive := job.archive
	if archive.Name == "" {
		archive.Name = time.Now().Format("documents-export-2006-01-02.zip")
	}
	return archive, nil
}

func (m *ZipDownloads) finish(_ context.Context, args []json.RawMessage) (any, *searpc.RPCError) {
	token := stringArgument(args, 0)
	if token != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if job := m.jobs[*token]; job != nil && job.taken {
			m.remove(*token)
		}
	}
	return 0, nil
}

func (m *ZipDownloads) lookup(token string) *zipJob {
	job := m.jobs[token]
	if job != nil && !job.taken && !time.Now().Before(job.expires) {
		m.remove(token)
		return nil
	}
	return job
}

func (m *ZipDownloads) remove(token string) {
	job := m.jobs[token]
	delete(m.jobs, token)
	job.cancel()
	removeZIPFile(job.archive.Path)
}

func removeZIPFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove prepared ZIP: %v", err)
	}
}

func (m *ZipDownloads) sweep() {
	defer m.running.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			for token, job := range m.jobs {
				if !time.Now().Before(job.expires) {
					m.remove(token)
				}
			}
			m.mu.Unlock()
		}
	}
}
