# SQLite 文件服务迁移计划

本轮交付 Linux 上的 **Go 核心 + Go fileserver + SQLite**。现有 Seahub 客户端和桌面客户端应能通过原有入口下载、上传、更新和同步，C 应能接管 Go 写过的数据。范围包括阶段 6 的 SQLite HTTP 与部署，以及这条链路需要的阶段 5 存量库写入。回收站、历史恢复、GC 和 fsck 工具迁移继续沿用总计划。 [迁移总计划](go-migration-plan.md)

| 基线 | 本轮起点 |
| --- | --- |
| 服务端 `master` | `f5afe736d9e4ff19dd8386234714b3d9e249420a`，含阶段 4 核心及第一批库写入。 |
| 兼容仓库 `main` | `ee387a31690616cd80aa0e9cc92adf3ebff499e6`，含 v1–v6。 |
| C 对拍版本 | 沿用 v5、v6 锁定的 `fa46d4c43a9a23ff2f70acf92c14e6cb28955664`。 |

默认分支合入前，在 Linux ARM64 用集成组合重跑了 v5 和 v6。v5 的 541 步均符合预期，含 21 步按 Go 预期校验的差异，另有 2 步仅回放 C。v6 的 41 步零差异。现有同步令牌、解密与配额验收覆盖实际消费函数，完整服务启动与传输从本轮开始验收。 [阶段 4 与第一批写入验收](go-migration-sqlite-stage4-acceptance.md)

## 实现前核对的契约

- **进程与数据库。** Go fileserver 当前启动时两次打开 MySQL，SQLite 的库数据和群组数据分别在 `seafile-data/seafile.db` 和 `ccnet/GroupMgr/groupmgr.db`。复用 Go 核心现有 SQLite 开库和事务约定，明确文件服务的 ccnet、配置、数据和 socket 参数。启动时校验必需的数据库和配置，按数据目录分别管理连接和写锁。当前 controller 按配置选择 fileserver，C 核心也独立读取配置和 `ENABLE_GO_FILESERVER` 决定是否启动自己的 HTTP，因此切换时两处必须一致。 [Go 文件服务启动](../fileserver/fileserver.go) · [数据库配置](../fileserver/option/option.go) · [核心 SQLite 写入](../fileserver/internal/seafserver/sqlite_write.go) · [C controller](../controller/seafile-controller.c) · [C 会话启动](../server/seafile-session.c)
- **对象和主分支。** Go 文件服务的更新使用 `SELECT … FOR UPDATE`，提交 ID 计算入口也与 Go 核心初始建库分开。用 v1、v6 和新写入样本核对 C 对对象格式、生成 ID 和父提交的约定。按既有数据版本解析历史对象，落盘后按旧 head 条件发布；失败时 `Branch` 和元数据保持原值，重复请求和过期 head 按实际客户端结果验收。SQLite C 对 `GCID`、`LastGCID` 有专门的返回路径；在未带这两张表的原有 SQLite schema 上验证上传和同步。 [Go 分支更新](../fileserver/fileop.go) · [Go 提交编码](../fileserver/commitmgr/commitmgr.go) · [C 分支更新](../common/branch-mgr.c) · [C 在线 GC 判断](../server/repo-mgr.c)
- **鉴权与通知。** Web 令牌、持久同步令牌按各自生命周期验证。管理 RPC 仍由现有开关控制，核心与 fileserver 使用同一服务账号连接私有 socket。对真实 fileserver 进程重复预热令牌，确认核心撤销成功后的下一次请求已失效。对真实的已有分支更新分别核对进程内事件和外部通知，普通建库沿用已经验收的分支新增语义。发送失败单独观测，分支写入以 SQLite 提交是否成功判定。 [令牌解析与同步路由](../fileserver/sync_api.go) · [核心启动](../fileserver/cmd/seafserver/main.go) · [通知发送](../fileserver/internal/seafserver/notification.go)
- **HTTP 与调用方。** 以 C、Go 的实际路由和调用请求确定覆盖范围。C 已注册 `/update-blks-api`、`/upload_progress`、`/idx_progress`，最新 Go 路由缺这三项，逐项核对现有调用方并冻结 C 响应。Seahub 内部查询和 ZIP 准备、下载及进度的实际调用需要一起验证。 [Go 路由](../fileserver/fileserver.go) · [C 上传路由](../server/upload-file.c) · [C 下载路由](../server/access-file.c) · [C 同步路由](../server/http-server.c)

## 任务与合入条件

