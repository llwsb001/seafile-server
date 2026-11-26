# Linux SQLite 发行候选验收记录

最终候选的提交、双架构归档和性能签收见 [首个 Go 候选验收记录](go-migration-sqlite-first-go.md)。

组合提交 `870f191a5f50bda658c540b370c716b4e88618e9` 的历史回放验证了可选择启用的 Linux SQLite 归档、原 C 版本的同数据接管和独立快照恢复。当时的 C 基线持续负载有失败，性能签收留待最终候选。 [阶段计划](go-migration-sqlite-release.md) · [组合候选](https://github.com/llwsb001/seafile-server/commit/870f191a5f50bda658c540b370c716b4e88618e9)

| 产物 | SHA-256 |
| --- | --- |
| Linux amd64 归档 | `68b4d0594208f81f2db47c034eff5a1f7fd181a00b60878f135ecbc378047307` |
| Linux arm64 归档 | `faea567991b3485291ec69b108c5800d681c6800eb3296c3be00e932c083637a` |
| C 基线 | 源码提交 `fa46d4c43a9a23ff2f70acf92c14e6cb28955664`，在各架构构建。 |

使用两个仓库 PR 中的锁文件准备 Go 1.24.13、C、Seahub、SeafDAV、libsearpc 和桌面客户端。归档在实际安装前检验 SHA-256，安装后核对归档内清单和进程加载的文件。 [构建入口](../release/sqlite/README.md) · [消费者版本](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/versions.lock)

v1–v8 回放使用兼容仓库 `98b9d9299f8880e66a0fde4738e226e73f320a9e`，v9 安装回放使用 `8eb9d2f574025c61aa17f345ba14745bed0378f1`。 [v1–v8 回放](https://github.com/llwsb001/seafile-compat-tests/commit/98b9d9299f8880e66a0fde4738e226e73f320a9e) · [v9 回放](https://github.com/llwsb001/seafile-compat-tests/commit/8eb9d2f574025c61aa17f345ba14745bed0378f1)

## 功能证据

| 验收项 | 实际结果 |
| --- | --- |
| v8 间歇性差异 | 两份锁定 C 复现跨原库移动后后台更新 `Branch` 的 SQLite 死锁。同步发布的两个视图已更新，源原库仍保留文件。修复回放后，C/C 和 C/Go 定向用例各连续通过三次。Go 继续检查两个原库和持久队列收敛。 [兼容测试 #63](https://github.com/llwsb001/seafile-compat-tests/pull/63) |
| v1–v8 | v1 的 113 个冻结对象校验通过。用 `870f191` 的 amd64 归档完成 41 个 v1 RPC 样本和 v2–v6 对拍。v7 通过 25 组 473 步，v8 通过 43 组 491 步及 92 条离线命令，另通过 controller 和虚拟库恢复队列的接管回放。 |
| 空目录安装 | 原生 arm64 使用独立安装候选，实际 Seahub 管理员登录、建库、重名拒绝、Web 与桌面同步通过。初始化可并发重试，已有内容保持原状。 [服务端 #77](https://github.com/llwsb001/seafile-server/pull/77) · [服务端 #78](https://github.com/llwsb001/seafile-server/pull/78) |
| 安装包启停 | 确认带空格路径、缺失 JWT、端口占用、子进程恢复和错误版本的停止请求。未排空的 Go 队列阻止 C 接管，清空后原 C 继续读写。 [服务端 #75](https://github.com/llwsb001/seafile-server/pull/75) · [服务端 #79](https://github.com/llwsb001/seafile-server/pull/79) |
| WebDAV 与通知 | 真实 DAV 通过 Unicode 创建、覆盖、校验下载以及只读拒绝。分别重启 DAV 和通知，两个客户端都收到期望提交并完成增量。Go 文件覆盖对显式过期 head 返回冲突。锁定 DAV 传入空 head。 [服务端 #76](https://github.com/llwsb001/seafile-server/pull/76) · [兼容测试 #64](https://github.com/llwsb001/seafile-compat-tests/pull/64) · [兼容测试 #65](https://github.com/llwsb001/seafile-compat-tests/pull/65) |
| 快照恢复 | 归档包含已提交的 WAL 事务、对象和配置权限。恢复到两份新目录后，C 和 Go 都通过 fsck、读取及后续写入。arm64 样本恢复分别耗时 0.039 秒和 0.036 秒，从恢复到验证新写入及重启分别耗时 0.400 秒和 0.906 秒。活动 PID 或 socket、错误摘要、同名目标均被拒绝。 [服务端 #80](https://github.com/llwsb001/seafile-server/pull/80) |
| v9 已安装接管 | 同一组合提交的原生 arm64、本地 amd64 仿真及 GitHub 原生 amd64 全量回放通过。真实 Web、DAV 与两个客户端在 C → Go → C 后继续写入。已安装的 fsck、GC、普通、加密、虚拟与回收站样本均参与。 [兼容测试 #66](https://github.com/llwsb001/seafile-compat-tests/pull/66) · [原生 amd64 回放](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45748705665) |

## 部署与回退

按绝对或相对版本目录和当前服务账号执行。先校验归档，解压到新版本目录，初始化新实例时使用同一归档的 `init.py`。存量实例把 SQLite 数据和配置留在共享实例目录，保留原 C 安装、Python 消费者及其动态库环境。

切换前关闭写入入口，核对主分支、历史、权限及文件摘要，停止服务后保存快照。启动 Go 后，通过实际用户路径验证原有读取及一次新写入。回退时先关闭写入入口并等在途请求完成，保留 Go 后台进程排空队列并更新统计。停止 Go 后复核队列、head 和统计，通过后才启动原 C。同数据回退失败时保留现场，从已验证快照恢复到新目录，并核对快照时点后待补的写入。启停和快照参数见各工具说明。 [启停与回退](https://github.com/llwsb001/seafile-server/pull/79) · [快照手册](https://github.com/llwsb001/seafile-server/pull/80)

## 发行门槛

- v9 CI 用同一完整 SHA 构建一次归档，并用该归档生成验收日志和摘要。GitHub 原生 amd64 使用兼容仓库合并检出 `d25929415c337946a242e04a63123e106ee2f43d`，下载的归档摘要与本地构建一致。锁定组合候选的原生 v8 也通过。 [兼容测试 #68](https://github.com/llwsb001/seafile-compat-tests/pull/68) · [v9 归档及日志](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45748705665) · [原生 v8](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45748928862)
- 性能回放已通过原生 arm64 的 C/Go 冒烟。锁定 C 两次持续负载均出现 `COMMIT` 和 `UPDATE VirtualRepo` 失败，虚拟库未与原库及 base commit 汇合。第二次的采样器全程未打开 C 数据库连接。WAL 诊断副本中的 C 在读取库时也报告 SQLite 锁冲突，真实 Web 查询返回 404。持续 C 基线没有通过，因此正式 C/Go 门槛暂未冻结。 [兼容测试 #67](https://github.com/llwsb001/seafile-compat-tests/pull/67)
- 上述归档摘要和耗时对应回放时的组合提交。最终签收时核对候选归档 SHA-256 与实测归档一致。摘要变化时，使用最终归档重跑性能和持续运行验收。

持续负载的探针排查和 Go 独立验收见 [SQLite 稳定性计划](go-migration-sqlite-stability.md)。
