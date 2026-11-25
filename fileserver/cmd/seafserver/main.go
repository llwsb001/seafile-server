// seafserver is the Go seaf-server used during the migration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
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
	var dataDir, confDir, socketDir, ccnetDir string
	var foreground, enableShareWrites, enableManagementRPCs bool
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
	flags.BoolVar(&enableManagementRPCs, "enable-management-rpcs", false, "enable management RPCs on a private socket")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !foreground || dataDir == "" || flags.NArg() != 0 {
		return errors.New("usage: seaf-server -f -d data-dir [-F config-dir] [-c ccnet-dir] [-p socket-dir] [--enable-share-writes] [--enable-management-rpcs]")
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
	confData, err := os.ReadFile(confPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", confPath, err)
	}
	config, err := ini.Load(confData)
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
	serverConfig := seafserver.NewServerConfig(db, confData)
	handlers := reader.RPCHandlers(serverConfig)
	maps.Copy(handlers, seafserver.EventRPCHandlers())
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
	if enableManagementRPCs {
		writer, err := seafserver.OpenSQLiteReadWrite(dbPath)
		if err != nil {
			return err
		}
		defer writer.Close()
		maps.Copy(handlers, serverConfig.RPCHandlers(writer))
		maps.Copy(handlers, reader.SyncTokenRPCHandlers(writer))
	}
	tokenMode, tokenTTL, err := webTokenSettings(confData, os.Getenv("ENABLE_GO_FILESERVER"))
	if err != nil {
		return fmt.Errorf("load Web access token settings from %s: %w", confPath, err)
	}
	listener, err := listenRPC(filepath.Join(socketDir, "seafile.sock"), tokenMode || enableManagementRPCs)
	if err != nil {
		return err
	}
	if tokenMode {
		tokens := seafserver.NewWebAccessTokens(tokenTTL)
		maps.Copy(handlers, tokens.RPCHandlers())
		stopCleanup := tokens.StartCleanup(ctx)
		defer stopCleanup()
	}
	return searpc.Serve(ctx, listener, seafserver.RPCService, handlers)
}
