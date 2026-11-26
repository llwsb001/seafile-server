//go:build linux

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/ini.v1"
)

type options struct {
	central, ccnet, data, logs, pid string
	foreground, test, version       bool
}

func parseOptions(args []string, diagnostics io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("seafile-controller", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	for _, option := range []struct {
		target      *string
		short, long string
		value, help string
	}{
		{&opts.central, "F", "central-config-dir", "", "central configuration directory"},
		{&opts.ccnet, "c", "config-dir", "~/.ccnet", "ccnet configuration directory"},
		{&opts.data, "d", "seafile-dir", "", "Seafile data directory"},
		{&opts.logs, "l", "logdir", "", "log directory"},
		{&opts.pid, "P", "pidfile", "", "controller PID file"},
	} {
		flags.StringVar(option.target, option.short, option.value, option.help)
		flags.StringVar(option.target, option.long, option.value, option.help)
	}
	var ccnetDebug, seafileDebug string
	flags.StringVar(&ccnetDebug, "g", "info", "ccnet logging level")
	flags.StringVar(&ccnetDebug, "ccnet-debug-level", "info", "ccnet logging level")
	flags.StringVar(&seafileDebug, "G", "debug", "Seafile logging level")
	flags.StringVar(&seafileDebug, "seafile-debug-level", "debug", "Seafile logging level")
	for _, option := range []struct {
		target      *bool
		short, long string
		help        string
	}{
		{&opts.foreground, "f", "foreground", "remain in the foreground"},
		{&opts.test, "t", "test", "validate the server configuration"},
		{&opts.version, "v", "version", "print the controller version"},
	} {
		flags.BoolVar(option.target, option.short, false, option.help)
		flags.BoolVar(option.target, option.long, false, option.help)
	}
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if !opts.version && (opts.central == "" || opts.data == "") {
		return opts, errors.New("--central-config-dir and --seafile-dir are required")
	}
	return opts, nil
}

type config struct {
	options
	executable, bin, install, pids, rpc string
	useGo, logToStdout                  bool
	dav                                 davConfig
	env                                 []string
}

type davConfig struct {
	enabled, debug bool
	host           string
	port           int
}

func loadConfig(opts options, executable string) (config, error) {
	for _, path := range []*string{&opts.central, &opts.ccnet, &opts.data, &opts.logs, &opts.pid} {
		if *path == "" {
			continue
		}
		absolute, err := expandPath(*path)
		if err != nil {
			return config{}, err
		}
		*path = absolute
	}
	for _, path := range []string{opts.ccnet, opts.data, opts.central} {
		info, err := os.Stat(path)
		if err != nil {
			return config{}, err
		}
		if !info.IsDir() {
			return config{}, fmt.Errorf("%s is not a directory", path)
		}
	}
	conf, err := ini.Load(filepath.Join(opts.central, "seafile.conf"))
	if err != nil {
		return config{}, fmt.Errorf("load seafile.conf: %w", err)
	}
	if !strings.EqualFold(conf.Section("database").Key("type").String(), "sqlite") {
		return config{}, errors.New("Go controller requires database type sqlite")
	}
	if opts.logs == "" {
		opts.logs = filepath.Join(filepath.Dir(opts.ccnet), "logs")
	}
	if opts.pid == "" {
		if pid := os.Getenv("SEAFILE_PIDFILE"); pid != "" {
			opts.pid, err = expandPath(pid)
			if err != nil {
				return config{}, err
			}
		}
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return config{}, err
	}
	bin := filepath.Dir(executable)
	install := filepath.Dir(filepath.Dir(bin))
	top := filepath.Dir(install)
	davPath := filepath.Join(opts.central, "seafdav.conf")
	dav, err := loadDAV(davPath)
	if err != nil {
		return config{}, err
	}
	c := config{
		options: opts, executable: executable, bin: bin, install: install,
		pids: filepath.Join(top, "pids"), rpc: filepath.Join(install, "runtime"),
		useGo:       conf.Section("fileserver").Key("use_go_fileserver").MustBool(false),
		logToStdout: os.Getenv("SEAFILE_LOG_TO_STDOUT") == "true", dav: dav,
	}
	c.env = c.childEnv(top, davPath)
	return c, nil
}

func expandPath(path string) (string, error) {
	if suffix, found := strings.CutPrefix(path, "~/"); found {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, suffix)
	}
	return filepath.Abs(path)
}

func loadDAV(path string) (davConfig, error) {
	conf, err := ini.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return davConfig{}, nil
	}
	if err != nil {
		return davConfig{}, fmt.Errorf("load seafdav.conf: %w", err)
	}
	settings := conf.Section("WEBDAV")
	if !settings.Key("enabled").MustBool(false) {
		return davConfig{}, nil
	}
	dav := davConfig{
		enabled: true, debug: settings.Key("debug").MustBool(false),
		host: settings.Key("host").MustString("0.0.0.0"), port: settings.Key("port").MustInt(8080),
	}
	if dav.port < 1 || dav.port > 65535 {
		return davConfig{}, fmt.Errorf("invalid SeafDAV port %d", dav.port)
	}
	return dav, nil
}

func (c config) childEnv(top, davPath string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "ENABLE_GO_FILESERVER" && key != daemonChildEnv {
			env = append(env, entry)
		}
	}
	dirs := []string{
		os.Getenv("PYTHONPATH"),
		filepath.Join(top, "conf"),
		filepath.Join(c.install, "seahub"),
		filepath.Join(c.install, "seahub", "thirdpart"),
		filepath.Join(c.install, "seahub", "seahub-extra"),
		filepath.Join(c.install, "seahub", "seahub-extra", "thirdparts"),
		filepath.Join(c.install, "seafile", "lib", "python3", "site-packages"),
		filepath.Join(c.install, "seafile", "lib64", "python3", "site-packages"),
	}
	if dirs[0] == "" {
		dirs = dirs[1:]
	}
	env = append(env,
		"CCNET_CONF_DIR="+c.ccnet, "SEAFILE_CONF_DIR="+c.data,
		"SEAFILE_DATA_DIR="+c.data,
		"SEAFILE_CENTRAL_CONF_DIR="+c.central, "SEAFILE_RPC_PIPE_PATH="+c.rpc,
		"SEAHUB_DIR="+filepath.Join(c.install, "seahub"), "SEAFDAV_CONF="+davPath,
		"PYTHONPATH="+strings.Join(dirs, string(os.PathListSeparator)),
	)
	if c.useGo {
		env = append(env, "ENABLE_GO_FILESERVER=true")
	}
	return env
}

func (c config) binary(name string) (string, error) {
	// Management scripts put the package bin directory on PATH. Prefer the
	// companion executable and require it when the Go package is selected.
	path, err := exec.LookPath(filepath.Join(c.bin, name))
	if err == nil || c.useGo {
		return path, err
	}
	return exec.LookPath(name)
}

func (c config) serviceArgs() []string {
	return []string{"-F", c.central, "-c", c.ccnet, "-d", c.data, "-p", c.rpc}
}

func (c config) pidPath(service string) string {
	return filepath.Join(c.pids, service+".pid")
}

func (c config) davArgs() []string {
	args := []string{"-m", "wsgidav.server.server_cli", "--server", "gunicorn", "--root", "/",
		"--log-file", filepath.Join(c.logs, "seafdav.log"), "--pid", c.pidPath("seafdav"),
		"--port", strconv.Itoa(c.dav.port), "--host", c.dav.host,
	}
	if c.dav.debug {
		args = append(args, "-v")
	}
	return args
}
