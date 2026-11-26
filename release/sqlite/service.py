#!/usr/bin/env python3
"""Linux SQLite release entry points for a versioned Seafile installation."""

import argparse
from configparser import ConfigParser, Error as ConfigError
from contextlib import closing
from dataclasses import dataclass
import fcntl
from http.client import HTTPConnection, HTTPException
import json
import os
from pathlib import Path
import select
import signal
import socket
import subprocess
import sys
import time


CONTROLLER_PID = "seafile-controller.pid"
CORE_PID = "seaf-server.pid"
WRITER_PIDS = (CORE_PID, "fileserver.pid", "seafdav.pid", "seahub.pid")
START_TIMEOUT = 20
STOP_TIMEOUT = 30


@dataclass(frozen=True)
class Release:
    version: Path
    implementation: str

    @property
    def instance(self) -> Path:
        return self.version.parent

    @property
    def pids(self) -> Path:
        return self.instance / "pids"

    @property
    def rpc(self) -> Path:
        return self.version / "runtime/seafile.sock"

    @property
    def launcher(self) -> Path:
        return self.binary("seafile-controller" if self.implementation == "go" else "seaf-server")

    @property
    def launcher_pid(self) -> Path:
        return self.pids / (CONTROLLER_PID if self.implementation == "go" else CORE_PID)

    def binary(self, name: str) -> Path:
        return self.version / "seafile/bin" / name

    def launch_args(self, *, check: bool = False) -> list[str]:
        args = [
            str(self.launcher), "-F", str(self.instance / "conf"),
            "-c", str(self.instance / "ccnet"), "-d", str(self.instance / "seafile-data"),
        ]
        if check:
            return [*args, "-t"]
        if self.implementation == "go":
            args.extend(("-l", str(self.instance / "logs")))
        else:
            args.extend(("-l", str(self.instance / "logs/seafile.log"), "-p", str(self.rpc.parent)))
        return [*args, "-P", str(self.launcher_pid), "-f"]

    def environment(self) -> dict[str, str]:
        path = os.pathsep.join((str(self.launcher.parent), os.environ.get("PATH", os.defpath)))
        env = {**os.environ, "PATH": path}
        if self.implementation == "c":
            env["ENABLE_GO_FILESERVER"] = "false"
        return env


def pid_from(path: Path) -> int | None:
    try:
        pid = int(path.read_text())
    except FileNotFoundError:
        return None
    if pid <= 1:
        raise ValueError(f"invalid PID in {path}")
    return pid


def process_binary(pid: int) -> Path | None:
    try:
        return Path(os.readlink(f"/proc/{pid}/exe"))
    except FileNotFoundError:
        return None


def active_process(pidfile: Path) -> tuple[int, Path] | None:
    pid = pid_from(pidfile)
    if pid is None:
        return None
    binary = process_binary(pid)
    if binary is None:
        return None
    return pid, binary


def rpc_listening(path: Path) -> bool:
    with closing(socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)) as client:
        client.settimeout(0.5)
        try:
            client.connect(str(path))
        except (FileNotFoundError, ConnectionRefusedError):
            return False
    return True


def assert_stopped(release: Release, *, include_seahub: bool = True) -> None:
    pidfiles = (CONTROLLER_PID, *WRITER_PIDS)
    if not include_seahub:
        pidfiles = pidfiles[:-1]
    for name in pidfiles:
        if process := active_process(release.pids / name):
            raise RuntimeError(f"{name} identifies a live process {process[0]} ({process[1]})")
    if rpc_listening(release.rpc):
        raise RuntimeError(f"RPC socket is still active at {release.rpc}")


def check_config(release: Release) -> tuple[str, int]:
    config = ConfigParser(interpolation=None, inline_comment_prefixes=("#", ";"))
    path = release.instance / "conf/seafile.conf"
    with path.open(encoding="utf-8") as source:
        config.read_file(source)
    if config.get("database", "type", fallback="").lower() != "sqlite":
        raise ValueError(f"{path} must select SQLite")
    use_go = config.getboolean("fileserver", "use_go_fileserver", fallback=False)
    if use_go != (release.implementation == "go"):
        raise ValueError(f"{path} does not select the requested {release.implementation} implementation")
    host = config.get("httpserver", "host", fallback="127.0.0.1")
    host = config.get("fileserver", "host", fallback=host)
    if host in ("", "0.0.0.0"):
        host = "127.0.0.1"
    elif host == "::":
        host = "::1"
    port = config.getint("httpserver", "port", fallback=8082)
    port = config.getint("fileserver", "port", fallback=port)
    if not 0 < port <= 65535:
        raise ValueError(f"invalid fileserver port {port}")
    subprocess.run(
        release.launch_args(check=True), cwd=release.version,
        env=release.environment(), check=True, timeout=START_TIMEOUT,
    )
    return host, port


