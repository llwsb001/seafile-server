# 数据与权限

- 集成测试使用隔离实例。`ci/run.py` 每轮清理 `/tmp/seafile-tests`，MySQL 测试会创建和删除 `ccnet` 与 `seafile` 测试数据库。 [测试启动代码](../../ci/run.py) · [数据库管理](../../ci/serverctl.py)
- GC 和带 `--repair` 的 fsck 会改动对象或分支。兼容性验证在数据副本上执行这两类测试。 [GC](../../server/gc/gc-core.c) · [fsck](../../server/gc/seaf-fsck.c)
- 测试连接参数、令牌和密钥从测试环境传入。PR 只记录复现所需的非敏感配置、命令和摘要结果。
