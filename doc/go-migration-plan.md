# Seafile 服务端 C → Go 迁移计划

## 交付目标

用 Go 交付 `seaf-server`、`seafile-controller`、`seafserv-gc`、`seaf-fsck` 和可选的 `seaf-fuse`，复用现有 Go `fileserver` 和 `notification-server`。首轮在 Linux 验收 MySQL 和 SQLite 部署。其他发行目标以现有安装包和实际消费者为准。

现有部署应能用原配置、数据库和 `seafile-data/storage` 启动 Go 版本。Seahub、桌面客户端及 WebDAV 使用现有协议。C 版本应能直接读取并继续操作 Go 写过的数据。安装方依赖的二进制名称、参数、环境变量、PID 文件和信号行为都纳入验收。

## 基线与测试数据

- C 源码基线锁在 `6b13248efe65bf743883998050197dd80839843b`。SQLite 的构建和运行方式见 [基线说明](../baseline/BUILD_NOTES.md)，历史测试结果记录在兼容仓库的 `results/baseline-report.md`。主仓库的 [CI](../ci/run.py) 目前测试 MySQL 下的 C、Go 两种文件服务器搭配 C 核心。
- 兼容仓库 `llwsb001/seafile-compat-tests` 的 `fixtures/v1` 提供 SQLite 数据库、`storage.tar` 和预期对象清单。完整样本含 8 个块、78 个 FS 对象和 27 个 commit，覆盖普通库、加密库和虚拟库。归档由验收环境提供，CI 用固定的 SHA-256 校验，缺包、校验和错误或检查到零个对象都应失败。新增场景使用新的样本版本。
- 兼容仓库的 Python 校验器检查 C 样本的对象 ID、原始内容摘要和目录树。验收 Go 读路径时，从实际 SQLite `Branch` 表取 head，通过 `VirtualRepo` 解析对象归属，并调用生产 Go 包读取对象。样本的 `repos.json` 用于对照。
- 每轮比较先让 C 和 Go 各自使用一份相同数据库及存储快照。保存步骤、输出和可重现差异。确定性结果逐值比较，随机 ID 和时间戳用可控输入或比较引用关系。