def http_ready(host: str, port: int) -> bool:
    with closing(HTTPConnection(host, port, timeout=0.5)) as client:
        try:
            client.request("GET", "/protocol-version")
            response = client.getresponse()
            return response.status == 200 and json.loads(response.read()) == {"version": 2}
        except (ConnectionError, TimeoutError, HTTPException, ValueError):
            return False


def start(release: Release) -> None:
    assert_stopped(release)
    host, port = check_config(release)
    try:
        with socket.create_connection((host, port), timeout=0.5):
            raise RuntimeError(f"fileserver port {host}:{port} is already in use")
    except ConnectionRefusedError:
        pass

    # Foreground mode keeps the child PID stable for readiness and shutdown.
    log = release.instance / "logs/sqlite-launch.log"
    log.parent.mkdir(exist_ok=True)
    release.rpc.parent.mkdir(exist_ok=True)
    with log.open("ab") as output:
        proc = subprocess.Popen(
            release.launch_args(), cwd=release.version, env=release.environment(),
            stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT, start_new_session=True,
        )
    ready = False
    try:
        deadline = time.monotonic() + START_TIMEOUT
        while time.monotonic() < deadline and proc.poll() is None:
            launcher = active_process(release.launcher_pid)
            core = active_process(release.pids / CORE_PID)
            fileserver = active_process(release.pids / "fileserver.pid")
            launcher_ok = launcher == (proc.pid, release.launcher.resolve())
            core_name = "seaf-server-go" if release.implementation == "go" else "seaf-server"
            core_ok = core is not None and core[1] == release.binary(core_name).resolve()
            http_owner_ok = (
                fileserver is not None and fileserver[1] == release.binary("fileserver").resolve()
                if release.implementation == "go" else fileserver is None
            )
            if launcher_ok and core_ok and http_owner_ok and rpc_listening(release.rpc) and http_ready(host, port):
                ready = True
                print(f"{release.implementation} {release.launcher.name} {proc.pid} is ready")
                return
            time.sleep(0.05)
        raise RuntimeError(f"{release.launcher.name} did not become ready, see {log}")
    finally:
        if not ready and proc.poll() is None:
            proc.terminate()
            proc.wait(timeout=STOP_TIMEOUT)


def stop(release: Release) -> None:
    pid = pid_from(release.launcher_pid)
    if pid is not None:
        try:
            handle = os.pidfd_open(pid)
        except ProcessLookupError:
            handle = None
        if handle is not None:
            try:
                binary = process_binary(pid)
                wanted = release.launcher.resolve()
                if binary is not None:
                    if binary != wanted:
                        raise RuntimeError(f"PID {pid} belongs to {binary}, expected {wanted}")
                    signal.pidfd_send_signal(handle, signal.SIGTERM)
                    poller = select.poll()
                    poller.register(handle, select.POLLIN)
                    if not poller.poll(STOP_TIMEOUT * 1000):
                        raise RuntimeError(f"{release.launcher.name} {pid} did not stop")
            finally:
                os.close(handle)
    assert_stopped(release, include_seahub=False)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version-dir", required=True, type=Path)
    parser.add_argument("--implementation", choices=("c", "go"), default="go")
    parser.add_argument("action", choices=("check", "start", "stop", "restart", "fsck", "gc"))
    parser.add_argument("args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.args and args.action not in ("fsck", "gc"):
        parser.error("trailing arguments require fsck or gc")

    try:
        release = Release(args.version_dir.resolve(strict=True), args.implementation)
        if args.action == "check":
            check_config(release)
            return 0
        release.pids.mkdir(exist_ok=True)
        with (release.pids / ".sqlite-release-operation.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            if args.action == "restart":
                check_config(release)
            if args.action in ("stop", "restart"):
                stop(release)
            if args.action in ("start", "restart"):
                start(release)
            if args.action in ("fsck", "gc"):
                assert_stopped(release)
                name = "seaf-fsck" if args.action == "fsck" else "seafserv-gc"
                extra = args.args[1:] if args.args[:1] == ["--"] else args.args
                path_options = {"--central-config-dir", "--seafdir", "--config-file", "--config-dir"}
                if any(arg.startswith(("-F", "-d", "-c")) or arg.partition("=")[0] in path_options for arg in extra):
                    raise ValueError("maintenance paths are fixed by --version-dir")
                result = subprocess.run(
                    [str(release.binary(name)), "-F", str(release.instance / "conf"),
                     "-d", str(release.instance / "seafile-data"), *extra],
                    cwd=release.version, env=release.environment(),
                )
                return result.returncode
        return 0
    except (ConfigError, OSError, RuntimeError, ValueError, subprocess.SubprocessError) as error:
        print(f"SQLite release {args.action}: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
