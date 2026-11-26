// seafservgc is the Go seafserv-gc for offline SQLite installations.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/haiwen/seafile-server/fileserver/internal/maintenance"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("seafserv-gc", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: seafserv-gc [-F central-config-dir] [-d seafile_dir] [-C|-D] [repo_id ...]")
		fmt.Fprintln(stderr, "Stop Seafile and its fileserver before running garbage collection.")
		flags.PrintDefaults()
	}
	var dataDir, configDir, ccnetDir, prefix string
	var check, dryRun, version bool
	var opts maintenance.GCOptions
	for _, field := range []struct {
		short, long, initial, help string
		dest                       *string
	}{
		{"d", "seafdir", "", "Seafile data directory", &dataDir},
		{"F", "central-config-dir", "", "central config directory", &configDir},
		{"c", "config-file", "~/.ccnet", "ccnet data directory", &ccnetDir},
		{"i", "id-prefix", "", "filter dry-run stores by repository ID prefix", &prefix},
	} {
		flags.StringVar(field.dest, field.short, field.initial, field.help)
		flags.StringVar(field.dest, field.long, field.initial, field.help)
	}
	for _, field := range []struct {
		short, long, help string
		dest              *bool
	}{
		{"C", "check", "check data integrity", &check},
		{"D", "dry-run", "report unused blocks", &dryRun},
		{"V", "verbose", "list candidate block IDs and history cutoffs", &opts.Verbose},
		{"r", "rm-deleted", "process only permanently deleted libraries", &opts.DeletedOnly},
		{"R", "rm-fs", "collect unreachable FS objects along with blocks", &opts.RemoveFS},
		{"v", "version", "return version status", &version},
	} {
		flags.BoolVar(field.dest, field.short, false, field.help)
		flags.BoolVar(field.dest, field.long, false, field.help)
	}
	flags.IntVar(&opts.Threads, "t", 1, "thread count for dry run")
	flags.IntVar(&opts.Threads, "thread-num", 1, "thread count for dry run")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 255
	}
	if version {
		return 255
	}
	if dataDir == "" {
		dataDir = filepath.Join(ccnetDir, "seafile-data")
	}
	var err error
	if dataDir, err = expandHome(dataDir); err == nil {
		configDir, err = expandHome(configDir)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	flags.Visit(func(field *flag.Flag) {
		if field.Name == "i" || field.Name == "id-prefix" {
			opts.Prefix = &prefix
		}
	})
	opts.RepoIDs = flags.Args()
	if opts.Threads <= 0 {
		opts.Threads = 10
	}
	store, err := maintenance.Open(ctx, dataDir, configDir)
	if err != nil {
		fmt.Fprintf(stdout, "Failed to create seafile session: %v\n", err)
		return 1
	}
	code := 0
	if opts.DeletedOnly {
		err = store.CollectGCDeleted(ctx, dryRun, stdout)
	} else if check {
		code, err = store.VerifyGC(ctx, opts.RepoIDs, stdout)
	} else if dryRun {
		err = store.DryRunGC(ctx, opts, stdout)
	} else {
		err = store.CollectGCBlocks(ctx, opts, stdout)
	}
	if err := errors.Join(err, store.Close()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return code
}

func expandHome(path string) (string, error) {
	if suffix, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, suffix), nil
	}
	return path, nil
}
