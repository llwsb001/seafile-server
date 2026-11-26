# SQLite 库生命周期与离线运维迁移计划

本阶段继续做 Linux SQLite。业务侧补齐 Seahub 的历史、回收站和文件复制移动，运维侧交付 `seaf-fsck`、`seafserv-gc` 以及可选用的 Go `seafile-controller`。验收以现有 Python API、桌面同步、命令行和 C 接管为准。MySQL、可选 FUSE 和正式发行进入各自后续批次。 [迁移总计划](go-migration-plan.md)

| 起点 | 提交与已验收范围 |
| --- | --- |
| 服务端 `master` | `998b2eed3994197395dbadcaac83f127d0d8a343` |
| 兼容仓库 `main` | `3b4f4feaad2a40baa8eb2ec8de381b9d70f22f89` |
| C 行为基线 | `fa46d4c43a9a23ff2f70acf92c14e6cb28955664`，沿用 v2–v7。 |
| 已验收 | v6 109 步、v7 25 组 473 步，真实 Seahub 与两个桌面客户端已在 SQLite 上完成 C → Go → C。现有 C controller 可以启动和恢复 Go 核心及 Go fileserver。 [SQLite 文件服务迁移验收](go-migration-sqlite-fileserver-acceptance.md) |

## 实现前核对的契约

- **历史与回收站。** Go 已注册 `seafile_get_commit_list` 和 `get_repo_history_limit`。先用同一提交图核对列表分页、两条父链、历史边界，以及读取历史、恢复和 GC 各自的截断时点。文件回收站通过 `get_deleted` 从历史查找删除项，整库回收站使用 `RepoTrash`，永久清理通过 `GarbageRepos` 交给 GC。恢复后哪些分享、虚拟库、同步令牌和统计可见，以锁定 C 的实际操作为依据。 [Go 历史查询](../fileserver/internal/seafserver/file_read.go) · [C RPC 注册](../server/seaf-server.c) · [C 库回收站](../server/repo-mgr.c) · [SQLite 表](../scripts/sql/sqlite/seafile.sql)
- **复制、移动与发布。** Go 已支持普通可写库的原子跨库移动和虚拟库的库内写入。复制与移动按当前 Seahub 调用区分，先确定源端所需读写权限、目标端写权限，以及 C 对加密库、虚拟库和同一对象存储的实际行为。提交前确认目标对象已落盘，源、目标 head 由条件更新保护。涉及虚拟路径时，沿用现有分支事务和持久队列，并验证最终汇合到原库。 [Go 跨库移动](../fileserver/internal/seafserver/file_cross_move.go) · [已提交文件写入队列](../fileserver/internal/seafserver/file_write_queue.go) · [C 文件操作](../server/repo-op.c) · [Python API](../python/seaserv/api.py)
- **对象保全。** SQLite 的 C GC 走离线模式。虚拟库的 commit 按逻辑库读取，FS 和 block 归原库存储，回收候选要覆盖源库、共享视图及其 base commit。锁定 C 的 `-D` 禁止对象删除，但扫描初始化可能推进 `RepoValidSince`。在 C 副本上冻结这个副作用，Go 的 dry-run 要保持数据库逻辑行和对象清单不变。C 使用 Bloom filter，比较时保留 C 统计，同时用可达集合和真实执行前后的对象清单判断差异。 [C GC](../server/gc/gc-core.c) · [Go 对象存储](../fileserver/objstore/objstore.go) · [C 完整性检查](../server/gc/verify.c)

## 任务与合入条件

一项能力一个正式 PR，并在兼容仓库为该能力补场景。按前置依赖使用独立 worktree。每个 PR 附上锁定版本、实际命令、通过场景和可复现的差异。

