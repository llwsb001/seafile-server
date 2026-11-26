//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	startTimeout = 8 * time.Second
	stopTimeout  = 6 * time.Second
)

type supervisor struct {
	config
	logger   *log.Logger
	core     *child
	http     *child
	webdav   *child
	retryDAV time.Time
}

type child struct {
	cmd     *exec.Cmd
	pidFile string
	done    chan struct{}
	err     error
}

func (s *supervisor) run(ctx context.Context, started func() error, reopen <-chan os.Signal, rotate func()) error {
	defer s.stopAll()
	if s.rpcReady() {
		return errors.New("Seafile RPC socket is already listening")
	}
	if err := s.recover(ctx); err != nil {
		return err
	}
	if err := started(); err != nil {
		return err
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-reopen:
			rotate()
		case <-tick.C:
			if err := s.recover(ctx); err != nil && ctx.Err() == nil {
				s.logger.Printf("recover services: %v", err)
			}
		}
	}
}

func (s *supervisor) recover(ctx context.Context) error {
	if !s.core.running() {
		// The HTTP and DAV workers hold connections to their core.
		s.stopAll()
		if err := s.startCore(ctx); err != nil {
			return err
		}
	}
	if s.useGo && !s.http.running() {
		s.stop(&s.http)
		args := append(s.serviceArgs(), "-P", s.pidPath("fileserver"), "-l", filepath.Join(s.logs, "fileserver.log"))
		next, err := s.startPackaged(ctx, "fileserver", s.pidPath("fileserver"), args, "", nil)
		if err != nil {
			return err
		}
		s.http = next
	}
	if s.dav.enabled && !s.webdav.running() && !time.Now().Before(s.retryDAV) {
		s.stop(&s.webdav)
		if err := s.startDAV(ctx); err != nil {
			s.logger.Printf("recover SeafDAV: %v", err)
			s.retryDAV = time.Now().Add(10 * time.Second)
		}
	}
	return nil
}

func (s *supervisor) startDAV(ctx context.Context) error {
	python, err := exec.LookPath("python3")
	if err != nil {
		name := os.Getenv("PYTHON")
		if name == "" {
			name = "python"
		}
		python, err = exec.LookPath(name)
		if err != nil {
			return fmt.Errorf("find SeafDAV Python executable: %w", err)
		}
	}
	proc, err := s.start(ctx, "seafdav", python, s.pidPath("seafdav"), s.davArgs(), "", nil)
	if err != nil {
		return err
	}
	s.webdav = proc
	return nil
}

func (s *supervisor) startCore(ctx context.Context) error {
	name := "seaf-server"
	logfile := filepath.Join(s.logs, "seafile.log")
	args := append(s.serviceArgs(), "-f", "-P", s.pidPath("seaf-server"))
	redirect := ""
	if s.useGo {
		name = "seaf-server-go"
		args = append(args, "--enable-management-rpcs", "--enable-share-writes")
		redirect = logfile
	} else {
		args = append(args, "-l", logfile)
	}
	next, err := s.startPackaged(ctx, name, s.pidPath("seaf-server"), args, redirect, s.rpcReady)
	if err != nil {
		return err
	}
	s.core = next
	return nil
}

func (s *supervisor) rpcReady() bool {
	conn, err := net.DialTimeout("unix", filepath.Join(s.rpc, "seafile.sock"), 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (s *supervisor) startPackaged(ctx context.Context, name, pidFile string, args []string, logPath string, ready func() bool) (*child, error) {
	path, err := s.binary(name)
	if err != nil {
		return nil, fmt.Errorf("%s did not start: %w", name, err)
	}
	return s.start(ctx, name, path, pidFile, args, logPath, ready)
}

func (s *supervisor) start(ctx context.Context, name, executable, pidFile string, args []string, logPath string, ready func() bool) (*child, error) {
	cmd := exec.Command(executable, args...)
	cmd.Env = s.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if !s.logToStdout {
		if logPath == "" {
			logPath = filepath.Join(s.logs, "controller.log")
		}
		file, err := openLog(logPath)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		cmd.Stdout, cmd.Stderr = file, file
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s did not start: %w", name, err)
	}
	proc := &child{cmd: cmd, pidFile: pidFile, done: make(chan struct{})}
	go proc.wait()
	s.logger.Printf("started %s, PID %d", name, cmd.Process.Pid)
	if err := proc.waitReady(ctx, ready); err != nil {
		s.stop(&proc)
		return nil, fmt.Errorf("%s did not start: %w", name, err)
	}
	return proc, nil
}

func (c *child) running() bool {
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *child) wait() {
	var status unix.Siginfo
	for {
		c.err = unix.Waitid(unix.P_PID, c.cmd.Process.Pid, &status, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(c.err, syscall.EINTR) {
			break
		}
	}
	close(c.done)
}

func (c *child) waitReady(ctx context.Context, ready func() bool) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			if c.err != nil {
				return c.err
			}
			return errors.New("process exited before readiness")
		case <-tick.C:
			if readPID(c.pidFile) == c.cmd.Process.Pid && (ready == nil || ready()) {
				return nil
			}
		}
	}
}

func (s *supervisor) stopAll() {
	s.stop(&s.webdav)
	s.stop(&s.http)
	s.stop(&s.core)
}

func (s *supervisor) stop(slot **child) {
	proc := *slot
	if proc == nil {
		return
	}
	*slot = nil
	pid := proc.cmd.Process.Pid
	defer func() {
		if err := removeOwnedPID(proc.pidFile, pid); err != nil {
			s.logger.Printf("clean up %s: %v", proc.pidFile, err)
		}
	}()
	s.logger.Printf("stopping %s, PID %d", filepath.Base(proc.cmd.Path), pid)
	// Each child runs in its own process group, including SeafDAV worker
	// subprocesses. Keep the exited leader unreaped until group signaling
	// finishes so its PID cannot be reused as another process group's ID.
	select {
	case <-proc.done:
		if proc.err != nil {
			s.logger.Printf("wait for PID %d: %v", pid, proc.err)
			_ = proc.cmd.Wait()
			return
		}
	default:
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		s.logger.Printf("stop PID %d: %v", pid, err)
	}
	timer := time.NewTimer(stopTimeout)
	defer timer.Stop()
	select {
	case <-proc.done:
		// The leader can exit while workers are still shutting down.
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			s.logger.Printf("clean up process group %d: %v", pid, err)
		}
	case <-timer.C:
		s.logger.Printf("force stop PID %d", pid)
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			s.logger.Printf("kill PID %d: %v", pid, err)
		}
		<-proc.done
	}
	_ = proc.cmd.Wait()
}

func readPID(path string) int {
	value, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(value)))
	return pid
}

func removeOwnedPID(path string, pid int) error {
	if path == "" || readPID(path) != pid {
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func openLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}

func lockController(dir string) (io.Closer, error) {
	file, err := os.OpenFile(filepath.Join(dir, ".seafile-controller.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("controller is already starting or running: %w", err)
	}
	return file, nil
}
