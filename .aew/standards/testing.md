# 验收

- C 的 SQLite 回归使用 Linux C 基线启动服务，再运行 `uv run python ci/run_sqlite_tests.py`。依赖、库路径和历史结果见 [基线说明](../../baseline/BUILD_NOTES.md)。MySQL 的 C、Go 文件服务组合见 [CI 入口](../../ci/run.py)。
- 对象和持久化兼容性用 `../seafile-compat-tests/fixtures/v1` 的实际 C 数据验收。样本需要配套的 `storage.tar`。在兼容仓库运行 `make verify-fixtures`，确认输出含 8 个块、78 个 FS 对象和 27 个 commit，且差异为零。Git 分支以兼容仓库的实现和 [迁移计划](../../doc/go-migration-plan.md) 为准。
- 数据写入测试使用各自独立的数据库和对象目录副本。RPC 以现有 Python 客户端的响应为验收依据，HTTP 和客户端同步以实际请求结果为依据。门槛见 [迁移计划](../../doc/go-migration-plan.md)。