| 任务 | 交付 | 前置 | 验收 |
| --- | --- | --- | --- |
| 1 | 在兼容仓库交付 `fixtures/v8`、`test-sqlite-lifecycle` 和 `test-sqlite-maintenance`。复用快照摘要、Python RPC、真实进程和版本锁。 | — | C/C 可独立回放。缺少归档、校验不符、命令意外退出或空场景均失败。CLI 冻结参数、退出码和有意义的输出，并保留对象及 SQLite 状态。 |
| 2 | 校准现有提交列表与历史保留逻辑，迁移 `set_repo_history_limit` 和 `set_repo_valid_since`。核对 `clean_up_repo_history` 的实际调用和副作用。 | 1 | 对拍默认值、无限、仅 head、有限天数、分页和合并提交。用明确越过截断边界的样本核对历史展示与维护保留规则。 |
| 3 | 补齐 Seahub 历史页读取，优先迁 `seafile_diff`、`seafile_list_file_revisions` 和历史提交下的文件、目录查询。 | 1、2 | Python 查看文件旧版本和两次提交的差异。核对增删改、改名、空文件、虚拟路径和旧对象缺失时的结果。 |
| 4 | 迁移文件回收站列表 `get_deleted`。 | 2、3 | 核对删除后重建、递归删除、同名历史项、时间窗口、翻页及排序。Seahub 能打开记录所指向的历史版本，有效权限变化后重新判断。 |
| 5 | 迁移 `seafile_revert_file`、`seafile_revert_dir`。 | 3、4 | 对拍空文件、整目录、同名冲突和无效历史引用。提交失败保持原 head。恢复后重启，检查内容摘要、统计、事件及虚拟视图。 |
| 6 | 迁移整库回滚 `seafile_revert_on_server`。 | 2、3 | 核对回滚生成的提交、父引用及当前可见内容。验证并发上传、只读或越权拒绝，以及 C 从回滚后的 head 继续写。 |
| 7 | 迁移 `seafile_destroy_repo` 的软删和整库回收站查询。 | 1 | 对拍用户、管理员列表和分页。核对可见性、分享、设备令牌、虚拟映射和待处理文件写任务，确认可恢复对象仍可读取。 |
| 8 | 迁移 `restore_repo_from_trash`。 | 7 | 对拍恢复的库 ID、原 head、所有者、大小和可见权限。旧 head 缺失或写事务失败时保留可重试状态。两端交替删除、重启、恢复并继续同步。 |
| 9 | 迁移 `del_repo_from_trash`、按用户或全局清空及 C 对应的过期处理。 | 7、8 | 核对 `RepoTrash` 与 `GarbageRepos` 的变更、时间边界和权限。重复清空与中途事务失败结果可验证，物理回收由任务 17、18 验收。 |
| 10 | 迁移普通库的 `seafile_copy_file`，复用既有跨库移动的对象复制和任务生命周期。 | 1 | 对拍同库和跨库复制、覆盖与重名、只读源库、目标配额、前后台结果及取消。检查源 head、目标提交和文件摘要。 |
| 11 | 支持 C 与 Seahub 实际允许的加密库复制或移动组合，并冻结其余组合的拒绝行为。 | 10 | 对拍锁定、解锁、密钥过期和不同库密钥。核对明文摘要及目标块标识，验证错误口令、落盘失败或取消时两端 head 的状态。 |
| 12 | 补齐涉及虚拟库的跨库复制与移动。 | 10 | 覆盖同源不同视图、跨原库、嵌套、权限继承和相邻路径隔离。校验原库、虚拟 head、base commit 和统计，验证跨库失败时两端引用的一致性。 |
| 13 | 交付 Go `seaf-fsck` 的默认检查、`-s`、`-S`、线程和库选择。 | 1 | 用真实 CLI 对拍健康库、缺失和损坏的 commit、FS 与块。区分只查存在性和校验内容，核对返回码、库选择及对输入数据的零写入。 |
| 14 | 接通 `seaf-fsck -E` 导出。 | 13 | 对拍目录结构、名称、空文件和多块文件摘要。冻结损坏库与加密库的实际导出行为，核对输出范围和源数据保持原状。 |
| 15 | 接通 `seaf-fsck -r` 修复。 | 2、13 | 在停服副本中对拍可恢复的 head、替换的目录项和修复后的内容。注入对象写入与数据库提交故障，检查失败后引用、重复修复、重启和 C 接管。 |
| 16 | 交付 Go `seafserv-gc -C` 完整性验证和 `-D` 回收候选扫描。 | 2、13 | 比较无限、有限、仅 head 历史的保留集合，覆盖合并提交、虚拟 base commit、可恢复整库、孤立对象和缺失对象。验证 dry-run 零写入及 C 的已知差异。 |
| 17 | 接通 Go `seafserv-gc` 的离线块回收。 | 9、16 | 在停服副本执行，真实删除集合可与候选解释逐项核对，所有有效可达块可读。模拟部分删除失败后复跑，核对 `RepoValidSince` 与仍可访问的历史。 |
| 18 | 补齐 `seafserv-gc -R` 和 `-r`，验证 FS 回收与已删除库存储清理。 | 9、17 | `-D` 各组合对数据库和存储零写入。真实执行核对各类对象清单、`GarbageRepos`、仍存引用及重复执行。删除失败的清理项可用下一次运行继续。 |
| 19 | 迁移 Linux SQLite 的 `seafile-controller`。核对安装产物的名称、参数、环境变量和管理脚本调用。 | 1 | 用现有 `test-fileserver-controller` 对拍前后台启动、PID、日志、私有 socket、HTTP 就绪、子进程分别故障后的恢复和停止顺序。核对 C 回退及配置启用的 SeafDAV 入口。 |
| 20 | 集成回归、同数据接管及 CI。兼容仓库锁定 v8 样本和 Go 候选，保留 v1–v7 回归。 | 2–19 | 实际 Seahub 与两个客户端通过旧功能和恢复用例。至少一份存量数据经历 C 写入、Go 写入与离线维护、C 接管并继续写。核对 CLI、权限、历史、对象、SQLite 表和 CI 结果。 |

