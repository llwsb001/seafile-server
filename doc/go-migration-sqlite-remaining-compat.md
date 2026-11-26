# Linux SQLite 剩余兼容项计划与验收

本阶段补齐虚拟库 base commit 管理、首次启动的默认模板库、服务端生成密钥的加密建库、加密目录虚拟库，以及 Seahub 普通用户搜索。任务编号接续上一阶段。交付范围限于 Linux SQLite。验收继续使用原 C 源码、冻结样本和锁定版本。 [迁移总计划](go-migration-plan.md) · [上一阶段计划](go-migration-sqlite-functional-coverage.md)

服务端起点为 `e5f6c6dfc26a6f37606b24b132521504176816a3`，兼容仓库 `main` 起点为 `e568cbc1e6b9d842f22bf4462f902975943bdde4`。兼容仓库 #72 提供 v10 验收入口，使用锁定 C `fa46d4c43a9a23ff2f70acf92c14e6cb28955664` 和锁定 Seahub `62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab`。#72 对应的 CI 已通过原生 amd64、arm64 的 v10 和 Go 独立持续负载。 [v10 验收 PR](https://github.com/llwsb001/seafile-compat-tests/pull/72) · [双架构 CI](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45772749090)

按块上传和更新继续作为已完成回归项。锁定 C 已停用 `seafile_post_file_blocks` 和 `seafile_put_file_blocks`，实际使用的 `/upload-blks-api`、`/update-blks-api` 已接入 Go，通过 v7 的缺块、提交和更新测试。 [C 注册表](../server/seaf-server.c#L166) · [Go 块提交](../fileserver/fileop.go#L3612) · [块传输 PR](https://github.com/llwsb001/seafile-server/pull/43)

## 任务 29｜接通虚拟库 base commit 管理

在私有 Searpc socket 注册 `set_base_commit`，复用现有 `VirtualRepo` 写入逻辑。现有 GC 用例先用 40 个零暂缓虚拟库合并，再恢复原库提交。以该用例和锁定 C 的实际返回为准，冻结已有提交、缺失的非零提交、全零值、格式错误、未知库和普通库的契约。 [C RPC](../common/rpc-service.c#L4342) · [GC 调用方](../tests/test_gc/test_gc.py#L295) · [Go 写入方法](../fileserver/repomgr/repomgr.go#L560)

- 在独立 SQLite 副本通过 Python RPC 写入全零值并重启，验证 base commit、虚拟库路径和两边 head 的状态。恢复真实提交并排空队列后，核对原库和虚拟库投影。
- 复现原库删除文件后仍由虚拟库 base commit 引用的场景。运行 GC 后确认文件块仍可读取，并用锁定 C 接管该副本继续写入。无效调用后核对数据库和队列状态。

## 任务 30｜首次启动创建默认系统库

接通 Go SQLite 启动时的默认模板库初始化，复用普通库创建和文件写入。锁定 C 创建由 `System` 拥有的 `My Library Template`，写入 `SystemInfo.default_repo_id`，并从 `library-template` 导入模板内容。先通过安装入口确认模板目录缺失或为空、以及指针指向已删除库时的实际行为。 [C 启动逻辑](../server/seafile-session.c#L620) · [Go 查询入口](../fileserver/internal/seafserver/repo_admin.go#L81) · [冷启动对拍](https://github.com/llwsb001/seafile-server/pull/94)

- 分别用全新实例、带嵌套模板文件的新实例、带有效模板库的存量实例和带失效指针的实例启动。用锁定 Seahub 的系统库管理入口读取模板库，核对所有者、文件摘要和默认库指针。
- 重启后核对有效模板库 ID、head 和管理员已有的模板改动。模拟初始化中断并重试，检查默认库指针恢复可用，核对重试前后的系统库记录，避免重复创建。用锁定 C 接管同一数据，继续读取和修改模板。

## 任务 31｜支持服务端生成密钥的加密建库

接通 `seafile_create_repo` 的密码分支，复用 Go 的密钥派生和加密建库发布事务。锁定 Seahub 对 `POST /api2/repos/` 提交 `passwd` 的请求走该分支，当前实测 C 返回 HTTP 200，Go 返回 520。以锁定 Seahub 默认的加密版本 2 起步，并对拍 Go 和冻结 C 支持的版本 3、4 及口令摘要配置。 [Seahub 调用](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/views.py#L1191) · [C 建库实现](../server/repo-mgr.c#L3953) · [Go 建库实现](../fileserver/internal/seafserver/repo_create.go#L32)

- 用真实 Seahub 仅提交密码建库，经锁定桌面客户端解锁、写入多块文件并重新克隆。核对随机字段格式、正确与错误密码、文件摘要和重启后读写。用锁定 C 接管 Go 写过的数据。
- 覆盖不支持的参数和写入失败，校验失败后数据库中没有可见的半成品库。复跑普通建库、客户端提交密钥材料建库、改密和摘要迁移。

## 任务 32｜支持从加密目录新建虚拟库

接通 `create_virtual_repo` 的加密分支，复用密码校验和虚拟库发布事务。按锁定 C 的契约继承原库的加密版本、适用的盐与包装密钥，并用虚拟库 ID 派生校验材料。真实 Seahub 的 `GET /api2/repos/<id>/dir/sub_repo/` 当前实测 C 返回 HTTP 200，Go 返回 500。 [Seahub 调用](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/views.py#L4102) · [C 加密元数据](../server/virtual-repo.c#L72) · [Go 创建路径](../fileserver/internal/seafserver/virtual_repo.go#L50)

- 分别用已有加密库和任务 31 新建的加密库验证。用锁定桌面客户端单独同步该目录，核对文件可解密、对象来自原库、相邻目录不出现在视图中。验证一次视图写入及原库投影，重启后由锁定 C 接管并继续读写。
- 覆盖缺密码、错误密码、无路径权限、重复请求与并发创建，核对返回值、虚拟库映射唯一性和队列状态。保留普通目录虚拟库回归。

## 任务 33｜修复 Seahub 普通用户搜索的 SQLite 路径

在 Seahub 单独交付 SQLite 适配，让 `GET /api/v2.1/search-file/` 从正确的 SQLite 数据源读取个人及群组隐藏路径。锁定 Seahub 的冷缓存路径会读取缺失的 `db_name`，后续查询还使用跨库表名。先用锁定副本复现两种核心下的 HTTP 500，再用单独检出的 Seahub 修复候选分别搭配锁定 C 和 Go 验收。兼容仓库记录候选的完整 SHA 和验证方式，保留现有版本锁供复现。 [搜索路由](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/search_file.py#L53) · [隐藏路径查询](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/utils/db_api.py#L658) · [既有对拍](https://github.com/llwsb001/seafile-server/pull/103)

- 用真实普通账号验证冷缓存、热缓存、空结果、隐藏目录及其后代、虚拟库边界和无访问权限。两种核心上均核对返回路径及顺序，隐藏路径及其后代始终从结果中排除，越权请求返回 403。
- 复跑库令牌搜索和核心 RPC。Seahub 修复候选通过验证后，在报告记录其交付位置。部署仍使用锁定 Seahub 时，普通用户搜索保留待修状态。

## 任务 34｜组合验收和交付

1. 任务 29、30、31 和 33 可独立并行。任务 32 先用已有加密库开发，待任务 31 合入后跑组合链路。各项使用独立 worktree 和 SQLite 副本，按依赖逐项验收并提正式 PR。Seahub 修复与服务端改动分别提交。
2. 任务 29–33 的定向回归通过后，从同一 Go 完整 SHA 复跑 v1–v10 及本阶段新增场景。原生 amd64 和 arm64 各构建一份发行归档，同一架构复用同一 SHA-256 做安装、功能回放、同数据 C 接管和 Go 三轮持续负载。每轮至少 180 秒，沿用已有负载及收敛条件。新增验收单独保留锁定 Seahub 与修复候选的结果。
3. 报告记录服务器、兼容仓库和 Seahub 候选的完整 SHA、归档 SHA-256、实际进程路径、通过步骤、差异和原始输出。C 持续负载与相对性能继续按既有发行门槛签收。 [v10 运行入口](https://github.com/llwsb001/seafile-compat-tests/pull/72) · [持续负载与签收条件](go-migration-sqlite-stability.md)

v10 和 Go 持续负载使用兼容仓库 #72 提供的入口。`SEAFILE_GO_REVISION` 为已推送的服务端完整 SHA，`SEAFILE_RELEASE_OUTPUT` 为新的结果目录。v1 冻结归档及其余历史场景按各自的样本说明准备。

```sh
SQLITE_CI_GO_REVISION="$SEAFILE_GO_REVISION" SQLITE_CI_V10=1 SQLITE_CI_GO_LOAD=1 \
  bash "$SEAFILE_COMPAT_TESTS/ci/test-sqlite-release.sh" "$SEAFILE_RELEASE_OUTPUT"
```
