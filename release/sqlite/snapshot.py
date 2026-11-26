#!/usr/bin/env python3
"""Save or restore the data of a stopped Linux SQLite installation."""

import argparse
from contextlib import closing, contextmanager
import errno
import fcntl
from hashlib import sha256
from io import BytesIO
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import socket
import sqlite3
import stat
import tarfile
from tempfile import TemporaryDirectory


DIRECTORIES = ("conf", "ccnet", "seafile-data", "seahub-data")
DATABASES = (
    "ccnet/GroupMgr/groupmgr.db",
    "ccnet/OrgMgr/orgmgr.db",
    "ccnet/PeerMgr/usermgr.db",
    "ccnet/misc/config.db",
    "seafile-data/seafile.db",
    "seahub.db",
)
SIDECARS = ("-wal", "-shm", "-journal")
PID_LOCK = ".sqlite-release-operation.lock"
MANIFEST = ".seafile-sqlite-snapshot.json"


class HashingReader:
    def __init__(self, stream):
        self.stream = stream
        self.digest = sha256()

    def read(self, size):
        data = self.stream.read(size)
        self.digest.update(data)
        return data


def digest(path):
    checksum = sha256()
    with path.open("rb") as stream:
        for data in iter(lambda: stream.read(1024 * 1024), b""):
            checksum.update(data)
    return checksum.hexdigest()


def check_pid(path):
    if not stat.S_ISREG(path.lstat().st_mode):
        raise ValueError(f"不是常规 PID 文件：{path}")
    pid = int(path.read_text().strip())
    if pid <= 0:
        raise ValueError(f"PID 文件无效：{path}")
    try:
        os.readlink(f"/proc/{pid}/exe")
    except FileNotFoundError:
        return
    except PermissionError:
        pass
    raise ValueError(f"实例仍有运行进程：PID {pid}（{path.name}）")


def check_socket(path):
    if not stat.S_ISSOCK(path.lstat().st_mode):
        raise ValueError(f"RPC 路径类型异常：{path}")
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.settimeout(1)
        try:
            connection.connect(str(path))
        except OSError as error:
            if error.errno in {errno.ECONNREFUSED, errno.ENOENT}:
                return
            raise
    raise ValueError(f"实例 RPC 仍在监听：{path}")


@contextmanager
def stopped_instance(instance):
    pids = instance / "pids"
    pids.mkdir(mode=0o700, exist_ok=True)
    if not stat.S_ISDIR(pids.lstat().st_mode):
        raise ValueError(f"PID 目录不是常规目录：{pids}")
    fd = os.open(pids / PID_LOCK, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "r+") as lock:
        if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode):
            raise ValueError("实例启停锁须为常规文件")
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError("实例启停或另一快照操作正在进行") from error
        for pid_file in pids.glob("*.pid"):
            check_pid(pid_file)
        for socket_path in instance.glob("*/runtime/seafile.sock"):
            check_socket(socket_path)
        yield


def check_layout(root):
    for name in (*DIRECTORIES, "seafile-data/storage"):
        if not stat.S_ISDIR((root / name).lstat().st_mode):
            raise ValueError(f"实例目录不是常规目录：{name}")
    for name in ("conf/seafile.conf", *DATABASES):
        if not stat.S_ISREG((root / name).lstat().st_mode):
            raise ValueError(f"实例数据不是常规文件：{name}")


def check_databases(root):
    # SQLite may replay journals or checkpoint WAL when it opens a database.
    # Perform the check on a second set of files so the archived bytes stay intact.
    with TemporaryDirectory(prefix="sqlite-integrity-") as directory:
        temp = Path(directory)
        for index, name in enumerate(DATABASES):
            source = root / name
            target = temp / f"db-{index}"
            shutil.copyfile(source, target)
            target.chmod(0o600)
            for suffix in ("-wal", "-journal"):
                sidecar = source.with_name(source.name + suffix)
                if sidecar.exists():
                    shutil.copyfile(sidecar, target.with_name(target.name + suffix))
            try:
                with closing(sqlite3.connect(target)) as db:
                    healthy = db.execute("PRAGMA integrity_check").fetchall() == [("ok",)]
            except sqlite3.Error as error:
                raise ValueError(f"无法打开归档内的 SQLite 数据库：{name}") from error
            if not healthy:
                raise ValueError(f"SQLite 完整性检查失败：{name}")


def source_paths(instance):
    def fail_walk(error):
        raise error

    for root in DIRECTORIES:
        start = instance / root
        yield start
        for base, directories, files in os.walk(start, onerror=fail_walk):
            directories.sort()
            for name in directories + sorted(files):
                yield Path(base) / name
    for name in (DATABASES[-1], *(DATABASES[-1] + suffix for suffix in SIDECARS)):
        path = instance / name
        if path.exists() or path.is_symlink():
            yield path


