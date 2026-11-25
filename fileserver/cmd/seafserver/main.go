// seafserver is the read-only Go seaf-server used during the migration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/haiwen/seafile-server/fileserver/internal/seafserver"
	"github.com/haiwen/seafile-server/fileserver/searpc"
	"gopkg.in/ini.v1"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("seaf-server", flag.ContinueOnError)
	var dataDir, confDir, socketDir string
	var foreground bool
	flags.StringVar(&dataDir, "d", "", "seafile data directory")
	flags.StringVar(&dataDir, "seafdir", "", "seafile data directory")
	flags.StringVar(&confDir, "F", "", "central configuration directory")
	flags.StringVar(&confDir, "central-config-dir", "", "central configuration directory")
	flags.StringVar(&socketDir, "p", "", "Searpc socket directory")
	flags.StringVar(&socketDir, "rpc-pipe-path", "", "Searpc socket directory")
	flags.BoolVar(&foreground, "f", false, "run in the foreground")
	flags.BoolVar(&foreground, "foreground", false, "run in the foreground")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !foreground || dataDir == "" || flags.NArg() != 0 {
		return errors.New("usage: seaf-server -f -d data-dir [-F config-dir] [-p socket-dir]")
	}
	if confDir == "" {
		confDir = dataDir
	}
	if socketDir == "" {
		socketDir = dataDir
	}
	confPath := filepath.Join(confDir, "seafile.conf")
	config, err := ini.Load(confPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", confPath, err)
	}
	if !strings.EqualFold(config.Section("database").Key("type").String(), "sqlite") {
		return errors.New("the read-only server requires database type sqlite")
	}
	db, err := seafserver.OpenSQLiteReadOnly(ctx, filepath.Join(dataDir, "seafile.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	reader := seafserver.NewReader(db, dataDir)
	listener, err := net.Listen("unix", filepath.Join(socketDir, "seafile.sock"))
	if err != nil {
		return err
	}
	return searpc.Serve(ctx, listener, seafserver.RPCService, reader.RPCHandlers())
}
