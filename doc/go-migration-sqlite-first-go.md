# Linux SQLite 与本地文件存储首个 Go 候选

本次验收覆盖 Linux amd64、arm64 上使用 SQLite 元数据和本地文件系统对象存储的部署。归档提供核心、文件服务、controller、通知、fsck 和 GC 的 Go 二进制。Seahub 使用独立维护的 fork，由部署环境提供。 [构建及安装布局](../release/sqlite/README.md)

候选 `bfc35eee` 已通过双架构功能与 Go 独立持续负载验收，以及 amd64 的 C/Go 相对性能验收。

## 提交与归档

| 组件 | 实测提交 |
| --- | --- |
| 服务端 | `bfc35eee28b6ad9cf5a6d4a2063318c56f3333c3` |
| 兼容测试 | `67704455ba14d8a1941e4fbe53513d76f7d2b0fd` |
| 部署用 Seahub fork | `8f2810f7667d99a44ff1efc08dafee6d8eb79b55` |

| 架构 | 归档 SHA-256 |
| --- | --- |
| amd64 | `b3543f154eaba7a044cb2644c9e5b9af81c872cf768f78816821df2e9894b0d6` |
| arm64 | `5f7d7977634964b3be8f42912151d48df79b2696c872eb85603fcea0276c0828` |