def write_archive(instance, archive):
    files = {}
    with tarfile.open(archive, "w:gz", format=tarfile.PAX_FORMAT) as tar:
        for path in source_paths(instance):
            relative = path.relative_to(instance).as_posix()
            mode = path.lstat().st_mode
            item = tar.gettarinfo(str(path), arcname=relative)
            if stat.S_ISDIR(mode):
                tar.addfile(item)
                files[relative] = {"mode": mode & 0o777, "kind": "directory"}
                continue
            if not stat.S_ISREG(mode):
                raise ValueError(f"快照不支持符号链接、设备和其他非常规资源：{relative}")
            # tarfile can infer hardlinks by inode. Store each path as independent data.
            item.type, item.linkname, item.size = tarfile.REGTYPE, "", path.stat().st_size
            with path.open("rb") as stream:
                source = HashingReader(stream)
                tar.addfile(item, source)
            files[relative] = {"mode": mode & 0o777, "kind": "file", "size": item.size,
                               "sha256": source.digest.hexdigest()}
        document = json.dumps({"schema": 1, "entries": files}, sort_keys=True).encode("utf-8")
        item = tarfile.TarInfo(MANIFEST)
        item.mode, item.size = 0o600, len(document)
        tar.addfile(item, BytesIO(document))


def validate_name(name):
    path = PurePosixPath(name)
    if (not path.parts or path.is_absolute() or ".." in path.parts or path.as_posix() != name
            or (path.parts[0] not in DIRECTORIES
                and name not in (DATABASES[-1], *(DATABASES[-1] + suffix for suffix in SIDECARS)))):
        raise ValueError(f"归档路径不属于 SQLite 实例：{name}")
    return path


def extract_archive(archive, output):
    directories = []
    with tarfile.open(archive, "r:gz") as tar:
        items = tar.getmembers()
        paths = [item.name for item in items]
        if len(paths) != len(set(paths)) or paths.count(MANIFEST) != 1:
            raise ValueError("快照缺少清单，或含重复条目")
        manifest_item = tar.getmember(MANIFEST)
        if not manifest_item.isfile():
            raise ValueError("快照清单须为常规文件")
        with tar.extractfile(manifest_item) as input_file:
            manifest = json.load(input_file)
        if (not isinstance(manifest, dict) or manifest.get("schema") != 1
                or not isinstance(manifest.get("entries"), dict)
                or set(manifest["entries"]) != set(paths) - {MANIFEST}):
            raise ValueError("快照清单与归档路径不一致")

        for item in items:
            if item.name == MANIFEST:
                continue
            relative = validate_name(item.name)
            destination = output / relative
            mode = item.mode & 0o777
            if item.isdir():
                if manifest["entries"][item.name] != {"mode": mode, "kind": "directory"}:
                    raise ValueError(f"归档目录权限不匹配：{relative}")
                destination.mkdir(mode=0o700)
                directories.append((destination, mode))
                continue
            if not item.isfile():
                raise ValueError(f"归档成员须为常规文件：{relative}")
            with tar.extractfile(item) as stream, destination.open("xb") as output_file:
                reader = HashingReader(stream)
                shutil.copyfileobj(reader, output_file)
            expected = {"mode": mode, "kind": "file", "size": item.size, "sha256": reader.digest.hexdigest()}
            if manifest["entries"][item.name] != expected:
                raise ValueError(f"归档文件摘要或属性不匹配：{relative}")
            destination.chmod(mode)
    for directory, mode in reversed(directories):
        directory.chmod(mode)
    check_layout(output)
    check_databases(output)


def archive_instance(instance, archive):
    if archive.is_relative_to(instance):
        raise ValueError("快照归档须位于实例目录外")
    with stopped_instance(instance), TemporaryDirectory(prefix="sqlite-snapshot-", dir=archive.parent) as directory:
        check_layout(instance)
        staged = Path(directory) / "snapshot.tar.gz"
        write_archive(instance, staged)
        staged.chmod(0o600)
        # Verify exactly the files read while the instance is stopped.
        checked = Path(directory) / "check"
        checked.mkdir(mode=0o700)
        extract_archive(staged, checked)
        checksum = digest(staged)
        with staged.open("rb") as output:
            os.fsync(output.fileno())
        os.link(staged, archive)
    print(f"{checksum}  {archive}")


def restore_instance(archive, instance, expected_sha256):
    if expected_sha256 is not None and digest(archive) != expected_sha256:
        raise ValueError("快照归档 SHA-256 不匹配")
    with TemporaryDirectory(prefix="sqlite-restore-", dir=instance.parent) as directory:
        staged = Path(directory)
        extract_archive(archive, staged)
        # This mkdir reserves a previously absent destination for the atomic rename below.
        instance.mkdir(mode=0o700)
        try:
            staged.replace(instance)
        except OSError:
            instance.rmdir()
            raise
    print(f"已验证并恢复 SQLite 实例至 {instance}")


def destination(value):
    path = Path(value).expanduser()
    return path.parent.resolve(strict=True) / path.name


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(required=True, dest="command")
    backup = sub.add_parser("backup")
    backup.add_argument("instance", type=Path)
    backup.add_argument("archive", type=Path)
    restore = sub.add_parser("restore")
    restore.add_argument("archive", type=Path)
    restore.add_argument("instance", type=Path)
    restore.add_argument("--sha256")
    args = parser.parse_args()
    try:
        if args.command == "backup":
            archive_instance(args.instance.resolve(strict=True), destination(args.archive))
        else:
            restore_instance(args.archive.resolve(strict=True), destination(args.instance), args.sha256)
    except (OSError, ValueError, KeyError, tarfile.TarError) as error:
        parser.exit(1, f"SQLite 快照操作失败：{error}\n")


if __name__ == "__main__":
    main()