一项功能一个正式 PR。兼容仓库的样本随对应场景提交。用独立 worktree 并行开发，先按表中的前置关系验收，再按依赖顺序合入。功能 PR 记录 C 版本、快照、实际请求结果和 SQLite 状态，跨模块改动附组合运行结果。

| 任务 | 交付 | 前置 | 验收 |
| --- | --- | --- | --- |
| 1 | 建立 v7 HTTP 对拍入口。锁定 C 版本、v6 或新增种子摘要，并列出 C／Go 路由、方法和调用方。 | — | 独立启动两份 C 核心和 C HTTP，按场景冻结状态码、关键响应头、响应结构、字节摘要及表状态。验证健康探针、错误启动、零场景、样本篡改和请求差异都能给出明确结果。 |
| 2 | 常规 Go fileserver 接入 SQLite，沿用现有可执行入口。接入数据、群组库、Searpc 及配置读取，处理启动、停止和进程就绪。 | — | 真实 Go 核心与 Go fileserver 在原位 schema 启动，实际 HTTP `/protocol-version` 可请求。缺库、错误配置或 socket 不可用有可诊断结果，SQLite 启动无 MySQL 连接。复跑已有 MySQL 启动用例。 |
| 3 | 接通真实 HTTP 的鉴权与库状态。复用已迁权限、Web 令牌、同步令牌和密钥入口。 | 1、2 | 验证账号、库、令牌用途、个人／群组／公开权限、虚拟库祖先、只读状态、过期、并发核销和撤销后下一次请求。验证 SQLite 实时状态在独立 fileserver 进程可见。 |
| 4 | 核对持续写入需要的 commit、FS 与 block 编码和持久化，复用已验证的初始库写入逻辑。 | 1 | 新写对象可被 C 加载，能复算适用版本的 ID。覆盖固定分块、多块、空文件、存量对象版本和加密块。按父提交、根目录与文件摘要核对生成对象。 |
| 5 | 接通 `/files`、`/blks` 和 `/repos/<repo>/files/<path>`。 | 3 | 对拍下载和预览、整文件和 Range、边界响应、文件名编码、空文件及大文件。验证普通库、虚拟库和已解锁加密库返回正确字节。 |
| 6 | 接通 ZIP 和文件分享链接。覆盖 `/zip`、`/f` 及锁定客户端实际使用的 ZIP 准备和进度调用。 | 5 | 用现有 Python 入口发起实际下载。逐项比对 ZIP 内路径、目录、文件摘要和所需文件名编码，核对准备、过期、取消及无权限状态。 |
| 7 | 接通已有分支的 SQLite 条件更新，明确旧 head 冲突、对象落盘、元数据提交和重试边界。 | 2、4 | 多连接更新只有符合旧 head 的写入成功。对象写入、写锁和提交故障后，引用、有权限的读取及通知状态符合约定，解除故障后可重试。用无在线 GC 表的旧库验证。 |
| 8 | 接通 SQLite 上传用量、配额和统计写入，处理 `RepoSize`、`RepoFileCount` 与统计使用的 head。 | 2、4 | 用上传前检查和桌面同步的 `/quota-check` 校验默认、显式和无限配额，验证达到限额时拒绝、虚拟库归源库所有者，以及较旧后台任务不会覆盖新 head 的统计。重启后能读出结果。 |
| 9 | 迁上传和更新的普通 HTTP 流程，先接 `/upload-api`、`/update-api` 及对应 Ajax 入口。 | 3、7、8 | 对拍新文件、替换、命名冲突、多文件、mtime、零字节、取消和重试。核对响应、head 和内容，Go 重启后可继续，C 接管能读取和更新。 |
| 10 | 接通块接口、续传与进度。覆盖 `/upload-raw-blks-api`、`/upload-blks-api`、`/update-blks-api`、`/upload_progress`、`/idx_progress`。 | 9 | 覆盖分块缺失、重复块、合法和不合法偏移、中断恢复、完成后的临时文件清理，以及现有客户端对进度和最终结果的处理。写入失败不会提前发布 head。 |
| 11 | 接通桌面同步下载和查询。覆盖权限检查、head 查询、commit、block、`fs-id-list`、`pack-fs`、缺块／缺 FS 查询。核对客户端实际使用的 `block-map`、`accessible-repos` 和 JWT。 | 3、4 | 对拍协议版本、请求／响应编码、对象帧、缺失对象、加密数据、库列表、权限拒绝及通知 JWT。客户端能从零克隆并继续增量下载。 |
| 12 | 接通桌面同步写入，覆盖发送 commit、block、`recv-fs` 与更新 head，复用 SQLite 分支更新。 | 7、8、11 | 验证快进、并发分支、冲突后的第二父提交、旧客户端重试、传输中断和重启。两个客户端修改后收敛，C 读取合并后的提交和内容。 |
| 13 | 接通虚拟库文件写入与源库合并，补齐阶段 5 遗留的非空虚拟库尺寸和文件数统计。 | 9、12 | 普通源库与虚拟库双向读写，验证嵌套目录、从源库继承权限、相邻路径隔离、合并时的源分支和 base commit 引用。C 接管后继续写入。 |
| 14 | 验证真实写请求的分支通知及事件。复用已有 `notification-server` 发送和接收代码。 | 7、9、12 | 用实际 HTTP 提交触发普通更新和合并，核对 repo ID、commit ID、事件次数与顺序、JWT 和接收端响应。对象写入失败、回滚和首次建库各有独立预期。 |
| 15 | 迁移 Seahub 主路径用到的基础文件写 RPC。优先覆盖新建空文件／目录、递归建目录、删除和改名移动，复用已验收的对象与分支写入。 | 7 | 用现有 Python 客户端对拍对应入口，覆盖目标冲突、非法路径、权限、提交关系和 C 接管。核对真实 HTTP 回归中仍调用的辅助 RPC，运行记录标明每个入口由哪个核心处理。 |
| 16 | 验收真实桌面客户端，用锁定版本启动两个独立客户端实例。 | 12–14 | 实际完成克隆、双向增量、并发冲突、断线重连以及令牌撤销后的重试。从两端读回相同文件内容，记录客户端版本、场景和观察到的分支。 |

