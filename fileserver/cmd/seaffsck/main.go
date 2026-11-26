// seaffsck is the Go seaf-fsck used for local SQLite maintenance.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/haiwen/seafile-server/fileserver/internal/maintenance"
)

type options struct {
	maintenance.CheckOptions
	dataDir, configDir, ccnetDir string
	force, help, version         bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parse(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		usage(stderr)
		return 255
	}
	if opts.help {
		usage(stderr)
		return 0
	}
	if opts.version {
		return 255
	}
	if opts.dataDir == "" {
		opts.dataDir = filepath.Join(opts.ccnetDir, "seafile-data")
	}
	opts.dataDir, err = expandHome(opts.dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	opts.configDir, err = expandHome(opts.configDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if runtime.GOOS == "linux" && !opts.force {
		info, err := os.Stat(opts.dataDir)
		if err != nil {
			fmt.Fprintf(stdout, "Failed to stat seafile data dir %s: %v\n", opts.dataDir, err)
			return 1
		}
		owner := info.Sys().(*syscall.Stat_t).Uid
		if euid := os.Geteuid(); uint32(euid) != owner {
			fmt.Fprintf(stdout, "Current user (%d) is not the user for running seafile server (%d). Unable to run fsck.\n",
				euid, owner)
			return 1
		}
	}

	store, err := maintenance.Open(ctx, opts.dataDir, opts.configDir)
	if err != nil {
		fmt.Fprintf(stdout, "Failed to create seafile session: %v\n", err)
		return 1
	}
	checkErr := store.Check(ctx, opts.CheckOptions, stdout)
	if err := errors.Join(checkErr, store.Close()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func expandHome(path string) (string, error) {
	suffix, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, suffix), nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: seaf-fsck [-F central-config-dir] [-d seafile_dir] [repo_id_1 [repo_id_2 ...]]
Additional options:
-f, --force: run fsck without user ownership check
-t, --threads: thread number for fsck
-S, --check-file-size: compare directory entry and file object sizes
-s, --shallow: check block existence without hashing block contents
-i, --id-prefix: run fsck for repos with this repo id prefix
Run against an offline SQLite installation or a stopped copy.
`)
}

func parse(args []string) (options, error) {
	result := options{ccnetDir: "~/.ccnet"}
	for len(args) != 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			result.RepoIDs = append(result.RepoIDs, args...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			result.RepoIDs = append(result.RepoIDs, arg)
			continue
		}
		names, value, hasValue := splitOption(arg)
		if len(names) == 0 {
			return result, fmt.Errorf("unrecognized option %q", arg)
		}
		for i, name := range names {
			requiresValue := strings.Contains("cdFit", name)
			if hasValue && !requiresValue {
				return result, fmt.Errorf("option %q does not take an argument", arg)
			}
			switch name {
			case "h":
				result.help = true
				return result, nil
			case "v":
				result.version = true
				return result, nil
			case "f":
				result.force = true
			case "s":
				result.Shallow = true
			case "S":
				result.CheckFileSize = true
			case "c", "d", "F", "i", "t":
				if !hasValue {
					switch {
					case i+1 < len(names):
						value = strings.Join(names[i+1:], "")
					case len(args) != 0:
						value, args = args[0], args[1:]
					default:
						return result, fmt.Errorf("option %q requires an argument", arg)
					}
				}
				setValue(&result, name, value)
			default:
				return result, fmt.Errorf("unrecognized option %q", arg)
			}
			if requiresValue {
				break
			}
		}
	}
	return result, nil
}

func splitOption(arg string) (names []string, value string, hasValue bool) {
	if !strings.HasPrefix(arg, "--") {
		return strings.Split(arg[1:], ""), "", false
	}
	long, value, hasValue := strings.Cut(arg[2:], "=")
	for _, option := range []struct{ long, short string }{
		{"help", "h"}, {"version", "v"}, {"force", "f"}, {"threads", "t"},
		{"config-file", "c"}, {"central-config-dir", "F"}, {"seafdir", "d"},
		{"shallow", "s"}, {"check-file-size", "S"}, {"id-prefix", "i"},
	} {
		if option.long == long {
			return []string{option.short}, value, hasValue
		}
	}
	return nil, "", false
}

func setValue(opts *options, name, value string) {
	switch name {
	case "c":
		opts.ccnetDir = value
	case "d":
		opts.dataDir = value
	case "F":
		opts.configDir = value
	case "i":
		opts.Prefix = &value
	case "t":
		opts.Threads = atoi(value)
	}
}

func atoi(value string) int {
	value = strings.TrimSpace(value)
	end := 0
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		end++
	}
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	n, _ := strconv.ParseInt(value[:end], 10, 32)
	return int(n)
}
