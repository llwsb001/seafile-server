# Linux SQLite 发行候选与切换演练计划

库生命周期和离线维护已交付，下一阶段把已合入的 Go 组件做成可安装、可升级、可回退的 Linux SQLite 发行候选。候选沿用现有数据目录和客户端协议，通过明确的配置选择启用。首轮在 Linux amd64 完成全量验收，并在原生 Linux arm64 复验安装和接管。MySQL、FUSE、生产默认切换及 C 构建清理留待后续批次。 [迁移总计划](go-migration-plan.md) · [上一阶段计划](go-migration-sqlite-lifecycle-maintenance.md)

候选版本、实际回放结果和当前签收状态见 [发行候选验收记录](go-migration-sqlite-release-acceptance.md)。

## 起点

| 项目 | 锁定版本与状态 |
| --- | --- |
| 服务端 `master` | `b7e3be540a6929de101b06582adb771423b2edf8`，已包含 Go 核心、文件服务、controller、fsck 和 GC。 [服务端 #71](https://github.com/llwsb001/seafile-server/pull/71) |
| 兼容仓库 `main` | `dd673840a161d7ebd36745b7c3ae085ca6760ba0`，已合入 v8 回放、离线维护和队列接管 CI。 [兼容测试 #62](https://github.com/llwsb001/seafile-compat-tests/pull/62) |
| C 对拍版本 | `fa46d4c43a9a23ff2f70acf92c14e6cb28955664`。继续使用上一阶段锁定的 Seahub、客户端、SeafDAV 和 libsearpc。 [兼容仓库版本锁](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/versions.lock) |
| 合并后复验 | v7 在兼容仓库 `main` 的默认锁定组合通过。v8 首次在 `cross-origin-move-advances-source-origin` 报告 `$.ret` 差异，同一提交复跑通过。任务 1 先定位这次间歇性差异，再固定 v1–v8 的共同候选。 [v7 运行](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45741814560) · [v8 首次运行](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45741814503/attempts/1) · [v8 复跑](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45741814503/attempts/2) |

## 安装和数据契约

- **安装入口。** 当前 Go controller 从安装树的 `seafile/bin` 找同目录的核心和 fileserver。`[fileserver] use_go_fileserver = true` 让它启动 `seaf-server-go` 和 `fileserver`，并向子进程传递 `ENABLE_GO_FILESERVER`。任务 2 从实际部署脚本核对启停入口、安装目录、PID、日志、socket、CLI 参数、环境变量及 Python 依赖。验收记录实际 PID 对应的可执行文件路径，并单独验证原 C 包回退。 [Go controller 配置](../fileserver/cmd/seafilecontroller/config.go) · [Go controller 进程管理](../fileserver/cmd/seafilecontroller/supervisor.go) · [C 安装入口](../Makefile.am)
- **SQLite 初始化和升级。** Go 核心按已有路径打开库数据和 ccnet 身份库。全新安装从仓库 SQLite SQL 和锁定的 Seahub 初始化入口生成数据库，至少核对 `seafile.db`、`PeerMgr/usermgr.db`、`GroupMgr/groupmgr.db`、`OrgMgr/orgmgr.db`，以及配置库和 Seahub 数据库的实际位置。存量升级保持共享表及对象格式可供 C 读写。列出 Go 新增表及其用途，验证旧安装缺少 Go 私有表时可以升级，并检查 C 接管后的可读写状态。 [SQLite SQL](../scripts/sql/sqlite) · [身份库打开](../fileserver/internal/seafserver/identity.go) · [文件写入队列](../fileserver/internal/seafserver/file_write_queue.go)
- **外部消费者。** 核对锁定 Seahub、桌面客户端、SeafDAV、通知服务、离线命令和选定部署脚本的实际调用。兼容记录逐条关联调用点、C 实现、Go 入口和验收用例。暴露给支持范围内消费者的入口必须有通过结果。通知服务与 fileserver 共用部署提供的 `JWT_PRIVATE_KEY`，认证材料的值留在部署环境。 [C RPC 注册](../server/seaf-server.c) · [Go 核心注册](../fileserver/cmd/seafserver/main.go) · [通知服务配置](../notification-server/server.go)

## 任务与合入条件

每项用独立 worktree 开发，按依赖提交正式 PR。服务端交付安装与运行能力，兼容仓库交付可执行验收、样本及报告。开发 PR 可先使用已推送的依赖提交验收，合入前在依赖的最新版本上重跑交叉场景。

| 任务 | 交付 | 前置 | 合入验收 |
| --- | --- | --- | --- |
| 1 | 稳定 v8 合并后回归，记录共同基线。 | — | 单独回放 `virtual-transfer`，记录 C/C、C/Go 的源与目标路径返回、四条 head、`VirtualRepo.base_commit` 和 Go 队列。确认是等待条件、基线差异还是发布问题后对症修复。各独立副本连续通过 3 次，再在兼容 `main` 用同一个 Go 完整 SHA 复跑 v1–v8。保留源删除、目标摘要、视图汇合及 C 接管断言。 |
| 2 | 锁定发布消费者和安装目录契约，列出缺项。 | — | 用真实入口清单覆盖启动脚本、Python RPC、HTTP、通知、WebDAV 和维护 CLI。记录实际加载路径、参数、返回值、配置优先级和进程归属。每项缺口有复现步骤与预计归属。 |
| 3 | 补齐任务 2 发现的发布阻塞项，每项独立交付。 | 2 | 用锁定消费者触发旧行为，补 C/C 与 C/Go 对拍。成功、拒绝、重启和 C 接管均有断言。支持矩阵中的阻塞项清零。 |
| 4 | 交付可复建的 Linux SQLite 归档。 | 2 | 归档含 `seaf-server-go`、`fileserver`、`seafile-controller`、`seaf-fsck`、`seafserv-gc`、`notification-server` 和启动所需的模板。锁定 Go 工具链、依赖、架构、源码 SHA 与构建参数。两次干净构建的文件清单、权限和 SHA-256 一致。检查 ELF 架构与动态依赖，在精简运行环境解包运行。 |
| 5 | 用候选归档在空目录安装。 | 2、4 | 用普通服务账号，从空目录初始化 SQLite 和 Seahub。管理员登录后建库、上传、下载，客户端克隆并写入。验证重复初始化保留已有库、空目录初始化失败可重试、缺依赖或路径不可写有明确错误。 |
| 6 | 接通存量安装选择 Go 和切回 C 的入口。 | 2、4 | 使用部署脚本依次完成启动、配置检查、停止、重启和回退。运行普通服务账号、相对路径及带空格目录用例。注入错误配置、缺二进制、端口占用和进程故障，确认不会留下第二个写入组合，停服后端口、PID 和 socket 可复用。 |
| 7 | 用实际 WebDAV 和通知进程验收发行安装。 | 3、5、6 | 通过已配置的 WebDAV 入口用两个账号检查列目录、上传、下载、Unicode 路径与只读权限。验证两个客户端能收到实际更新通知，SeafDAV 或通知进程重启后恢复。C 接管读取 WebDAV 写入内容并继续操作。 |
| 8 | 交付停服快照和独立恢复演练。 | 4、6 | 记录配置、SQLite 库、对象目录、密钥引用及恢复所需的文件权限。恢复到新目录，核对 `PRAGMA integrity_check`、对象引用及文件摘要。用 WAL 样本验证快照含已提交事务，通过恢复包分别启动 Go 和 C。 |
| 9 | 冻结 v9 同数据安装、升级、维护与回退用例。 | 1、3、5–8 | 使用候选归档与保留的 C 安装，在普通、加密、虚拟库和回收站样本上执行下文的接管矩阵。通过已安装路径调用 `seaf-fsck` 和 `seafserv-gc`，并在隔离副本测试启动中断、存在待处理队列和回退失败。 |
| 10 | 用真实进程测量性能及持续运行。 | 1、4、6 | 固定负载、机器、监控采样与通过门槛后，交替测 C 和 Go。Go 从发行归档安装，报告记录实测归档的 SHA-256。记录 Web 和同步延迟及吞吐、进程 RSS、SQLite 写锁等待、队列深度与排空耗时。检查负载中的数据正确性，附原始结果。 |
| 11 | 接入发行归档构建和 v9 验收 CI。 | 4、9 | CI 构建一次，安装、回退和报告使用同一份归档及 SHA-256。缺少归档、摘要错误、空样本或失败断言使任务失败。保留 v1–v8 的既有入口，v9 记录安装的二进制路径与架构。 |
| 12 | 完成候选签收和演练手册。 | 7–11 | amd64 跑完整矩阵，arm64 原生运行安装、新旧数据启动和 C 接管。手册记录版本、安装、只读检查、切换步骤、触发回退的条件、恢复步骤与耗时。最终报告附候选和兼容仓库 SHA、测试产物、性能结果及冻结差异。 |

任务 1、2 可以并行。安装契约锁定后，消费者缺口、归档及性能负载可在独立 worktree 开始准备。归档就绪后并行做空目录安装、存量启停和快照演练。涉及实际安装树的接管测试在对应功能合入后运行。

## 验收方式

### 复跑既有兼容矩阵

使用相邻兼容仓库和锁定的 C 构建。`SEAFILE_GO_REVISION` 是已推送的完整服务端提交，v7 和 v8 显式使用同一个值。v7 默认锁定 Go 候选早于本阶段起点，直接调用默认入口只覆盖历史基线。v1–v6 按各版样本说明逐项复跑，用同一候选的二进制和相应配置。记录每一版 C/C 与 C/Go 的执行数量、已冻结差异及归档摘要。 [v7 入口](https://github.com/llwsb001/seafile-compat-tests/tree/dd673840a161d7ebd36745b7c3ae085ca6760ba0/fixtures/v7) · [v8 入口](https://github.com/llwsb001/seafile-compat-tests/tree/dd673840a161d7ebd36745b7c3ae085ca6760ba0/fixtures/v8)

```sh
SQLITE_CI_GO_REVISION="$SEAFILE_GO_REVISION" SQLITE_CI_GO_SUITE=all \
  make -C "$SEAFILE_COMPAT_TESTS" test-fileserver-ci
SQLITE_CI_GO_REVISION="$SEAFILE_GO_REVISION" \
  make -C "$SEAFILE_COMPAT_TESTS" test-sqlite-lifecycle-ci
```

### 用同一份数据完成安装、升级和回退

任务 9 在兼容仓库增加 `fixtures/v9` 和 `make test-sqlite-release`。运行器接收候选归档、归档清单、C 安装目录及锁定消费者。通过安装后的实际启动脚本、私有 RPC、HTTP、WebDAV 和维护命令运行以下步骤。普通、加密、虚拟库至少各有一份，样本包含历史、回收站、已签发的同步令牌和一个明确可回收的孤立对象。

| 步骤 | 操作与断言 |
| --- | --- |
| C 写入与快照 | 使用实际 Seahub 和两个客户端准备存量库，记录当前和历史文件 SHA-256、权限、分支、虚拟库 base commit、回收站状态。停止写入方后保存一致快照，检查 SQLite 完整性。 |
| Go 接管与重启 | 停 C、确认无剩余写进程，再从版本化安装树启动 Go。验证账号登录、已有权限、历史、加密文件、Web 和两个客户端的读写。执行虚拟库写入和回收站恢复，重启后核对持久队列、主分支、内容和统计。 |
| 停服维护 | 运行安装树中的 fsck、GC 完整性检查和 dry-run。核对只读命令前后的 SQLite 逻辑行、对象清单和摘要。在副本执行修复或真实 GC，检查预期删除集合、可访问历史以及保留对象。 |
| C 接管并继续写 | 关闭 Web、同步、WebDAV 和管理 RPC 等写入入口，等在途写入完成。保留 Go 后台进程，等队列排空且统计跟上最新 head。停止全部 Go 写入进程后，复核队列为空、head 与停服前一致且统计对应当前 head。任一条件未满足则暂停回退，保持写入入口关闭。复核通过后，从保留的 C 包启动并接管同一数据，验证内容、历史边界、权限和已持久化令牌。再通过 Seahub、客户端及启用的 WebDAV 各执行一次新写入。 |
| 中断与恢复 | 在独立副本测试切换前停止、Go 启动失败、停写时仍有在途写入、队列排空超时和 C 回退失败。核对最后一笔已确认写入进入 C 接管后的数据，并验证队列未排空时 C 保持停服。记录进程及引用状态，验证切换脚本保留可重试状态。另从快照恢复到新目录，核对快照时点的读写与摘要，并记录恢复耗时和可恢复的数据时点。 |

计划内回退以 C 原位接管为验收目标。发现数据损坏或 C 无法启动时，保留现场并走快照恢复演练。记录最后有效快照至最后一次确认写入之间的时间差，以及恢复后需要补做的写入。

### 性能和候选签收

在同一原生 Linux 主机上为 C、Go 使用同一硬件配额、数据和客户端版本。分别预热后交替运行，至少记录 3 轮。负载包含小文件批量同步、大文件 Web 上传与下载、双客户端增量和虚拟库后台收敛。先记录 C 的延迟 p50/p95、吞吐、峰值 RSS、SQLite 锁等待和队列排空时间，再由任务 10 的评审在 Go 候选结果出炉前写入各项可接受偏差及采样窗口。持续运行用例还需验证没有丢失提交、漏删或多删对象、权限放宽、队列持续堆积和统计倒退。

候选报告须给出两仓库完整 SHA、C 与外部组件版本、归档摘要、架构、实际运行命令、场景数量、已知差异和恢复耗时。签收时核对任务 10 报告中的实测归档 SHA-256 与最终候选归档一致。摘要变化后，使用最终归档重跑性能和持续运行验收，更新报告。只有 v1–v9、发行归档安装、同数据 C 接管和性能门槛全部通过，才签收为 Linux SQLite 可选用候选。
