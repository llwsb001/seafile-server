from concurrent.futures import ThreadPoolExecutor, TimeoutError
from contextlib import closing
from pathlib import Path
import sqlite3
from threading import Event
from time import monotonic, sleep
from uuid import UUID

from pysearpc import SearpcError
import pytest
from seaserv import seafile_api as api
from seaserv.service import SEAFILE_DATA_DIR
from tests.config import USER


ZERO_COMMIT = "0" * 40


def view_state(connection, view):
    return connection.execute(
        "SELECT base_commit, path FROM VirtualRepo WHERE repo_id = ?", (view,)
    ).fetchone()


@pytest.fixture
def sqlite_database():
    database = Path(SEAFILE_DATA_DIR).resolve() / "seafile.db"
    if not database.is_file():
        pytest.skip("requires the SQLite core database")
    return database


@pytest.fixture
def sqlite_view(repo, sqlite_database):
    with closing(sqlite3.connect(f"{sqlite_database.as_uri()}?mode=ro", uri=True)) as connection:
        if connection.execute("PRAGMA journal_mode").fetchone() == ("wal",):
            pytest.skip("requires rollback journal mode, where readers block writes")
        view = api.create_virtual_repo(repo.id, "/dir1", "SQLite contention", "", USER)
        assert view
        try:
            yield connection, view
        finally:
            connection.rollback()
            api.remove_repo(view)


def test_base_commit_waits_for_sqlite_reader(sqlite_view):
    reader, view = sqlite_view
    reader.execute("BEGIN")
    before = view_state(reader, view)
    assert before and before[0] != ZERO_COMMIT
    started = Event()

    def update():
        started.set()
        return api.set_base_commit(view, ZERO_COMMIT)

    with ThreadPoolExecutor(max_workers=1) as executor:
        pending = executor.submit(update)
        try:
            assert started.wait(timeout=5)
            # A read transaction pins a shared lock until the independent client releases it.
            with pytest.raises(TimeoutError):
                pending.result(timeout=0.25)
        finally:
            reader.rollback()
        assert pending.result(timeout=5) == 0

    assert view_state(reader, view) == (ZERO_COMMIT, before[1])


def test_base_commit_reports_unresolved_sqlite_lock(sqlite_view):
    reader, view = sqlite_view
    reader.execute("BEGIN")
    before = view_state(reader, view)
    assert before and before[0] != ZERO_COMMIT

    with ThreadPoolExecutor(max_workers=1) as executor:
        try:
            pending = executor.submit(api.set_base_commit, view, ZERO_COMMIT)
            with pytest.raises(SearpcError):
                pending.result(timeout=5)
        finally:
            reader.rollback()

    assert view_state(reader, view) == before
    assert api.set_base_commit(view, ZERO_COMMIT) == 0
    assert view_state(reader, view) == (ZERO_COMMIT, before[1])


def wait_for(condition, failure):
    deadline = monotonic() + 8
    while monotonic() < deadline:
        if condition():
            return
        sleep(0.05)
    pytest.fail(failure)


@pytest.mark.parametrize("maintenance_wins", (False, True), ids=("retry", "concurrent-maintenance"))
def test_virtual_repo_retries_background_base_update(sqlite_view, sqlite_database, repo, maintenance_wins):
    reader, view = sqlite_view
    origin = repo.id
    original_heads = (api.get_repo(origin).head_cmmt_id, api.get_repo(view).head_cmmt_id)
    assert view_state(reader, view) == (original_heads[0], "/dir1")

    # Each fixture owns its trigger and marker even when tests run concurrently.
    suffix = UUID(view).hex
    trigger = f"test_base_update_failure_{suffix}"
    marker = f"test_base_update_seen_{suffix}"
    with closing(sqlite3.connect(sqlite_database)) as writer:
        writer.execute(f"CREATE TABLE {marker} (observed INTEGER PRIMARY KEY)")
        try:
            # FAIL rejects the VirtualRepo update and preserves the recorded attempt.
            writer.execute(f"""
                CREATE TRIGGER {trigger} BEFORE UPDATE OF base_commit ON VirtualRepo
                WHEN OLD.repo_id = '{UUID(view)}' AND NEW.base_commit <> '{ZERO_COMMIT}'
                BEGIN
                    INSERT INTO {marker} VALUES (NULL);
                    SELECT RAISE(FAIL, 'injected base commit update failure');
                END
            """)
            assert api.post_empty_file(view, "/", "published.txt", USER) == 0
            wait_for(lambda: reader.execute(f"SELECT 1 FROM {marker}").fetchone() == (1,),
                     "the background virtual library merge never tried to write its base")
            published_heads = (api.get_repo(origin).head_cmmt_id, api.get_repo(view).head_cmmt_id)
            assert published_heads[0] != original_heads[0]
            assert published_heads[1] != original_heads[1]
            assert api.get_file_id_by_path(origin, "/dir1/published.txt") == ZERO_COMMIT
            assert api.get_file_id_by_path(view, "/published.txt") == ZERO_COMMIT
            assert view_state(reader, view) == (original_heads[0], "/dir1")

            if maintenance_wins:
                wait_for(lambda: reader.execute(f"SELECT count(*) FROM {marker}").fetchone()[0] >= 2,
                         "the background worker never retried its failed base update")
                assert api.set_base_commit(view, ZERO_COMMIT) == 0
                assert view_state(reader, view) == (ZERO_COMMIT, "/dir1")

            writer.execute(f"DROP TRIGGER {trigger}")
            if maintenance_wins:
                # Allow the pending one-second retry to run even though its CAS matches no rows.
                sleep(1.5)
                assert view_state(reader, view) == (ZERO_COMMIT, "/dir1")
            else:
                wait_for(lambda: view_state(reader, view) == (published_heads[0], "/dir1"),
                         "the background virtual library merge did not recover its base")
            assert (api.get_repo(origin).head_cmmt_id, api.get_repo(view).head_cmmt_id) == published_heads
        finally:
            writer.execute(f"DROP TRIGGER IF EXISTS {trigger}")
            writer.execute(f"DROP TABLE {marker}")