每个架构复用同一归档执行发行回放。历史功能对拍使用 C `fa46d4c43a9a23ff2f70acf92c14e6cb28955664` 及原 Seahub `62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab`。真实桌面客户端、SeafDAV、SeafObj、libsearpc 和 Python 继续按兼容仓库的锁文件准备。 [双架构归档及日志](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45793601237) · [C 依赖锁](https://github.com/llwsb001/seafile-compat-tests/blob/67704455ba14d8a1941e4fbe53513d76f7d2b0fd/ci/sqlite-fileserver.lock) · [运行时依赖锁](https://github.com/llwsb001/seafile-compat-tests/blob/67704455ba14d8a1941e4fbe53513d76f7d2b0fd/ci/sqlite-release-runtime.lock)

## 功能回放

- v1 的 113 个冻结对象和 41 个 RPC 场景通过，锁定加密库的密钥、IV 与明文摘要一致。v2–v8 的协议、权限、写入和生命周期回归通过。 [兼容测试入口](https://github.com/llwsb001/seafile-compat-tests/tree/67704455ba14d8a1941e4fbe53513d76f7d2b0fd)
- 两个架构均通过 v9 安装、同数据 C → Go → C 接管及快照恢复。v10 各有 28 项匹配，v11 通过虚拟库 base commit、默认系统库以及包含真实桌面同步的加密库回放。 [双架构结果](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45793601237) · [桌面克隆就绪修正](https://github.com/llwsb001/seafile-compat-tests/pull/74)
- Go 独立持续负载各完成三轮，单轮 180–183 秒。amd64 分别完成 56、59、45 次负载循环，arm64 分别完成 57、51、45 次。每轮均排空队列并通过 SQLite 完整性、安装版 fsck 和同数据重启后的增量同步。 [双架构 `go-load.json`](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45793601237)
- Seahub fork 的普通用户搜索在 C 与 Go 两种核心上均通过冷、热缓存回放，未授权用户仍收到 403，库令牌搜索权限和虚拟库范围均通过。 [Seahub 修复](https://github.com/llwsb001/seahub/pull/1)

## amd64 性能签收

在同一台 Linux amd64 runner 上为 C 和 Go 分配两个 vCPU 和 4 GiB 内存。两组共享同一套 Seahub fork、Redis、锁定桌面客户端以及归档中的通知进程，使用独立的 SQLite 数据副本。C 从冻结版本 `fa46d4c` 叠加已合入的虚拟库锁竞争修复 `49910b30523759c53e06a82f5c01b04058d04287` 后重新构建，实际运行二进制的 SHA-256 为 `86503ced1ee9d4206be12362dea6eb5901c929e4b95d2dd5cd1b6fe279ed5ad1`。 [C 修复](https://github.com/llwsb001/seafile-server/pull/116) · [复验脚本](https://github.com/llwsb001/seafile-compat-tests/blob/03a0c257c39b2d99182e7a94ac5e8dab4a2f5052/ci/run-sqlite-performance-signoff.sh)

先测三轮 C 并冻结 `limits.json`，再按 C → Go、Go → C、C → Go 的顺序实测。各轮包含预热及至少 180 秒正式负载。冻结 C 完成 45、53、45 次负载循环，交替 C 完成 45、45、45 次，Go 完成 53、45、58 次。每轮均通过下载摘要、双端同步、删除、权限、虚拟库收敛和停服后的 SQLite 完整性检查。

| 指标 | 冻结 C | 交替 C | Go | Go 门槛 |
| --- | ---: | ---: | ---: | --- |
| Web 上传 p95（ms） | 89.82 | 91.32 | 107.38 | ≤ 两套 C 各自的 1.25 倍 |
| Web 下载 p95（ms） | 18.30 | 19.29 | 17.38 | ≤ 两套 C 各自的 1.25 倍 |
| Web 上传吞吐 p50（MiB/s） | 283.33 | 278.43 | 246.96 | ≥ 两套 C 各自的 80% |
| Web 下载吞吐 p50（MiB/s） | 1453.87 | 1415.93 | 1451.28 | ≥ 两套 C 各自的 80% |
| 双客户端小文件同步收敛 p95（s） | 3.612 | 4.156 | 3.739 | ≤ 两套 C 各自的 1.25 倍 |
| 双客户端小文件同步吞吐 p50（MiB/s） | 0.0824 | 0.0814 | 0.0913 | ≥ 两套 C 各自的 80% |
| 虚拟库写入收敛 p95（ms） | 792.28 | 841.82 | 75.61 | ≤ 两套 C 各自的 1.25 倍 |
| 服务端进程合计 RSS p95（MiB） | 169.19 | 163.50 | 234.91 | ≤ 两套 C 各自的 1.50 倍 |
| 服务端进程合计 RSS 采样峰值（MiB） | 172.46 | 178.07 | 241.98 | ≤ 256 MiB |
| SQLite 写锁等待 p95（ms） | 0.131 | 0.130 | 0.131 | ≤ 5 ms |
| SQLite 写锁探针超时率（单轮最大） | 0% | 0% | 0% | ≤ 1% |
| Go 持久写队列峰值 | — | — | 1 | ≤ 4 |

Go 对两组 C 参考的全部门槛均通过。Web 上传 p95 增加 17.6%–19.6%，上传吞吐 p50 下降 11.3%–12.8%，RSS p95 增加 38.8%–43.7%。虚拟库写入收敛 p95 从 792–842 ms 降至 76 ms。

| 证据文件 | SHA-256 |
| --- | --- |
| `c-baseline.json` | `7ed3309f0f04189a75365367c00610cdd919e165869b7e4556ee7d52bfbee4b7` |
| `limits.json` | `a62dbac67a8a76d16519922aa00084318c530bca74da4fd47df34ce926d79687` |
| `comparison.json` | `338118fcd9f8ef11501d0d0b1eedee7d482cf7693c9f3501bf5930a4c5c68d8f` |

原始采样、构建环境、C 修复补丁和冻结时间顺序保存在 `sqlite-amd64-performance` 产物。 [性能验收产物](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45795603270/artifacts/15108150626) · [采样契约](https://github.com/llwsb001/seafile-compat-tests/blob/67704455ba14d8a1941e4fbe53513d76f7d2b0fd/fixtures/v9/performance.md)

## 安装与回退

按架构选择归档，校验摘要后解压到新的版本目录。存量实例共享配置和数据目录。回退前关闭写入入口，等待在途写入完成及 Go 队列排空，核对 head 和统计后停服，再从原 C 版本目录接管。同数据接管失败时保留现场，使用已验证的快照恢复。 [启停与回退](../release/sqlite/SERVICE.md) · [快照与恢复](../release/sqlite/SNAPSHOT.md)