阶段 1 合入并准备好完整 v1 样本后，在兼容仓库运行对象层基线。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-data IMPL=c-baseline FIXTURES_DIR="$SEAFILE_FIXTURES_V1"
```

`SEAFILE_COMPAT_TESTS` 指向兼容仓库，`SEAFILE_FIXTURES_V1` 指向包含 `storage.tar` 的 v1 目录。验收预期是 113 个对象通过、78 个 FS 对象的规范化 JSON 通过，并能从 SQLite 打开普通库的目录树。加密样本的明文验收随 Go 密钥和解密功能接入。

通过对象层基线后，将归档解到独立的临时目录。将 `SEAFILE_FIXTURES_V1` 和 `SEAFILE_FIXTURES_DATA` 分别设为 v1 目录和包含已解包 `storage/` 的目录，在 `fileserver/` 运行 `go test ./internal/seafserver -run '^TestReadFixturesV1$' -count=1 -v`。Go 验收应从 SQLite 读取三个业务库，遍历 15 个目录和 8 个文件，核对 27 个 commit 和文件引用的块。

在 `fileserver/` 运行 `go build -o "$SEAFILE_GO_SERVER" ./cmd/seafserver` 可构建只读 Searpc 服务。将样本数据库放在解包后的数据目录中，文件名为 `seafile.db`。准备含 `[database]` 和 `type = sqlite` 的 `seafile.conf`，用 `-f -d 数据目录 -F 配置目录 -p socket目录` 启动 Go 服务。对同一快照的两个独立副本启动 C 和 Go 后，在兼容仓库传入两边的 `seafile.sock` 运行 `make test-api`。

## PR 顺序和验收条件

各阶段可以拆成多个 PR。每个 PR 说明依赖及所覆盖的功能，并附上可复现的验收命令和实际结果。存在前置 PR 时，后续 PR 可以以前置分支为基线，合并时仍按依赖顺序进行。

| 顺序 | 工作 | 合入条件 |
| --- | --- | --- |
| 1 | 在兼容仓库固化 v1 验收入口和归档摘要。把 `make test-data` 接入独立校验，给后续生产读路径留出真实执行入口。 | 完整样本检查到 113 个对象。缺少或改动归档时返回失败。目标执行失败可传递给 CI。 |
| 2 | 在现有 Go 模块中复用 `commitmgr`、`fsmgr`、`blockmgr` 和 `objstore`。接通 `Branch → commit → FS → block`，用数据版本和库归属选择读路径。 | 从 v1 SQLite 找到普通库、虚拟库和加密库的 head。目录和文件结果与基线一致，校验空文件、空目录、历史提交、多块文件和源库对象。现有 `fileserver` 的调用方通过回归。 |
| 3 | 实现 Go Searpc 服务端和 `seaf-server` 命令，先迁 `seafile_get_repo`、`seafile_get_commit`、`seafile_list_dir_by_path` 和 `seafile_get_file_id_by_path`。 | 用现有 Python 客户端分别连接 C、Go 的 `seafile.sock`。比较成功、缺失及非法参数的响应，包含字段、`null`、列表排序、整数精度和错误码。测试覆盖连接复用。 |
| 4 | 分批迁用户、群组、组织、分享、权限、配额、下载令牌、解密密钥与事件。已合入 [身份与库权限读取](go-migration-authorization.md)、[普通库分享写入](go-migration-share-writes.md) 和 [Web 文件访问令牌](go-migration-web-access-tokens.md)。余项见 [SQLite 后续迁移计划](go-migration-sqlite-next.md)。 | 现有 RPC 回归通过。覆盖权限允许和拒绝、令牌过期与核销、持久化设置、缓存及通知事件。配置和环境变量的解析与优先级符合 C 基线。 |
| 5 | 迁仓库和文件写入、回滚、回收站、历史、固定分块、CDC、加密、并发合并和后台任务。SQLite 建库和目录分享见 [第一批写入](go-migration-sqlite-next.md#阶段-5-的第一批写入)。 | 落盘对象后按旧 head 条件更新 `Branch`。对拍并发、重试和中断后的对象与数据库状态，验证 `GCID`、`LastGCID` 及虚拟库合并。新样本覆盖冲突提交的第二父提交、支持的加密版本和旧对象版本。 |
| 6 | 用现有 Go `fileserver` 接替 C HTTP。先按 [SQLite 文件服务迁移计划](go-migration-sqlite-fileserver.md) 完成 SQLite 启动、传输、同步及切换。 | MySQL、SQLite 都通过 HTTP 测试和真实桌面客户端的双端同步。检查 `/update-blks-api`、上传进度、Range、文件名编码、大文件、配额和错误响应。 |
| 7 | 迁 `seafserv-gc`、`seaf-fsck`、`seafile-controller` 和可选 FUSE。 | GC 和 fsck 先跑校验、导出及 dry-run，在隔离副本验证修复和删除。核对候选对象、可达对象和修复后的 head。验证 controller 启停与重启，以及 FUSE 挂载、列目录和文件读取。 |
| 8 | 发行演练、灰度和构建清理。先按 [SQLite 发行候选与切换演练计划](go-migration-sqlite-release.md) 交付可选择启用的 Linux SQLite 候选。 | 两种数据库分别完成 C 写入 → Go 读写 → C 读写。比较相同负载下的同步延迟、吞吐和内存，合入前记录基线及允许偏差。确认安装产物消费者后，清理本仓库中已被替换的 C、Vala 和 Autotools 构建入口。 |

在 [Searpc 注册表](../server/seaf-server.c)、[C HTTP 路由](../server/http-server.c)、[Go HTTP 路由](../fileserver/fileserver.go) 和 [安装规则](../lib/Makefile.am) 中查找阶段 3 至 8 的契约。已有 [Python 集成测试](../tests) 提供库操作、权限、回收站、虚拟库和 GC 的回归用例。端到端测试由现有 Python 客户端、Seahub 和桌面客户端调用被测服务。

SQLite 文件服务验收后的任务、依赖和复现命令见 [SQLite 库生命周期与离线运维迁移计划](go-migration-sqlite-lifecycle-maintenance.md)，覆盖剩余的阶段 5 库与文件操作及阶段 7 的离线工具和 controller。

SQLite 发行回放后的探针排查、Go 持续负载和签收条件见 [SQLite 稳定性计划](go-migration-sqlite-stability.md)，延续阶段 8 的性能与持续运行验收。

稳定性验收后，管理后台、权限浏览、搜索、加密库、文件导入及运维的后续交付见 [SQLite 下一阶段功能计划](go-migration-sqlite-functional-coverage.md)。

v10 后的虚拟库维护、加密库、默认模板及普通用户搜索见 [SQLite 剩余兼容项计划](go-migration-sqlite-remaining-compat.md)。

## 写入切换与回退

1. 灰度前，演练一次从一致的数据库和存储快照启动 C，再切到 Go 写入，最后用同一份数据启动 C。验证历史、权限、文件内容和新写入。
2. 每份数据在切换时只由一个核心实现处理写入。切换前处理在途请求和后台任务，按基线验证临时令牌等进程内状态的行为。
3. 如果差异影响对象、权限、同步或数据完整性，停止 Go 核心，再用已验证的 C 发行物接管同一份数据。数据库结构和对象格式的兼容性由前面的双向读写测试保证。
4. 完成灰度、回退演练和构建产物检查后，结束 C 运行时的过渡支持。由发布负责人签收数据库和工具矩阵，并记录最终使用的 C 基线及验收结果。
