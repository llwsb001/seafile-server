// Main package for Seafile file server.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/haiwen/seafile-server/fileserver/blockmgr"
	"github.com/haiwen/seafile-server/fileserver/commitmgr"
	"github.com/haiwen/seafile-server/fileserver/fsmgr"
	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/metrics"
	"github.com/haiwen/seafile-server/fileserver/option"
	"github.com/haiwen/seafile-server/fileserver/repomgr"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"github.com/haiwen/seafile-server/fileserver/share"
	"github.com/haiwen/seafile-server/fileserver/utils"
	log "github.com/sirupsen/logrus"

	"net/http/pprof"
)

var dataDir, absDataDir string
var centralDir string
var ccnetDir string
var logFile, absLogFile string
var rpcPipePath string
var pidFilePath string
var logFp *os.File

var seafileDB, ccnetDB *sql.DB

var logToStdout bool

func init() {
	flag.StringVar(&centralDir, "F", "", "central config directory")
	flag.StringVar(&dataDir, "d", "", "seafile data directory")
	flag.StringVar(&ccnetDir, "c", "", "ccnet config directory for SQLite")
	flag.StringVar(&logFile, "l", "", "log file path")
	flag.StringVar(&rpcPipePath, "p", "", "rpc pipe path")
	flag.StringVar(&pidFilePath, "P", "", "pid file path")

	env := os.Getenv("SEAFILE_LOG_TO_STDOUT")
	if env == "true" {
		logToStdout = true
	}

	log.SetFormatter(&LogFormatter{})
}

const (
	timestampFormat = "[2006-01-02 15:04:05] "
)

type LogFormatter struct{}

func (f *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	levelStr := entry.Level.String()
	if levelStr == "fatal" {
		levelStr = "ERROR"
	} else {
		levelStr = strings.ToUpper(levelStr)
	}
	level := fmt.Sprintf("[%s] ", levelStr)
	appName := ""
	if logToStdout {
		appName = "[fileserver] "
	}
	buf := make([]byte, 0, len(appName)+len(timestampFormat)+len(level)+len(entry.Message)+1)
	if logToStdout {
		buf = append(buf, appName...)
	}
	buf = entry.Time.AppendFormat(buf, timestampFormat)
	buf = append(buf, level...)
	buf = append(buf, entry.Message...)
	buf = append(buf, '\n')
	return buf, nil
}

func main() {
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runFileServer(ctx); err != nil {
		log.Fatal(err)
	}
}

