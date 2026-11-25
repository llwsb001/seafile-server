// seafserver is the Go seaf-server used during the migration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	var dataDir, confDir, socketDir, ccnetDir string
	var foreground, enableShareWrites bool
	flags.StringVar(&dataDir, "d", "", "seafile data directory")
	flags.StringVar(&dataDir, "seafdir", "", "seafile data directory")
	flags.StringVar(&confDir, "F", "", "central configuration directory")
	flags.StringVar(&confDir, "central-config-dir", "", "central configuration directory")
	flags.StringVar(&ccnetDir, "c", "", "ccnet configuration directory")
	flags.StringVar(&ccnetDir, "config-file", "", "ccnet configuration directory")
	flags.StringVar(&socketDir, "p", "", "Searpc socket directory")
	flags.StringVar(&socketDir, "rpc-pipe-path", "", "Searpc socket directory")
	flags.BoolVar(&foreground, "f", false, "run in the foreground")
	flags.BoolVar(&foreground, "foreground", false, "run in the foreground")
	flags.BoolVar(&enableShareWrites, "enable-share-writes", false, "enable ordinary SQLite share writes")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !foreground || dataDir == "" || flags.NArg() != 0 {
		return errors.New("usage: seaf-server -f -d data-dir [-F config-dir] [-c ccnet-dir] [-p socket-dir] [--enable-share-writes]")
	}
	if enableShareWrites && ccnetDir == "" {
		return errors.New("--enable-share-writes requires -c")
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
		return errors.New("the Go server requires database type sqlite")
	}
	dbPath := filepath.Join(dataDir, "seafile.db")
	db, err := seafserver.OpenSQLiteReadOnly(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	reader := seafserver.NewReader(db, dataDir)
	// The C server reads keep_days as a signed decimal integer.
	keepDays, err := strconv.ParseInt(config.Section("history").Key("keep_days").String(), 10, 32)
	if err != nil {
		keepDays = -1
	}
	handlers := reader.RPCHandlers(int(keepDays))
	if ccnetDir != "" {
		identity, err := seafserver.OpenIdentityReadOnly(ctx, ccnetDir)
		if err != nil {
			return err
		}
		defer identity.Close()
		maps.Copy(handlers, identity.RPCHandlers())
		cloudMode := config.Section("general").Key("cloud_mode").String()
		maps.Copy(handlers, reader.PermissionRPCHandlers(identity, cloudMode == "true" || cloudMode == "1"))
	}
	if enableShareWrites {
		writer, err := seafserver.OpenShareWriter(dbPath)
		if err != nil {
			return err
		}
		defer writer.Close()
		maps.Copy(handlers, writer.RPCHandlers())
	}
	listener, err := net.Listen("unix", filepath.Join(socketDir, "seafile.sock"))
	if err != nil {
		return err
	}
	return searpc.Serve(ctx, listener, seafserver.RPCService, handlers)
}
