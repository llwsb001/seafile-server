# SQLite 后续迁移计划

本轮在 Linux 的 SQLite 环境交付阶段 4 的剩余能力，再接通阶段 5 的普通库、虚拟库和目录分享写入。每项分别提 PR，按表中的依赖合入。当前起点包含 Web 文件访问令牌和 v4 样本，最新默认分支已通过 v4 的 49 个 C／Go 步骤。 [服务端 PR #16](https://github.com/llwsb001/seafile-server/pull/16) · [兼容测试 PR #7](https://github.com/llwsb001/seafile-compat-tests/pull/7)

## 阶段 4 的剩余任务

| 任务 | PR 内容 | 前置 | 合入条件 |
| --- | --- | --- | --- |
| 1 | 在 `seafile-compat-tests` 建立 v5 清单和 `test-stage4`。按能力分组记录 Searpc、SQLite 表与调用方结果，沿用现有样本复制和摘要验证。 | v4 | 每组场景随对应实现前的兼容 PR 补齐。用两个隔离 C 实例逐步确认可对拍部分，并记录基线差异。 |
| 2 | 补齐普通分享查询。先迁 `seafile_list_share_repos`、`seafile_list_repo_shared_to`、`seafile_get_shared_groups_by_repo` 和 `list_inner_pub_repos`，核对 Python 调用再补齐相关列表、计数和按所有者查询。 | 1 | v3 写入后能查到对应关系。覆盖收发方向、群组 ID、权限、分页、列表排序、空结果和库元数据。 |
| 3 | 迁移管理设置的 `get_server_config_*` 与 `set_server_config_*`，接通 `SeafileConf` 的字符串、整数、64 位整数和布尔值。 | 1 | 覆盖数据库值、文件值、缺失值与类型转换。已迁移的历史保留读路径通过。Go 写入配置后，C 重启能读出同一设置。 |
| 4 | 迁移用户配额、实际用量与上传前检查。覆盖 `seafile_get_user_quota_usage`、`seafile_get_user_share_usage`、`set_user_quota`、`get_user_quota`、`check_quota` 和 `list_user_quota_usage`。 | 1、3 | 覆盖默认和显式配额、单位、无限、空库、虚拟库归属、共享用量和精确边界。验证 SQLite `UserQuota` 持久化和消费者代码的通过／拒绝。 |
| 5 | 同批迁移加密库口令检查、设置、撤销、状态和 `seafile_get_decrypt_key`。按库和用户管理进程内密钥。 | 1 | v1 加密库通过 C／Go 密钥对拍和明文摘要校验。补齐 C 支持的各加密版本样本，覆盖错误口令、用户隔离、过期、撤销和重启。 |
| 6 | 同批迁移持久库同步令牌的发放、列举，以及按令牌、账号和设备撤销。接通 `RepoUserToken`、`RepoTokenPeerInfo` 和同步消费者的失效路径。 | 1 | 覆盖新旧令牌、有无设备信息、不同账号、SQLite 事务失败后重试、重启后读取。先用独立 Go fileserver 进程加载令牌缓存，撤销成功返回后，下一次经原缓存路径的请求立即被拒绝。 |
| 7 | 补齐本地账号管理。优先迁 `add_emailuser`、`validate_emailuser`、`update_emailuser`、`update_role_emailuser`、`update_emailuser_id`、`remove_emailuser` 和管理页用到的列表。 | 1、6 | 覆盖旧密码记录校验与升级、重复账号、改名、禁用、角色、分页与 C 接管后的登录读写。对拍账号变更引起的引用和令牌状态变化。 |
| 8 | 迁移群组创建、改名、成员增删、管理员变更和删除。补齐客户端实际使用的群组读接口。 | 1、2 | 覆盖嵌套群组、祖先继承、管理员标记、删除后的分享可见性、重复操作和中途失败后的恢复。已有群组分页与授权用例通过。 |
| 9 | 迁移组织、组织成员和组织群组关系的管理入口。 | 1、7、8 | 加入两个组织共用成员的合成样本。对拍组织范围、成员与群组关系、分页，以及删除等跨库操作失败后可观察的状态。 |
| 10 | 补齐 CE 已注册的组织分享查询与变更，优先以 `org_get_shared_users_by_repo`、`get_org_group_repos_by_user` 和实际路径查询调用为入口。 | 1、2、9 | 依据 C 注册表和 Python 实际调用确认支持范围。对拍组织间隔离、组合分享结果、库与群组引用，以及空结果。 |
| 11 | 迁移 `publish_event` 和 `pop_event`。在同一核心进程按频道保存和消费事件。 | 1 | 验证 FIFO、频道隔离、单次消费、空队列、错误形状和 `ctime` 整秒。复用 Go fileserver 的现有发布调用验证。 |
| 12 | 接入 Go 核心对现有 `notification-server` 的发送路径和相应 JWT 配置，验证仓库更新事件的接收端。 | 1、6、11 | 向隔离接收端发送合成的分支更新事件，核对事件字段、认证、配置启用、过期与失败行为。后续迁移已有分支更新时，再核对实际提交触发的事件。 |
| 13 | 完成阶段 4 的 SQLite 集成回归和切换演练，输出已迁 RPC 与 Python 消费入口清单。 | 2–12 | v1–v5 全部回归通过。对快照执行 C 写入、Go 运行和写入、C 接管，覆盖权限、配额、账号关系和持久令牌。报告明确缺项、边界差异和失败恢复结果。 |

### 实现前核对的兼容约定

- 配置与配额。`quota.default` 由 C 配置管理器读取，优先检查 `SeafileConf`，再回退到 `seafile.conf`。核对 `-2` 无限配额、单位换算和 `usage + delta` 达到配额时的拒绝。虚拟库按源库所有者计费，统计时按 C 的虚拟库去重和分享用量规则对拍。 [配置读取](../common/config-mgr.c#L200) · [配额默认值](../server/quota-mgr.c#L19) · [用量判断](../server/quota-mgr.c#L402)
- 管理 RPC 默认关闭。显式传入 `--enable-management-rpcs` 后，才接入本计划新增的管理设置、持久化写入和口令、密钥、持久同步令牌接口。需要读取身份库的接口仍须提供 `-c`。只要注册上述接口，服务就必须在 socket 可连接前确保权限为 `0700`，无法保证时启动失败。可信调用方与核心使用同一服务账号。验收分别覆盖默认启动和 Web 令牌模式关闭时启用管理 RPC。已有 Web 令牌与普通库分享沿用各自开关。 [Go 启动入口](../fileserver/cmd/seafserver/main.go) · [socket 约定](go-migration-web-access-tokens.md#实现约定)
- 加密与身份。按 C 的存量格式验证加密版本、旧密码记录和必要的升级路径。对拍用合成账号与加密样本，报告只记录字段、长度、结论和明文摘要。 [加密口令](../server/passwd-mgr.c#L98) · [账户密码](../common/user-mgr.c#L330) · [fileserver 解码入口](../fileserver/fileop.go#L222)
- 同步令牌。锁定的 C 基线在按令牌和按账号删除时使用 SQLite 不支持的多表语法。用有设备记录的样本单独记录 C 的失败响应和表状态。Go 以一个事务撤销令牌并清理关联设备记录，覆盖有无设备记录两种情况。 [C 单条撤销](../server/repo-mgr.c#L1577) · [C 按账号撤销](../server/repo-mgr.c#L1857) · [SQLite 表](../scripts/sql/sqlite/seafile.sql#L22) · [fileserver 查询](../fileserver/sync_api.go#L1327)
- 事件与通知。`publish_event`／`pop_event` 按 C 队列对拍，事件体包含 `content` 字符串和 `ctime`。外部通知使用现有 `notification-server` 事件接口，记录认证、HTTP 状态和发送失败的观测，结合实际提交验证事件出现的时机。 [C 进程内队列](../common/mq-mgr.c#L34) · [C 通知发送](../server/notif-mgr.c#L91) · [Go 通知接收](../notification-server/event.go)

## 阶段 5 的第一批写入

| 任务 | PR 内容 | 前置 | 合入条件 |
| --- | --- | --- | --- |
| 14 | 在兼容仓库建立 v6 写入快照和 `test-repo-create`。用 C 生成空库、包含目录的源库、虚拟库和目录分享预期。 | 1 | 记录每步的库、分支、所有者、虚拟映射、对象 ID、分享和权限。两个 C 实例可回放，随机 ID 和时间戳按关系比较。 |
| 15 | 迁移 SQLite 普通未加密库的 `seafile_create_repo`。连接初始 commit、`Branch`、`Repo`、`RepoOwner` 和库元数据写入。 | 13、14 | Go 建库后可重启读取，C 可打开并继续写文件。模拟对象写失败和数据库提交失败，核对库、分支、所有者和元数据均未发布。首次建库完成时与 C 一致，不发送分支更新通知。 |
| 16 | 迁移基于普通源库的 `create_virtual_repo`。复用源库对象，持久化 `VirtualRepo` 映射、base commit 和虚拟分支。 | 15 | 覆盖路径规范化、同路径复用、嵌套虚拟库和并发创建。Go 生成的虚拟库能被 C 打开，源库与虚拟分支引用有效。 |
| 17 | 迁移普通源库的目录分享创建、改权和撤销，覆盖个人与群组两条链路。 | 16 | 验证相邻路径隔离、个人与群组优先级、继承及撤销。模拟分享行写入失败，首次创建不留下可见虚拟库，复用时保留原虚拟库和原有分享。通过 C 对 Go 回放、Go 重启及 C 接管，核对分享行与现有库和对象引用。 |

库写入先落盘初始对象，在一个 SQLite 事务内发布库记录、分支、所有者、库元数据及适用的虚拟库映射。首次目录分享把新增虚拟库的这些记录和分享行放入同一事务。对象写入或事务失败时，数据库引用保持原值。已经落盘的未引用对象留给后续 GC。首次建库沿用 C 的分支新增语义。分支更新通知在后续文件写入阶段，按已有分支更新成功提交后的行为验收。 [普通库创建](../server/repo-mgr.c#L3813) · [虚拟库创建](../server/virtual-repo.c#L260) · [目录分享入口](../common/rpc-service.c#L1739) · [新增分支](../common/branch-mgr.c#L174) · [分支更新通知](../common/branch-mgr.c#L364)

## 执行顺序与验收命令

任务 1 完成后，可并行推进分享读取、设置、加密密钥、同步令牌和事件。配额随后接入设置，身份和组织按表推进。任务 14 可在阶段 4 的实现期间准备。涉及共同 RPC 注册、SQLite 连接或调用方的 PR，合入前在最新依赖分支重新跑交叉场景。

阶段 4 的实际结果、C → Go → C 演练和注册清单见 [SQLite 阶段 4 集成验收报告](go-migration-sqlite-stage4-acceptance.md)。

先运行现有 v1–v4 验收入口。v5 与 v6 的入口由各自的兼容 PR 交付，按能力选择场景。C 对 C 设 `SEAFILE_TARGET_IMPL=c`，C 对 Go 设 `SEAFILE_TARGET_IMPL=go` 并传入对应二进制。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-stage4 \
  FIXTURES_DIR="$SEAFILE_FIXTURES_V5" SCENARIO_SET=quota \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL="$SEAFILE_TARGET_IMPL" SEAFILE_TARGET_BIN="$SEAFILE_TARGET_BIN"

make -C "$SEAFILE_COMPAT_TESTS" test-repo-create \
  FIXTURES_DIR="$SEAFILE_FIXTURES_V6" SCENARIO_SET=all \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL="$SEAFILE_TARGET_IMPL" SEAFILE_TARGET_BIN="$SEAFILE_TARGET_BIN"
```

服务端每项 PR 报告记录 C 提交、样本版本、SQLite 配置、执行命令、用例数、差异和重启或接管结果。调用方验收使用现有 Python 客户端，并在 Go fileserver 包内把实际消费函数接到隔离的 SQLite 连接或真实 Unix socket。文件传输和桌面同步的完整 HTTP 验收见阶段 6。 [v1 与 v2](go-migration-authorization.md#v2-场景与验收) · [v3](go-migration-share-writes.md#写入验收) · [v4](go-migration-web-access-tokens.md#验收场景)