func runFileServer(ctx context.Context) error {
	if centralDir == "" {
		return errors.New("central config directory must be specified")
	}
	if _, err := os.Stat(centralDir); err != nil {
		return fmt.Errorf("open central config directory: %w", err)
	}

	if dataDir == "" {
		return errors.New("seafile data directory must be specified")
	}
	if _, err := os.Stat(dataDir); err != nil {
		return fmt.Errorf("open seafile data directory: %w", err)
	}
	var err error
	absDataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve seafile data directory: %w", err)
	}

	if logToStdout {
		// Use default output (StdOut)
	} else if logFile == "" {
		absLogFile = filepath.Join(absDataDir, "fileserver.log")
		fp, err := os.OpenFile(absLogFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("open fileserver log: %w", err)
		}
		logFp = fp
		log.SetOutput(fp)
	} else if logFile != "-" {
		absLogFile, err = filepath.Abs(logFile)
		if err != nil {
			return fmt.Errorf("resolve fileserver log path: %w", err)
		}
		fp, err := os.OpenFile(absLogFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("open fileserver log: %w", err)
		}
		logFp = fp
		log.SetOutput(fp)
	}

	if absLogFile != "" && !logToStdout {
		utils.Dup(int(logFp.Fd()), int(os.Stderr.Fd()))
	}
	// When logFile is "-", use default output (StdOut)

	if err := option.LoadSeahubConfig(); err != nil {
		return fmt.Errorf("read seahub config: %w", err)
	}
	option.LoadFileServerOptions(centralDir)
	dbOptions, err := option.LoadDBOption(centralDir)
	if err != nil {
		return fmt.Errorf("load database configuration: %w", err)
	}

	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	seafileDB, ccnetDB, err = openDatabases(startup, dbOptions)
	if err != nil {
		return err
	}
	defer seafileDB.Close()
	defer ccnetDB.Close()
	if err := rpcClientInit(startup, dbOptions.DBEngine == "sqlite"); err != nil {
		return err
	}
	cancel()

	level, err := log.ParseLevel(option.LogLevel)
	if err != nil {
		log.Info("use the default log level: info")
		log.SetLevel(log.InfoLevel)
	} else {
		log.SetLevel(level)
	}

	repomgr.Init(seafileDB)

	fsmgr.Init(centralDir, dataDir, option.FsCacheLimit)

	blockmgr.Init(centralDir, dataDir)

	commitmgr.Init(centralDir, dataDir)

	groupTable := option.GroupTableName
	if dbOptions.DBEngine == "sqlite" {
		groupTable = "Group"
	}
	share.Init(ccnetDB, seafileDB, groupTable, option.CloudMode)

	fileopInit()

	syncAPIInit()

	sizeSchedulerInit()

	stopVirtualRepo := virtualRepoInit()
	defer stopVirtualRepo()

	initUpload()

	metrics.Init()
	defer metrics.Stop()

	server := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", option.Host, option.Port),
		Handler: newHTTPRouter(),
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen for fileserver HTTP: %w", err)
	}
	defer listener.Close()

	if pidFilePath != "" {
		if err := os.WriteFile(pidFilePath, []byte(fmt.Sprint(os.Getpid())), 0664); err != nil {
			return fmt.Errorf("write fileserver PID: %w", err)
		}
		defer func() {
			if err := os.Remove(pidFilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Warnf("Remove fileserver PID: %v", err)
			}
		}()
	}

	go handleUser1Signal(ctx)
	log.Print("Seafile file server started.")

	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			<-result
			return fmt.Errorf("shut down fileserver HTTP: %w", err)
		}
		err := <-result
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func handleUser1Signal(ctx context.Context) {
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGUSR1)
	defer signal.Stop(signalChan)

	for {
		select {
		case <-ctx.Done():
			return
		case <-signalChan:
			logRotate()
		}
	}
}

func logRotate() {
	if logToStdout {
		return
	}
	// reopen fileserver log
	fp, err := os.OpenFile(absLogFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		log.Fatalf("Failed to reopen fileserver log: %v", err)
	}
	log.SetOutput(fp)
	if logFp != nil {
		logFp.Close()
		logFp = fp
	}

	utils.Dup(int(logFp.Fd()), int(os.Stderr.Fd()))
}

var rpcclient *searpc.Client

func rpcClientInit(ctx context.Context, check bool) error {
	var pipePath string
	if rpcPipePath != "" {
		pipePath = filepath.Join(rpcPipePath, "seafile.sock")
	} else {
		pipePath = filepath.Join(absDataDir, "seafile.sock")
	}
	rpcclient = searpc.Init(pipePath, seafserver.RPCService, 10)
	if check {
		// Look up a repository without creating data. Both cores return a
		// successful null value when this repository does not exist.
		const probe = "00000000-0000-0000-0000-000000000000"
		if _, err := rpcclient.CallContext(ctx, "seafile_get_repo", probe); err != nil {
			return fmt.Errorf("contact Searpc at %q: %w", pipePath, err)
		}
	}
	return nil
}

