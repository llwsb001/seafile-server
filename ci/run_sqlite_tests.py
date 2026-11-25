#!/usr/bin/env python3
"""在 sqlite3 + 原版 C fileserver 上跑 seafile-server 的 pytest 套件。

上游 ci/run.py 的 main() 硬编码只跑 mysql，这里复用它的 start_and_test_with_db('sqlite3')。
用法：python3 ci/run_sqlite_tests.py
"""
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
os.chdir(os.path.dirname(HERE))  # 仓库根目录

import run  # noqa: E402

run.mkdirs(run.INSTALLDIR)
os.environ.update(run.make_build_env())
run.start_and_test_with_db('sqlite3')
