package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/ini.v1"
)

func clearInactiveRPCSocket(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode().Type() != os.ModeSocket || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("RPC path %q already exists", path)
	}
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err == nil {
		conn.Close()
		return fmt.Errorf("RPC socket %q is already listening", path)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("check RPC socket %q: %w", path, err)
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) {
		return fmt.Errorf("RPC socket %q changed while checking it", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func webTokenSettings(data []byte, enableEnv string) (bool, int64, error) {
	// GLib treats inline text and quotes as part of these configuration values.
	config, err := ini.LoadSources(ini.LoadOptions{
		IgnoreInlineComment: true, PreserveSurroundedQuote: true,
	}, data)
	if err != nil {
		return false, 0, err
	}
	section := config.Section("fileserver")
	mode := section.Key("use_go_fileserver").Value()
	enabled := enableEnv == "true" || mode == "true" || mode == "1"
	// GKeyFile accepts text after the decimal integer when separated by whitespace.
	const whitespace = " \t\r\n\v\f"
	ttlValue := strings.TrimLeft(section.Key("web_token_expire_time").Value(), whitespace)
	if space := strings.IndexAny(ttlValue, whitespace); space >= 0 {
		ttlValue = ttlValue[:space]
	}
	ttl, err := strconv.ParseInt(ttlValue, 10, 32)
	if err != nil || ttl <= 0 {
		ttl = 3600
	}
	return enabled, ttl, nil
}

func listenRPC(path string, private bool) (net.Listener, error) {
	if !private {
		return net.Listen("unix", path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	syscall.ForkLock.RLock()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()

	// Bind and verify the resulting filesystem mode before accepting connections.
	mask := syscall.Umask(0o077)
	err = unix.Bind(fd, &unix.SockaddrUnix{Name: path})
	syscall.Umask(mask)
	if err != nil {
		return nil, fmt.Errorf("bind RPC socket %q: %w", path, err)
	}
	listener, err := net.FileListener(file)
	if err != nil {
		unix.Unlink(path)
		return nil, fmt.Errorf("open RPC listener %q: %w", path, err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(true)
	info, err := os.Lstat(path)
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("verify RPC socket %q: %w", path, err)
	}
	if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o700 {
		listener.Close()
		return nil, fmt.Errorf("RPC socket %q must have mode 0700", path)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != uint32(os.Geteuid()) {
		listener.Close()
		return nil, fmt.Errorf("RPC socket %q must belong to the server user", path)
	}
	if err := unix.Listen(fd, unix.SOMAXCONN); err != nil {
		listener.Close()
		return nil, fmt.Errorf("listen on RPC socket %q: %w", path, err)
	}
	return listener, nil
}