func newHTTPRouter() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/protocol-version{slash:\\/?}", handleProtocolVersion)
	r.Handle("/files/{.*}/{.*}", appHandler(accessCB))
	r.Handle("/blks/{.*}/{.*}", appHandler(accessBlksCB))
	r.Handle("/zip/{.*}", appHandler(accessZipCB))
	r.Handle("/upload-api/{.*}", uploadHTTPHandler(uploadAPICB))
	r.Handle("/upload-aj/{.*}", uploadHTTPHandler(uploadAjaxCB))
	r.Handle("/update-api/{.*}", uploadHTTPHandler(updateAPICB))
	r.Handle("/update-aj/{.*}", uploadHTTPHandler(updateAjaxCB))
	r.Handle("/upload-blks-api/{.*}", uploadHTTPHandler(uploadBlksAPICB))
	r.Handle("/upload-raw-blks-api/{.*}", uploadHTTPHandler(uploadRawBlksAPICB))
	if repomgr.UsesSQLite() {
		r.Handle("/update-blks-api/{.*}", uploadHTTPHandler(updateBlksAPICB))
		r.HandleFunc("/upload_progress", uploadProgressCB)
		r.HandleFunc("/idx_progress", indexProgressCB)
	}

	// links api
	//r.Handle("/u/{.*}", appHandler(uploadLinkCB))
	r.Handle("/f/{.*}{slash:\\/?}", appHandler(accessLinkCB))
	//r.Handle("/d/{.*}", appHandler(accessDirLinkCB))

	r.Handle("/repos/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/files/{filepath:(?s).*}", appHandler(accessV2CB))

	// file syncing api
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/permission-check{slash:\\/?}",
		appHandler(permissionCheckCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/commit/HEAD{slash:\\/?}",
		appHandler(headCommitOperCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/commit/{id:[\\da-z]{40}}",
		appHandler(commitOperCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/block/{id:[\\da-z]{40}}",
		appHandler(blockOperCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/fs-id-list{slash:\\/?}",
		appHandler(getFsObjIDCB))
	r.Handle("/repo/head-commits-multi{slash:\\/?}",
		appHandler(headCommitsMultiCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/pack-fs{slash:\\/?}",
		appHandler(packFSCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/check-fs{slash:\\/?}",
		appHandler(checkFSCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/check-blocks{slash:\\/?}",
		appHandler(checkBlockCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/recv-fs{slash:\\/?}",
		appHandler(recvFSCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/quota-check{slash:\\/?}",
		appHandler(getCheckQuotaCB))
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/jwt-token{slash:\\/?}",
		appHandler(getJWTTokenCB))

	// seadrive api
	r.Handle("/repo/{repoid:[\\da-z]{8}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{4}-[\\da-z]{12}}/block-map/{id:[\\da-z]{40}}",
		appHandler(getBlockMapCB))
	r.Handle("/accessible-repos{slash:\\/?}", appHandler(getAccessibleRepoListCB))

	// pprof
	r.Handle("/debug/pprof", &profileHandler{http.HandlerFunc(pprof.Index)})
	r.Handle("/debug/pprof/cmdline", &profileHandler{http.HandlerFunc(pprof.Cmdline)})
	r.Handle("/debug/pprof/profile", &profileHandler{http.HandlerFunc(pprof.Profile)})
	r.Handle("/debug/pprof/symbol", &profileHandler{http.HandlerFunc(pprof.Symbol)})
	r.Handle("/debug/pprof/heap", &profileHandler{pprof.Handler("heap")})
	r.Handle("/debug/pprof/block", &profileHandler{pprof.Handler("block")})
	r.Handle("/debug/pprof/goroutine", &profileHandler{pprof.Handler("goroutine")})
	r.Handle("/debug/pprof/threadcreate", &profileHandler{pprof.Handler("threadcreate")})
	r.Handle("/debug/pprof/trace", &traceHandler{})

	if option.HasRedisOptions {
		r.Use(metrics.MetricMiddleware)
	}
	return r
}

func handleProtocolVersion(rsp http.ResponseWriter, r *http.Request) {
	io.WriteString(rsp, "{\"version\": 2}")
}

type appError struct {
	Error   error
	Message string
	Code    int
}

type appHandler func(http.ResponseWriter, *http.Request) *appError

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if e := fn(w, r); e != nil {
		if e.Error == errSQLiteSyncAuth {
			w.WriteHeader(e.Code)
			return
		}
		if e.Error != nil && e.Code == http.StatusInternalServerError {
			log.Errorf("path %s internal server error: %v\n", r.URL.Path, e.Error)
		}
		http.Error(w, e.Message, e.Code)
	}
}

func RecoverWrapper(f func()) {
	defer func() {
		if err := recover(); err != nil {
			log.Errorf("panic: %v\n%s", err, debug.Stack())
		}
	}()

	f()
}

type profileHandler struct {
	pHandler http.Handler
}

func (p *profileHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	queries := r.URL.Query()
	password := queries.Get("password")
	if !option.EnableProfiling || password != option.ProfilePassword {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}

	p.pHandler.ServeHTTP(w, r)
}

type traceHandler struct {
}

func (p *traceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	queries := r.URL.Query()
	password := queries.Get("password")
	if !option.EnableProfiling || password != option.ProfilePassword {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}

	pprof.Trace(w, r)
}