### 任务 15 的 Seahub 调用范围

SQLite Go 核心在启用管理 RPC 并配置 ccnet 后，可在私有 socket 处理普通 v1 库的空文件、建目录、递归建目录、删除、批量删除、改名及库内移动。核心校验写权限和库状态，通过同一分支发布入口记录新提交。目录 ID、提交列表与目录项读取走 Go 核心，供 Seahub 在写前定位目录及处理重名，在写后重读元数据。兼容仓库用 Seahub 使用的 Python RPC 客户端复放这些入口，再让 C 接管同一数据库和对象目录。 [新建入口](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/file.py#L160-L214) · [重名检查](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/utils/__init__.py#L363-L376)

- 同一用户拥有的普通未加密 v1 库支持同步与后台跨库移动。锁定 Seahub 的单文件移动向同一 RPC 传不同的源、目标库，批量移动还读取后台任务 ID。Go 核心先检查两库授权、只读状态、目标配额和复制限制，再复制文件对象与数据块。源和目标的提交在同一个 SQLite 事务内发布。验收核对冲突命名、两条提交父链、失败回滚、后台任务状态和 C 接管后的文件内容。跨所有者、加密和虚拟库移动仍需分别验收。 [单文件移动](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/file.py#L350-L428) · [批量移动](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/repos_batch.py#L1290-L1351)
- 可写共享目录仍需接入。锁定 Seahub 的共享库列表带原始分享项库 ID 进入库页面，同一页面以该 ID 发起文件创建。Go 基础入口对虚拟库写调用返回错误。后续随虚拟库写入验证原库和虚拟库的分支关系、同源分享传播和 C 接管。 [共享库页面链接](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/frontend/src/pages/app/main-panel/shared-libs/item.js#L214) · [页面创建文件](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/frontend/src/pages/app/main-panel/lib-content-view/lib-content-view.js#L1429)
| 17 | 接入现有 Linux 启动方式的 SQLite Go 组合。联动 controller、核心和 fileserver 的启动参数、端口、管理开关、socket 与停止顺序。 | 5、6、10、13–16 | 默认配置可按原路径启动 C，显式选择时仅有 Go fileserver 监听目标 HTTP 端口，核心及文件服务退出后可重启。模拟启动配置错误和进程失效，验证诊断和恢复。 |
| 18 | 在兼容仓库和 CI 接入 v7 总回归，完成同数据的切换与回退验收。 | 15–17 | v1–v7 全量通过。先 C 写入，再用 Go 完成 HTTP 写入和同步，最后用 C 接管并继续上传和客户端同步。确认文件摘要、权限、head、SQLite 行、在途任务和重启结果，并记录关键吞吐、延迟和内存基线。 |

任务 1、2 可以并行。C 的 HTTP 基线就绪后，可并行做对象写入核对、鉴权及下载样本。SQLite 启动和对象约定明确后，分支更新与用量统计可以并行，下载和同步读也走各自的 worktree。上传、同步写入和基础文件 RPC 在各自前置具备后分开交付，再汇合验证虚拟库、真实客户端和部署。

## 验收方式

### 启动 SQLite Go 组合

在已有 SQLite 数据与配置目录上，将 `seafile.conf` 的 `[database] type` 设为 `sqlite`，在 `[fileserver]` 配置监听地址和端口并开启 `use_go_fileserver`。给两个进程设置同一 `JWT_PRIVATE_KEY`，使用同一服务账号。先在一个终端启动核心，等 `seafile.sock` 可用后，在另一终端启动文件服务。

```sh
"$SEAFILE_TARGET_CORE_BIN" -f -F "$CONF_DIR" -d "$DATA_DIR" -c "$CCNET_DIR" -p "$RPC_DIR"
"$SEAFILE_TARGET_FILESERVER_BIN" -F "$CONF_DIR" -d "$DATA_DIR" -c "$CCNET_DIR" -p "$RPC_DIR" -l -
```

文件服务从 `DATA_DIR/seafile.db` 与 `CCNET_DIR/GroupMgr/groupmgr.db` 读取库和群组数据，也支持从 `CCNET_CONF_DIR` 取得 ccnet 目录。实际 HTTP `GET /protocol-version` 返回版本 2 后即可执行 v7 场景。需要从核心签发持久同步令牌时，启动核心时额外传入 `--enable-management-rpcs`。

### 对拍与接管

1. **跑现有回归。** 在 Linux 按阶段 4 报告准备锁定的 C 二进制、`pysearpc` 和 v1 归档，保留 v1–v5 的验收入口。v6 用 `test-repo-create` 跑全部库写入场景。服务端现有 [上传与更新](../tests/test_file_operation/test_upload_and_update.py)、[下载响应头](../tests/test_file_operation/test_download_headers.py)、[ZIP](../tests/test_file_operation/test_zip_download.py)、[虚拟库合并](../tests/test_file_operation/test_merge_virtual_repo.py) 和 [大文件](../tests/test_file_operation/test_upload_large_files.py) 测试作为 v7 场景来源。 [现有验收命令](go-migration-sqlite-stage4-acceptance.md#复现命令)
2. **建立 v7 双实例回放。** 兼容仓库新增 `fixtures/v7` 和 `make test-fileserver`。隔离两端的数据库、对象目录、临时文件、端口和 Unix socket。C 基线明确启用自身 HTTP，Go 待测组合使用真正启动的 `seaf-server` 与 `fileserver`。每组先过 C／C，再过 C／Go。Web 接口的独立回放使用可控 Seahub 响应，最终切换用与客户端配套的真实 Seahub。能观察到的差异分别保留 C 冻结响应和 Go 预期。
3. **比较持久化和调用方结果。** HTTP 响应比较状态码、选定响应头、结构化数据及内容摘要。生成令牌和时间按关联关系及时效校验。ZIP 比包内路径和文件摘要，提交比父子关系、根目录和可达对象。写入结果核对 `Branch`、`RepoInfo`、`RepoSize`、`RepoFileCount` 及适用的虚拟库映射，并用现有 Python 和桌面客户端读取。报告记录敏感字段形状、别名或摘要。
4. **在同一份数据上演练切换。** 用 C 完成第一轮上传，停下旧组合并处理在途请求和后台任务，保留一致的 SQLite 与对象目录副本。切到 Go 后执行新的上传、同步和一次重启，再停 Go 并由 C 接管同一目录。C 需读出所有已确认的写入，并从最新 head 继续写。验收记录临时令牌重签、配额、权限、对象摘要和后台统计的接管行为。

目前的 v6 回归命令可直接执行。后两条是任务 1 要交付的 v7 命令约定。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-repo-create \
  FIXTURES_DIR=fixtures/v6 SCENARIO_SET=all \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL=go SEAFILE_TARGET_BIN="$SEAFILE_TARGET_CORE_BIN"

make -C "$SEAFILE_COMPAT_TESTS" test-fileserver \
  FIXTURES_DIR=fixtures/v7 SCENARIO_SET=all \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL=c

make -C "$SEAFILE_COMPAT_TESTS" test-fileserver \
  FIXTURES_DIR=fixtures/v7 SCENARIO_SET=all \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL=go SEAFILE_TARGET_CORE_BIN="$SEAFILE_TARGET_CORE_BIN" \
  SEAFILE_TARGET_FILESERVER_BIN="$SEAFILE_TARGET_FILESERVER_BIN"
```
