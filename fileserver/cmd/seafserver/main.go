// seafserver is the Go seaf-server used during the migration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"math"
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
	var dataDir, confDir, socketDir, ccnetDir, pidFile string
	var foreground, testConfig, enableShareWrites, enableManagementRPCs bool
	flags.StringVar(&dataDir, "d", "", "seafile data directory")
	flags.StringVar(&dataDir, "seafdir", "", "seafile data directory")
	flags.StringVar(&confDir, "F", "", "central configuration directory")
	flags.StringVar(&confDir, "central-config-dir", "", "central configuration directory")
	flags.StringVar(&ccnetDir, "c", "", "ccnet configuration directory")
	flags.StringVar(&ccnetDir, "config-file", "", "ccnet configuration directory")
	flags.StringVar(&socketDir, "p", "", "Searpc socket directory")
	flags.StringVar(&socketDir, "rpc-pipe-path", "", "Searpc socket directory")
	flags.StringVar(&pidFile, "P", "", "PID file path")
	flags.BoolVar(&foreground, "f", false, "run in the foreground")
	flags.BoolVar(&foreground, "foreground", false, "run in the foreground")
	flags.BoolVar(&testConfig, "t", false, "validate the SQLite configuration without starting the server")
	flags.BoolVar(&testConfig, "test", false, "validate the SQLite configuration without starting the server")
	flags.BoolVar(&enableShareWrites, "enable-share-writes", false, "enable ordinary SQLite share writes")
	flags.BoolVar(&enableManagementRPCs, "enable-management-rpcs", false, "enable management RPCs on a private socket")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (!foreground && !testConfig) || dataDir == "" || flags.NArg() != 0 {
		return errors.New("usage: seaf-server [-f|-t] -d data-dir [-F config-dir] [-c ccnet-dir] [-p socket-dir] [--enable-share-writes] [--enable-management-rpcs]")
	}
	if enableShareWrites && ccnetDir == "" {
		return errors.New("--enable-share-writes requires -c")
	}
	if err := seafserver.CheckNotificationConfig(); err != nil {
		return err
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

	var identity *seafserver.Identity
	if ccnetDir != "" {
		identity, err = seafserver.OpenIdentityReadOnly(ctx, ccnetDir)
		if err != nil {
			return err
		}
		defer identity.Close()
	}
	tokenMode, tokenTTL, err := webTokenSettings(confData, os.Getenv("ENABLE_GO_FILESERVER"))
	if err != nil {
		return fmt.Errorf("load Web access token settings from %s: %w", confPath, err)
	}
	if testConfig {
		return nil
	}

	reader := seafserver.NewReader(db, dataDir)
	serverConfig := seafserver.NewServerConfig(db, confData)
	handlers := reader.RPCHandlers(serverConfig)
	maps.Copy(handlers, reader.EventRPCHandlers())
	quota := seafserver.NewUserQuota(db, serverConfig)
	maps.Copy(handlers, quota.RPCHandlers())
	cloudModeValue := config.Section("general").Key("cloud_mode").String()
	cloudMode := cloudModeValue == "true" || cloudModeValue == "1"
	if identity != nil {
		maps.Copy(handlers, identity.RPCHandlers())
		handlers["seafile_get_user_share_usage"] = quota.ShareUsageRPC(identity)
		maps.Copy(handlers, reader.PermissionRPCHandlers(identity, cloudMode))
		if enableManagementRPCs {
			organizations, err := seafserver.OpenOrganizations(ccnetDir, identity)
			if err != nil {
				return err
			}
			defer organizations.Close()
			maps.Copy(handlers, organizations.RPCHandlers())
		}
	}
	if enableShareWrites {
		writer, err := seafserver.OpenShareWriter(dbPath)
		if err != nil {
			return err
		}
		defer writer.Close()
		maps.Copy(handlers, writer.RPCHandlers())
	}
	var passwords *seafserver.RepoPasswords
	if enableManagementRPCs {
		writer, err := seafserver.OpenSQLiteReadWrite(dbPath)
		if err != nil {
			return err
		}
		defer writer.Close()
		maps.Copy(handlers, serverConfig.RPCHandlers(writer))
		maps.Copy(handlers, reader.AdminRepoReadRPCHandlers())
		maps.Copy(handlers, quota.ManagementRPCHandlers(writer))
		maps.Copy(handlers, reader.SyncTokenRPCHandlers(writer))
		maps.Copy(handlers, reader.HistoryRPCHandlers(writer))
		maps.Copy(handlers, reader.RepoCreationRPCHandlers(writer))
		maps.Copy(handlers, reader.RepoGovernanceRPCHandlers(writer))
		maps.Copy(handlers, reader.RepoTrashWriteRPCHandlers(writer, cloudMode))
		handlers["seafile_set_repo_owner"] = seafserver.SetRepoOwnerRPC(writer, cloudMode)
		stopTrashExpiry := reader.StartRepoTrashExpiry(ctx, writer, serverConfig)
		defer stopTrashExpiry()
		maps.Copy(handlers, reader.DirectoryShareRPCHandlers(writer))
		handlers["seafile_get_upload_tmp_file_offset"] = seafserver.UploadOffsetRPC(writer)
		handlers["set_repo_status"] = seafserver.SetRepoStatusRPC(writer)
		handlers["set_base_commit"] = seafserver.SetVirtualBaseCommitRPC(writer)

		passwords = seafserver.NewRepoPasswords(reader)
		maps.Copy(handlers, passwords.RPCHandlers())
		maps.Copy(handlers, passwords.MutationRPCHandlers(writer))
		stopPasswords := passwords.StartCleanup(ctx)
		defer stopPasswords()
		handlers["seafile_repo_set_access_property"] = seafserver.SetRepoAccessPropertyRPC(writer)

		if identity != nil {
			copyConfig := config.Section("web_copy")
			maxMiB := copyConfig.Key("max_size").MustInt64(0)
			limits := seafserver.FileMoveLimits{
				MaxFiles: copyConfig.Key("max_files").MustInt64(0),
				MaxBytes: min(max(0, maxMiB), math.MaxInt64>>20) << 20,
			}
			fileHandlers, closeMoves := reader.FileWriteRPCHandlers(ctx, writer, identity, cloudMode, quota, passwords, limits)
			defer closeMoves()
			maps.Copy(handlers, fileHandlers)
			groupManagement, err := seafserver.OpenGroupManagement(ccnetDir, writer)
			if err != nil {
				return err
			}
			defer groupManagement.Close()
			maps.Copy(handlers, groupManagement.RPCHandlers())
			accounts, err := seafserver.OpenAccounts(ccnetDir, dbPath)
			if err != nil {
				return err
			}
			defer accounts.Close()
			maps.Copy(handlers, accounts.RPCHandlers())
		}
	}
	socketPath := filepath.Join(socketDir, "seafile.sock")
	if pidFile != "" {
		if err := clearInactiveRPCSocket(ctx, socketPath); err != nil {
			return err
		}
	}
	listener, err := listenRPC(socketPath, tokenMode || enableManagementRPCs)
	if err != nil {
		return err
	}
	defer listener.Close()
	if tokenMode {
		zips, err := seafserver.NewZipDownloads(ctx, dataDir, reader, serverConfig, passwords)
		if err != nil {
			return fmt.Errorf("initialize ZIP downloads: %w", err)
		}
		defer zips.Close()
		maps.Copy(handlers, zips.RPCHandlers())
		tokens := zips.WebAccessTokens(tokenTTL)
		maps.Copy(handlers, tokens.RPCHandlers())
		stopCleanup := tokens.StartCleanup(ctx)
		defer stopCleanup()
	}
	if pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0644); err != nil {
			listener.Close()
			return fmt.Errorf("write core PID file: %w", err)
		}
		defer os.Remove(pidFile)
	}
	return searpc.Serve(ctx, listener, seafserver.RPCService, handlers)
}
