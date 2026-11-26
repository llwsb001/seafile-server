# Linux SQLite 下一阶段功能计划与验收

下一阶段补齐锁定 Seahub、桌面客户端和运维工具仍会用到的 SQLite 能力。优先接通管理后台、带权限的目录浏览、库内搜索和加密库操作。计划包含 26 项功能、1 项契约核对和 1 项集成验收。每项功能可以单独验收并提正式 PR。 [迁移总计划](go-migration-plan.md) · [上一阶段验收](go-migration-sqlite-stability-acceptance.md)

## 起点与范围

| 项目 | 当前起点 |
| --- | --- |
| 服务端 `master` | `be7f2c4669777db4a874e82c9e56c040bfebd2a3`，已合入 Go SQLite 持锁读取修复和复验记录。 [服务端 #84](https://github.com/llwsb001/seafile-server/pull/84) · [服务端 #85](https://github.com/llwsb001/seafile-server/pull/85) |
| 兼容仓库 `main` | `0f7560c7f5dfd3258e77bb12869967d171cd495b`，已合入 Go 独立持续负载入口。合并前 v7、v8 回归通过，合并候选 `315e07b` 通过 v9 和原生 amd64 持续负载。 [兼容测试 #70](https://github.com/llwsb001/seafile-compat-tests/pull/70) · [合并候选验收](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45758686986) |
| C 基线与调用方 | C 源码及锁定提交 `fa46d4c43a9a23ff2f70acf92c14e6cb28955664` 保持原样。沿用锁定的 Seahub、桌面客户端、SeafDAV 和 libsearpc，测试副本继续支持 C 接管。 [现有发行安装契约](go-migration-sqlite-install-contract.md) |
| 发行状态 | Go 独立持续负载和同数据回退已有通过记录。锁定 C 持续负载基线仍未完成，C/Go 相对性能与发行签收单独保留待通过。 [稳定性验收记录](go-migration-sqlite-stability-acceptance.md#签收状态) |

初步静态清点从锁定 C 的核心注册表中找到 213 个不同 RPC 名称，其中 35 个在 Go 核心的聚合注册入口没有同名项。这是候选清单。任务 1 核对 SQLite 下的实际可用性、进程启动选项和真实消费者，再冻结返回值与持久化行为。本阶段使用 Linux SQLite，沿用现有对象格式、共享表和私有 Searpc socket。 [C RPC 注册](../server/seaf-server.c) · [Go 核心启动注册](../fileserver/cmd/seafserver/main.go) · [Python RPC 封装](../python/seaserv/api.py)

## 任务、依赖与验收

「前置」是验收和合入顺序。能从现有接口和独立样本启动的任务可并行开发。功能 PR 带定向 C/C、C/Go 回放或 Go 运维用例。实现侧复用已有的仓库读取、权限、对象发布和私有 socket 入口。

### 管理后台与库治理

| 任务 | 功能 | 前置 | 验收 |
| --- | --- | --- | --- |
| 1 | 冻结候选 RPC 与真实调用方。按管理后台、普通用户、桌面、内部工具标记入口。 | — | 从锁定 C 的实际 SQLite 进程调用候选 RPC，比对 Go 安装模式。记录参数、返回、错误、排序、分页及调用文件。把仅注册、SQLite 可用和实际消费者在用的入口标清。 |
| 2 | 管理后台全库列表与库数，接通 `seafile_get_repo_list`、`seafile_count_repos`。 | 1 | 用真实管理员打开非空、多页和空列表。对拍排序、页边界、虚拟库选项以及旧库缺 `RepoInfo` 的结果。普通用户请求管理路由被拒绝。 |
| 3 | 后台按库名或 ID 前缀搜索，接通 `seafile_search_repos_by_name`、`seafile_get_repos_by_id_prefix`。 | 1、2 | 用同名库、大小写和 Unicode、重合的名字与 ID 前缀检查结果和分页。通过 Seahub 后台搜索入口验证。 |
| 4 | 库占用空间与孤儿库清单，接通 `seafile_server_repo_size`、`seafile_get_orphan_repo_list`。 | 1 | 对拍正常库、虚拟库、缺统计及缺所有者的旧库。记录返回值及排序，确认启停和读取过程保留对象与库引用。 |
| 5 | 系统统计，接通 `get_total_file_number`、`get_total_storage`。 | 1、4 | 真实后台显示统计。用普通、虚拟、已删除和统计落后样本确认 C 的聚合口径。用上传、删除及重启验证数值追上当前 head。 |
| 6 | 库改名与描述，接通 `seafile_edit_repo`。 | 1 | 走真实管理后台和群组库入口，核对新提交、旧历史、元数据、远端列表及通知。覆盖无权限、并发写入、失败回滚和 C 接管后继续改名。 |
| 7 | 资料库转让，接通 `seafile_set_repo_owner`。 | 1 | 走真实 Seahub 转让调用。验证新旧所有者、共享、群组库及虚拟引用的实际 C 语义。核对已有令牌、配额、失败后重试和 C 接管。 |
| 8 | 管理库状态，接通 `set_repo_status`。 | 1 | 对拍状态值、未知库、旧统计行及状态恢复。由真实管理员改变状态，对 Web、同步和启用的 DAV 核对各入口的实际读写结果。 |
| 9 | 补齐库访问属性读写，接通 `seafile_repo_set_access_property`、`seafile_repo_query_access_property`。 | 1 | 在锁定 SQLite 上确认 `public`、`own`、`private` 及无值的实际存储和效果。核对非法值、权限边界和重启后的持久状态。 |
| 10 | 系统默认库查询，接通 `get_system_default_repo_id`。 | 1 | 用有记录、缺记录、指向已删除库的副本走 Seahub 管理入口。核对现有 `SystemInfo` 行的读取行为和首次安装的真实结果。 |

后台列表、搜索、改名和状态都有锁定消费者。空间和统计入口先核对 C 返回的数据来源，再复用 Go 的已有代码。 [Seahub 后台资料库](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/admin/libraries.py) · [后台系统统计](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/admin/sysinfo.py) · [管理后台权限入口](../fileserver/internal/seafserver/permission.go)

### 浏览、搜索和分享

| 任务 | 功能 | 前置 | 验收 |
| --- | --- | --- | --- |
| 11 | 带权限的目录浏览，接通 `list_dir_with_perm`。 | 1 | 真实 Seahub 浏览普通库和嵌套虚拟库，核对条目类型、大小、权限字段和分页。覆盖只读分享、未授权路径、目录 ID 与路径不一致，以及同源不同子树。 |
| 12 | 文件与目录大小，接通 `seafile_get_file_size`、`seafile_get_dir_size`。 | 1 | 对拍空目录、嵌套目录、多块文件、缺对象和虚拟库使用原库对象的行为。通过真实文件详情或复制前配额检查验证。 |
| 13 | 文件最近修改时间，接通 `seafile_calc_files_last_modified`。 | 1 | 用修改、移动、重命名、同名重新创建及有历史裁剪的样本核对 C 的提交遍历范围、时间单位、缺项返回及顺序。 |
| 14 | 库和子目录内按名称搜索，接通 `search_files`、`search_files_by_path`。 | 1、11 | 用锁定 Seahub 搜索入口验收 `search_files`。对 `search_files_by_path` 单独跑定向 C/C、C/Go RPC 对拍，用相邻子树中的同名文件验证结果范围。覆盖匹配规则、数量、顺序、Unicode、空查询、虚拟库投影和无权限路径。 |
| 15 | 文件名校验，接通 `seafile_is_valid_filename`。 | 1 | 对拍空名、非法分隔符、受配置影响的忽略规则、Unicode 和边界长度。用真实创建或重命名入口确认通过和拒绝结果。 |
| 16 | 通过所有者、原库和路径定位虚拟库，接通 `get_virtual_repos_by_owner`、`get_virtual_repo`。 | 1 | 两个用户共享同一个源路径时核对不同虚拟库身份，覆盖嵌套路径、空查找、路径迁移后的查找及 C 接管。 |
| 17 | 子目录个人与群组分享明细，接通 `seafile_get_shared_users_for_subdir`、`seafile_get_shared_groups_for_subdir`。 | 1、16 | 在 Seahub 分享弹窗和管理界面核对发起者、接收者、权限及空列表。验证路径改名、分享撤销、嵌套虚拟库和不同用户的返回范围。 |
| 18 | 分支诊断查询，接通 `seafile_branch_gets`。 | 1 | 核对分支名称、head、旧库及未知库输出，验证实际维护调用与 CLI 观察一致。 |
| 19 | 虚拟库 base commit 管理，核对并接通 `set_base_commit` 的受支持调用。 | 1、16、18 | 先在锁定 SQLite 找到真实维护调用或可复现的兼容用途。若交付写入，覆盖缺对象、未知库和普通库拒绝，以及重启、队列、原库投影和 C 接管。若用途未确认，在矩阵记录条件和证据。 |

目录详情和复制移动会调用目录权限列表。分享查询和虚拟库定位会进入分享弹窗、后台及通知相关调用。 [Seahub 目录查询](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/dir.py) · [库内搜索](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/search_file.py) · [目录分享明细](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/dir_shared_items.py)

### 加密库、文件导入和运维

| 任务 | 功能 | 前置 | 验收 |
| --- | --- | --- | --- |
| 20 | 生成加密库初始化参数，接通 `generate_magic_and_random_key`。 | 1 | 冻结 C 支持的加密版本、入参和错误。随机输出核对格式、长度及用对方实现解开和验证的结果。使用合成口令，测试日志记录字段存在和验证结果。 |
| 21 | 兼容客户端提交现有密钥材料建库，接通 `seafile_create_enc_repo`。 | 1、20 | 使用锁定 Seahub 或兼容客户端的真实创建请求，验证现有版本的库头、盐和客户端解密。覆盖重复 ID、字段损坏、失败后无半发布库，以及 Go 写入后用 C 解密。 |
| 22 | 加密库修改密码，接通 `seafile_change_repo_passwd`。 | 1、20、21 | 真实 Seahub 改密后用新密码读写和重新克隆，核对旧密码、缓存、历史与已有客户端的实际契约。对拍错误旧密码、并发文件写入与事务失败。 |
| 23 | 加密库口令摘要算法迁移，接通 `seafile_upgrade_repo_pwd_hash_algorithm`。 | 1、20、22 | 用锁定 C 确认受支持的算法、参数和版本。检查文件对象和历史摘要、凭据验证、重启及 C 接管后继续读写。 |
| 24 | 私有 RPC 单文件暂存导入，接通 `seafile_post_file`。 | 1、12、15 | 先冻结允许读取的暂存目录和源文件归属规则。用现有 Seahub 内部调用或同契约样本导入真实暂存文件，分别校验目标库写权限和源文件读权限。源文件校验失败时 head 保持不变。核对文件字节、统计、配额、通知、SQLite 提交失败及虚拟目录投影。 |
| 25 | 私有 RPC 批量暂存导入，接通 `seafile_post_multi_files`。 | 1、24 | 沿用任务 24 的目标及源文件权限规则，对每项输入验证源文件范围和归属，任一源文件校验失败时整批不发布新 head。核对 C 的文件清单、覆盖和失败边界，验证同名、并发及重试结果，并让 C 接管同一数据继续写。 |
| 26 | 文件块清单与缺块诊断，接通 `seafile_list_file_blocks`、`seafile_check_repo_blocks_missing`。 | 1、12 | 在只读副本核对多块、分页、重复和缺失块，覆盖普通库、虚拟库和损坏对象。将 RPC 结果和已装 fsck 的观察对齐。 |
| 27 | 已安装服务的只读状态入口，计划增加 `seafile-sqlite status`。 | — | 复用现有 PID、实际可执行路径和就绪检查。验证停止、运行及子进程异常，记录 queue 是否可观测，核对执行前后数据库、PID 文件和运行进程一致。 |
| 28 | 新增 v10 的组合验收与交付记录。运行既有 v1–v9 及持续负载，并用实际消费者把本阶段功能串成工作流。 | 2–27 | v1–v8 按同一 Go 源码 SHA 回归。每个架构的 v9、v10 和 Go 持续负载使用该架构的同一份发行归档，并核对 SHA-256。记录 C/C、C/Go 的步骤数和差异，验证安装、改动、重启、接管与继续写入。条件项记录验证结果和交付边界。 |

锁定 Seahub 已调用预生成密钥材料建库、改密码及暂存文件导入。Go 可复用已有的密码派生、口令缓存和文件发布事务。 [建库请求](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/views.py) · [改密请求](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/repo_set_password.py) · [Go 口令派生](../fileserver/internal/seafserver/repo_password_derive.go) · [Go 文件写入](../fileserver/internal/seafserver/file_write.go) · [现有启停入口](../release/sqlite/service.py)

## 并行顺序

1. 首批并行冻结任务 1 的调用边界，并开始 2、4、6、7、8、10、11、12、13、15、16、18、20、27 的定向样本和实现。每条线用独立 worktree 和 SQLite 副本。共享注册表或共用测试运行器的改动按依赖串行合入。
2. 后续按表接通依赖的 3、5、14、17、19、21–26。跨工作线调用已公布的输入和输出。涉及分支发布的写入复用既有事务、对象写入和队列，分别验证失败和 C 接管。
3. 用兼容仓库逐个版本保留新样本和断言。v10 按功能逐批加入，最后用任务 28 的源码 SHA 和归档口径验收。尚未合并的依赖可以用已推送的完整 SHA 验收，PR 中注明依赖和各自运行结果。

## 集成验收

- **后台与普通用户。** 管理员开全库列表、搜索、改名、转让及切换库状态。普通用户借助共享目录浏览、搜索及退出分享访问。记录真实 HTTP 返回、RPC 参数、SQLite 行和文件摘要。
- **加密库与写入。** 用锁定客户端创建加密库、改密码并重新克隆，覆盖多块文件和历史读取。从真实暂存目录导入文件后让两个客户端同步。短暂持锁时仍校验已确认上传的文件可回读。
- **停服与接管。** 记录 head、统计、权限、令牌及虚拟库 base commit。停写并排空 Go 队列，重启后核对内容及历史，再以固定 C 接管同一份数据继续写入。用实际安装的 fsck 核对引用。涉及删除和损坏的测试使用独立副本。
- **发布候选。** 原生 amd64 跑完整矩阵。原生 arm64 复验候选安装、新旧数据、关键新增链路及 C 回退。变更后的候选用同一归档执行 Go 三轮持续负载，每轮至少 180 秒。报告保留两仓库完整 SHA、归档 SHA-256、实际进程路径、失败证据和原始输出。C/Go 相对性能仍按现有独立门槛签收。 [稳定性验收方式](go-migration-sqlite-stability.md)

下面的命令沿用已有 v7、v8、v9 和 Go 独立负载入口。`SEAFILE_GO_REVISION` 指已推送的同一 Go 完整 SHA。v7、v8 各自从该提交构建，v1–v6 按兼容仓库各版说明复跑同一源码版本。最后一条命令在新的 `SEAFILE_RELEASE_OUTPUT` 目录构建一次归档，复用于 v9 和 Go 持续负载。任务 28 完成时追加 v10 命令，直接使用这份归档及其 SHA-256。

```sh
SQLITE_CI_GO_REVISION="$SEAFILE_GO_REVISION" SQLITE_CI_GO_SUITE=all \
  make -C "$SEAFILE_COMPAT_TESTS" test-fileserver-ci
SQLITE_CI_GO_REVISION="$SEAFILE_GO_REVISION" \
  make -C "$SEAFILE_COMPAT_TESTS" test-sqlite-lifecycle-ci
SQLITE_CI_GO_REVISION="$SEAFILE_GO_REVISION" SQLITE_CI_GO_LOAD=1 \
  bash "$SEAFILE_COMPAT_TESTS/ci/test-sqlite-release.sh" "$SEAFILE_RELEASE_OUTPUT"
```
