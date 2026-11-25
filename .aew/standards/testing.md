# 验收

- C 的 SQLite 回归在 Linux 上从已编译的源码树运行。按 [基线说明](../../baseline/BUILD_NOTES.md) 中的 SQLite 测试步骤配置安装前缀和 Python，脚本负责启动测试实例。MySQL 的 C、Go 文件服务组合见 [CI 入口](../../ci/run.py)。
- 对象和持久化验收使用 `../seafile-compat-tests` 的 `main`，须包含其 [PR #1](https://github.com/llwsb001/seafile-compat-tests/pull/1)。按 [v1 样本说明](https://github.com/llwsb001/seafile-compat-tests/blob/main/fixtures/v1/README.md#取得冻结归档) 取得 `storage.tar`，再在兼容仓库运行 `make verify-fixtures`。预期 8 个块、78 个 FS 对象和 27 个 commit，差异为零。归档和主 SQLite 数据库均有固定 SHA-256。
- 数据写入测试使用各自独立的数据库和对象目录副本。RPC 以现有 Python 客户端的响应为验收依据，HTTP 和客户端同步以实际请求结果为依据。门槛见 [迁移计划](../../doc/go-migration-plan.md)。
