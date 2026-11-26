// seafilecontroller supervises the Linux SQLite Seafile services.
//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

const daemonChildEnv = "SEAFILE_CONTROLLER_DAEMON_CHILD"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseOptions(args, os.Stderr)
	if err != nil {
		return err
	}
	if opts.version {
		fmt.Fprintln(os.Stderr, "seafile-controller version 1.0")
		return flag.ErrHelp
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	c, err := loadConfig(opts, executable)
	if err != nil {
		return err
	}
	if c.test {
		return testServerConfig(c)
	}
	for _, dir := range []string{c.logs, c.pids, c.rpc} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if !c.foreground && !c.logToStdout && os.Getenv(daemonChildEnv) != "1" {
		return startBackground(c, args)
	}
	return supervise(c)
}

func testServerConfig(c config) error {
	args := []string{"-F", c.central, "-c", c.ccnet, "-d", c.data, "-t"}
	if !c.useGo {
		return runConfigCheck(c, "seaf-server", append(args, "-f"))
	}
	if err := runConfigCheck(c, "seaf-server-go", args); err != nil {
		return err
	}
	return runConfigCheck(c, "fileserver", args)
}

func runConfigCheck(c config, name string, args []string) error {
	path, err := c.binary(name)
	if err != nil {
		return fmt.Errorf("check %s configuration: %w", name, err)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = c.env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("check %s configuration: %w", name, err)
	}
	return nil
}

func startBackground(c config, args []string) error {
	notify, childNotify, err := os.Pipe()
	if err != nil {
		return err
	}
	defer notify.Close()
	defer childNotify.Close()
	cmd := exec.Command(c.executable, args...)
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{childNotify}
	diagnostics, err := openLog(filepath.Join(c.logs, "controller.log"))
	if err != nil {
		return err
	}
	defer diagnostics.Close()
	cmd.Stdout, cmd.Stderr = diagnostics, diagnostics
	if err := cmd.Start(); err != nil {
		return err
	}
	childNotify.Close()
	var ready [1]byte
	if _, err := io.ReadFull(notify, ready[:]); err != nil {
		return fmt.Errorf("background controller did not start: %w", errors.Join(err, cmd.Wait()))
	}
	return cmd.Process.Release()
}

func supervise(c config) error {
	lock, err := lockController(c.pids)
	if err != nil {
		return err
	}
	defer lock.Close()

	logPath := filepath.Join(c.logs, "controller.log")
	logger := log.New(os.Stdout, "seafile-controller ", log.LstdFlags)
	var logFile *os.File
	if !c.logToStdout {
		logFile, err = openLog(logPath)
		if err != nil {
			return err
		}
		logger.SetOutput(logFile)
		defer func() { logFile.Close() }()
	}
	rotate := func() {
		if logFile == nil {
			return
		}
		file, err := openLog(logPath)
		if err != nil {
			logger.Printf("reopen controller log: %v", err)
			return
		}
		logger.SetOutput(file)
		logFile.Close()
		logFile = file
	}

	var notify *os.File
	if os.Getenv(daemonChildEnv) == "1" {
		notify = os.NewFile(3, "background controller readiness")
		defer notify.Close()
	}
	started := func() error {
		if c.pid != "" {
			if err := os.WriteFile(c.pid, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
				return fmt.Errorf("write controller PID: %w", err)
			}
		}
		if notify != nil {
			_, err := notify.Write([]byte{1})
			notify.Close()
			return err
		}
		return nil
	}
	defer func() {
		if err := removeOwnedPID(c.pid, os.Getpid()); err != nil {
			logger.Printf("clean up controller PID: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reopen := make(chan os.Signal, 1)
	signal.Notify(reopen, syscall.SIGUSR1)
	defer signal.Stop(reopen)
	s := supervisor{config: c, logger: logger}
	return s.run(ctx, started, reopen, rotate)
}
