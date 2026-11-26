#!/usr/bin/env python3
"""Initialize the shared databases and configuration for a SQLite release."""

import argparse
from collections.abc import Callable
import configparser
from contextlib import closing
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import sys
from tempfile import TemporaryDirectory


DATABASES = {
    "seafile-data/seafile.db": "seafile.sql",
    "ccnet/PeerMgr/usermgr.db": "user.sql",
    "ccnet/GroupMgr/groupmgr.db": "groupmgr.sql",
    "ccnet/OrgMgr/orgmgr.db": "org.sql",
    "ccnet/misc/config.db": "config.sql",
}


def publish_new_file(destination: Path, create: Callable[[Path], None]) -> None:
    if destination.is_file():
        return
    destination.parent.mkdir(parents=True, exist_ok=True)
    with TemporaryDirectory(prefix=".sqlite-init-", dir=destination.parent) as scratch:
        staged = Path(scratch) / destination.name
        create(staged)
        staged.chmod(0o600)
        # A concurrent initializer may have already published a complete file.
        try:
            os.link(staged, destination)
        except FileExistsError:
            if not destination.is_file():
                raise


def check_sqlite(database: Path) -> None:
    # The published file must be complete without any temporary journal files.
    with closing(sqlite3.connect(f"{database.as_uri()}?mode=rw", uri=True)) as connection:
        if connection.execute("PRAGMA journal_mode=DELETE").fetchone() != ("delete",):
            raise sqlite3.DatabaseError(f"could not close the journal for {database.name}")
        if connection.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
            raise sqlite3.DatabaseError(f"integrity check failed for {database.name}")


def create_sqlite(database: Path, schema: str) -> None:
    with closing(sqlite3.connect(database)) as connection:
        connection.executescript(f"BEGIN;\n{schema}\nCOMMIT;")
    check_sqlite(database)


def seahub_database_setting(database_expression: str) -> str:
    return (
        'DATABASES = {"default": {"ENGINE": "django.db.backends.sqlite3", '
        f'"NAME": {database_expression}}}}}\n'
    )


def migrate_seahub(database: Path, instance: Path, version: Path) -> None:
    seahub = version / "seahub"
    settings = "from seahub.settings import *\n" + seahub_database_setting(repr(str(database)))
    (database.parent / "sqlite_release_migrate.py").write_text(settings, encoding="utf-8")
    pythonpath = (str(database.parent), str(seahub / "thirdpart"), os.environ.get("PYTHONPATH", ""))
    environment = {
        **os.environ,
        "PYTHONPATH": os.pathsep.join(filter(None, pythonpath)),
        "PYTHONDONTWRITEBYTECODE": "1",
        "SEAFILE_CENTRAL_CONF_DIR": str(instance / "conf"),
        "SEAFILE_DATA_DIR": str(instance / "seafile-data"),
        "SEAFILE_RPC_PIPE_PATH": str(version / "runtime"),
        "CCNET_CONF_DIR": str(instance / "ccnet"),
        "SEAHUB_LOG_DIR": str(instance / "logs"),
    }
    subprocess.run(
        [sys.executable, str(seahub / "manage.py"), "migrate", "--run-syncdb", "--noinput",
         "--settings=sqlite_release_migrate"],
        cwd=seahub, env=environment, check=True,
    )
    check_sqlite(database)


def initialize(version: Path) -> None:
    version = version.resolve(strict=True)
    instance = version.parent
    share = version / "seafile/share"
    manage = version / "seahub/manage.py"
    if not manage.is_file():
        raise FileNotFoundError(f"install the pinned Seahub tree at {manage.parent}")
    schemas = {
        name: (share / "sqlite" / source).read_text(encoding="utf-8")
        for name, source in DATABASES.items()
    }
    configs = {
        name: (share / "config" / name).read_text(encoding="utf-8")
        for name in ("seafile.conf", "seafdav.conf")
    }
    conf = instance / "conf"
    for directory in (conf, instance / "ccnet", instance / "seafile-data", instance / "seafile-data/storage",
                      instance / "seahub-data", instance / "logs", instance / "pids", version / "runtime"):
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)

    for name, content in configs.items():
        publish_new_file(conf / name, lambda staged: staged.write_text(content, encoding="utf-8"))
    config = configparser.ConfigParser(interpolation=None)
    with (conf / "seafile.conf").open(encoding="utf-8") as file:
        config.read_file(file)
    if config.get("database", "type", fallback="").strip().lower() != "sqlite":
        raise ValueError(f"{conf / 'seafile.conf'} requires [database] type = sqlite")

    seahub_settings = (
        "from pathlib import Path as _Path\n"
        "_instance_root = _Path(__file__).resolve().parent.parent\n"
        f"SECRET_KEY = {secrets.token_urlsafe(48)!r}\n"
        'SEAHUB_DATA_ROOT = str(_instance_root / "seahub-data")\n'
    ) + seahub_database_setting('str(_instance_root / "seahub.db")')
    for name, content in (("ccnet.conf", "[General]\n"), ("seahub_settings.py", seahub_settings)):
        publish_new_file(conf / name, lambda staged: staged.write_text(content, encoding="utf-8"))
    for name, schema in schemas.items():
        publish_new_file(instance / name, lambda staged: create_sqlite(staged, schema))

    publish_new_file(instance / "seahub.db", lambda staged: migrate_seahub(staged, instance, version))
    print(f"SQLite initialization complete: {instance}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", type=Path, help="extracted SQLite release directory with Seahub installed")
    arguments = parser.parse_args()
    try:
        initialize(arguments.version)
    except (OSError, ValueError, configparser.Error, sqlite3.Error, subprocess.CalledProcessError) as error:
        parser.exit(1, f"SQLite initialization failed: {error}\n")


if __name__ == "__main__":
    main()