任务 1 可与各条线的 C 合约核对并行。样本入口就绪后，历史与恢复、整库回收站、复制移动、fsck 和 controller 可同时开工。GC 候选可提前用只读样本验证，真实删除在历史保留、回收站和完整性规则通过后合入。涉及共享 RPC 注册、分支发布和对象层的 PR，在最新依赖上补跑交叉场景。

## 验收方式

### 复跑已合入基线

在隔离 Linux 环境使用 [锁定的 C 构建](../baseline/BUILD_NOTES.md) 和相邻兼容仓库。先跑 v1–v7，保留各组对拍数量和已经冻结的差异，再跑新增场景。现有 v7 CI 接收已推送的服务端完整提交 SHA，下面两条命令分别复验默认基线和指定候选。真实 Seahub、双客户端、controller 和对象检查继续使用现有 v7 入口。 [现有复现入口](go-migration-sqlite-fileserver-acceptance.md#复现入口) · [v7 参数](https://github.com/llwsb001/seafile-compat-tests/blob/3b4f4feaad2a40baa8eb2ec8de381b9d70f22f89/fixtures/v7/README.md)

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-fileserver-ci
SQLITE_CI_GO_REVISION="$SEAFILE_GO_REVISION" SQLITE_CI_GO_SUITE=all \
  make -C "$SEAFILE_COMPAT_TESTS" test-fileserver-ci
```

### 新增 v8 的运行约定

任务 1 交付下列入口。C/C 传 `SEAFILE_TARGET_IMPL=c`，C/Go 用下列参数指向实际编译的二进制。涉及 HTTP 或实际客户端的场景沿用 v7 的独立进程和端口。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-sqlite-lifecycle \
  FIXTURES_DIR=fixtures/v8 SCENARIO_SET=all SEAFILE_TARGET_IMPL=go \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_CORE_BIN="$SEAFILE_TARGET_CORE_BIN" \
  SEAFILE_TARGET_FILESERVER_BIN="$SEAFILE_TARGET_FILESERVER_BIN"

make -C "$SEAFILE_COMPAT_TESTS" test-sqlite-maintenance \
  FIXTURES_DIR=fixtures/v8 SCENARIO_SET=all SEAFILE_TARGET_IMPL=go \
  SEAFILE_BASELINE_FSCK_BIN="$SEAFILE_BASELINE_FSCK_BIN" SEAFILE_BASELINE_GC_BIN="$SEAFILE_BASELINE_GC_BIN" \
  SEAFILE_TARGET_FSCK_BIN="$SEAFILE_TARGET_FSCK_BIN" SEAFILE_TARGET_GC_BIN="$SEAFILE_TARGET_GC_BIN"
```

### 分阶段检查同一数据

1. **准备和冻结。** 为 C/C、C/Go 分别复制 SQLite 与对象目录。加入普通、加密、虚拟、回收站库存量，以及合并提交、旧历史、空文件、大文件和孤立对象。人为缺失、损坏和故障注入各用独立副本。记录 SHA-256、对象 ID、角色、时间边界和预期引用。
2. **先验只读，再验写入。** 查询、检查、导出和 dry-run 各比较 SQLite 逻辑行、对象清单及校验摘要。修复和 GC 删除在停止核心、fileserver 及其它写入方后的隔离副本执行。先观察候选，再执行修复或回收，检查所保留的提交、FS、块和整库回收站对象。可回收对象仍因 C Bloom filter 保留时单独记差异。
3. **验证失败与重试。** 测试落盘、SQLite 写锁、发布前并发变更和临时文件中断。比较 `Branch`、`RepoInfo`、`RepoTrash`、`GarbageRepos`、`RepoHistoryLimit`、`RepoValidSince`、`VirtualRepo` 和适用的令牌、统计、队列。CLI 另记录退出码、选中库、保留或清理的对象和可重试项。
4. **用消费者验收。** 真实 Seahub 执行历史查看、文件恢复和整库恢复。两个客户端在 Go 重启后读取恢复内容并继续增量写入。维护后以同一份数据启动 C，验证保留历史、权限、内容、统计和下一次浏览器上传及客户端同步。记录候选版本、各回放步骤数、已冻结的差异和未覆盖的组合。
