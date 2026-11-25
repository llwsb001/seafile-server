# SQLite 阶段 4 集成验收与切换演练

阶段 4 的 Go 核心通过了 SQLite v1–v5 集成验收。v5 有 541 步 C／Go 回放，其中 520 步与 C 一致，21 步按已记录的 Go 行为校验。同一份冻结快照完成了 C 写入、Go 读写和重启、C 接管并继续写入的 21 步演练。这些结果覆盖核心的 Searpc 和数据兼容。Go 文件服务的完整 SQLite 启动、文件传输和桌面同步仍需按后续阶段验收。 [SQLite 后续迁移计划](go-migration-sqlite-next.md)

## 验收基线

| 输入 | 已测试版本 |
| --- | --- |
| C 核心 | `fa46d4c43a9a23ff2f70acf92c14e6cb28955664`，Linux arm64，SQLite。 [C 提交](https://github.com/llwsb001/seafile-server/commit/fa46d4c43a9a23ff2f70acf92c14e6cb28955664) · [构建说明](../baseline/BUILD_NOTES.md) |
| Go 核心及消费者 | `3a5adc3961da7f13cb077e141c60d576c58d42cf`，试验分支 `codex/sqlite-stage4-integration`。 [服务端集成提交](https://github.com/llwsb001/seafile-server/commit/3a5adc3961da7f13cb077e141c60d576c58d42cf) |
| 兼容样本与回放工具 | `3c19a0700642a7d05ae7e41c466cb55662f2a37a`，试验分支 `codex/sqlite-stage4-fixture-integration`。 [兼容集成提交](https://github.com/llwsb001/seafile-compat-tests/commit/3c19a0700642a7d05ae7e41c466cb55662f2a37a) · [v5 场景规范](https://github.com/llwsb001/seafile-compat-tests/blob/3c19a0700642a7d05ae7e41c466cb55662f2a37a/fixtures/v5/README.md) |

两条集成分支固定了可复现的 PR 组合。回放验证归档和数据库摘要，为每个角色复制独立运行目录和 Unix socket。C 使用 `[database] type = sqlite` 及样本的标准或云模式配置，Go 使用同一份配置。v5 根据场景打开普通分享写入或管理 RPC，并记录同一次调用的原始响应、Python 解码和指定的 SQLite 表状态。随机令牌使用别名，生成时间按秒或微秒校验后用占位符记录。回放使用合成凭据，敏感字段在冻结响应中以字段形状、长度、摘要或别名记录。 [回放入口](https://github.com/llwsb001/seafile-compat-tests/blob/3c19a0700642a7d05ae7e41c466cb55662f2a37a/tests/compare_stage4.py)

## 回归结果

默认分支集成前使用 Linux ARM64 重新运行了 v5 和 v6。v5 的 C／C 543 步全部一致，C／Go 541 步全部符合预期，另有 2 步只回放 C。v6 的 C／C 和 C／Go 各 41 步零差异，覆盖普通建库、虚拟库复用、嵌套目录权限的改权、重启、撤销及 C 接管。 [兼容样本整合 PR #23](https://github.com/llwsb001/seafile-compat-tests/pull/23)

常规 Go fileserver 启动仍使用 MySQL。同步令牌撤销的 SQLite 消费验收使用独立测试进程，常规启动与实际部署切换列入阶段 6。 [后续验收](go-migration-sqlite-next.md)

| 样本 | 已验证结果 |
| --- | --- |
| v1 对象与读取 | 8 个块、78 个 FS 对象和 27 个提交校验无差异。C／Go 读 RPC 41 步零差异，覆盖普通库、虚拟库、加密库和路径错误。 [v1 验收](go-migration-plan.md) |
| v2 身份与授权 | 66 步零差异。身份 37、权限 21、云模式 4、撤销快照 4。 [v2 验收](go-migration-authorization.md#v2-场景与验收) |
| v3 普通分享写入 | 64 步零差异。个人 21、群组 17、公开 13、云模式公开 13。包括写锁失败后重试、Go 重启和 C 接管。 [v3 验收](go-migration-share-writes.md#写入验收) |
| v4 Web 文件访问令牌 | 49 步零差异。生命周期 44、带前导零的 TTL 3、环境变量启用 2。另验证默认关闭、其他本地账号无法连接私有 socket，以及八个连接并发消费一次性令牌。 [v4 验收](go-migration-web-access-tokens.md#验收场景) |
| v5 集成 | C／C 543 步零差异。C／Go 541 步均符合预期，其中 21 步验证已记录的差异。另有 2 步专门冻结 C 的 SQLite 同步令牌撤销失败。 [v5 冻结样本](https://github.com/llwsb001/seafile-compat-tests/tree/3c19a0700642a7d05ae7e41c466cb55662f2a37a/fixtures/v5) |

| v5 场景组 | C／Go 一致 | Go 单独校验 | 仅 C | 主要验收内容 |
| --- | ---: | ---: | ---: | --- |
| `baseline` | 4 | 0 | 0 | 快照及进程边界 |
| `shares` | 73 | 0 | 0 | 个人收发、群组、公开库、分页、库元数据、重启和接管 |
| `config` | 55 | 0 | 0 | `SeafileConf` 优先于文件值、类型、缺值和 C 重启 |
| `quota` | 47 | 0 | 0 | 默认值、字节单位、无限、用量及共享用量、精确拒绝边界 |
| `encryption` | 70 | 0 | 0 | 六个合成库、加密格式 1–4、旧 magic、PBKDF2、Argon2id 和明文摘要 |
| `sync-tokens` | 14 | 0 | 2 | 旧设备记录、新签发、列表、按设备撤销、重启和 C 接管 |
| `accounts` | 34 | 0 | 0 | 存量口令升级、状态、角色、分页、改名引用和删除 |
| `groups` | 77 | 0 | 0 | 嵌套群组、祖先继承、管理员、成员分页和删组后的分享权限 |
| `organizations` | 64 | 10 | 0 | 两个组织共用成员、分页、角色、群组关系和跨库失败 |
| `org-shares` | 36 | 11 | 0 | 组织隔离、个人和群组路径组合、空结果、组织群组关联写入后的可见性 |
| `events` | 25 | 0 | 0 | FIFO、频道隔离、单次消费、空队列及 `content` 和 `ctime` |
| `handoff` | 21 | 0 | 0 | 同快照 C → Go → C |
| 合计 | 520 | 21 | 2 | |

### 同快照接管

1. C 创建本地账号、设置库的只读分享、写入库所有者配额并发放持久同步令牌，共 4 步。
2. Go 使用同一份数据库和对象目录。Go 验证口令、权限、配额与旧令牌，提高配额和分享权限，修改账号角色并签发第二枚令牌，共 9 步。
3. Go 重启后复查上传配额、两枚令牌和分享权限，共 3 步。
4. C 接管这份数据，读取账号角色、配额与两枚令牌，撤销更新后的分享并确认权限消失，共 5 步。 [接管场景 PR #18](https://github.com/llwsb001/seafile-compat-tests/pull/18)

### 实际消费者与失败恢复

| 路径 | 验证 |
| --- | --- |
| Go fileserver Web 令牌消费 | 使用真实 Unix Searpc 调用现有令牌解析入口，读取库 ID、对象 ID、操作和使用者，失效令牌触发 `403`。 [消费测试](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/web_access_token_rpc_test.go) |
| Go fileserver 上传配额 | 管理 RPC 经 Unix Searpc 写入隔离 SQLite 后，实际 `checkQuotaByContentLength` 读取数据库并使用新限制，覆盖允许、到达限额和数据库值变化。 [配额消费测试](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/quota_rpc_test.go) |
| Go fileserver 解密 | `parseCryptKey` 经 Unix Searpc 获取 v1 加密库的密钥和 IV，并按明文 SHA-256 验证密文块。v5 另用 OpenSSL 校验四种格式的合成样本。 [解密消费测试](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/repo_password_rpc_test.go) · [加密样本](https://github.com/llwsb001/seafile-compat-tests/pull/15) |
| Go fileserver 同步令牌 | 独立 Go 测试进程用 SQLite 注入现有 HTTP handler，先让现有请求入口预热旧令牌缓存。SQLite 路径在每次请求中检查数据库的权威记录。核心通过 Unix Searpc 撤销后，下一次请求立即被拒绝。带设备和无设备令牌均覆盖，删除设备行失败时事务回滚，解除故障后可重试。 [同步消费者测试](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/sync_token_revocation_test.go) |
| 进程内发布与外部通知 | 实际 fileserver 事件发布函数经 Unix Searpc 写入 Go 核心。外部通知单独验证 Go 核心发送入口、合成库和提交 ID、真实 `notification-server` 接收路由、JWT 签名和到期、HTTP 失败及取消。 [发布消费测试](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/event_rpc_test.go) · [通知实现和联调记录 PR #26](https://github.com/llwsb001/seafile-server/pull/26) |
| SQLite 多表失败恢复 | 普通分享在写入或提交遇锁时回滚。账号改名最后一张表更新失败时，账号及库、群组和令牌引用均回滚。群组建删故障保留事务前状态，解除故障后重试成功。组织关系在跨库注入故障后也能回滚。 [分享写入](https://github.com/llwsb001/seafile-compat-tests/pull/6) · [账号](https://github.com/llwsb001/seafile-server/pull/25) · [群组](https://github.com/llwsb001/seafile-server/pull/24) · [组织](https://github.com/llwsb001/seafile-server/pull/28) |

### 可观测差异与切换边界

- **同步令牌。** 锁定 C 对单令牌及按账号撤销使用 SQLite 不支持的多表删除。两个仅 C 的步骤冻结了失败响应和保留的令牌与设备行。Go 在一个事务中删除配套记录，并让同步消费者重新检查 SQLite。 [同步令牌样本 PR #13](https://github.com/llwsb001/seafile-compat-tests/pull/13)
- **组织。** C 在 SQLite 的组织库直接访问群组表，使部分列表返回空。C 删除组织可能删除其他组织仍引用的全局账号与群组，跨库故障还可能留下部分状态。10 个步骤分别校验 Go 的列表、共享引用保留与回滚。C 的解除组织群组关联存在 SQLite 参数绑定问题，Go 用真实 Unix Searpc 单独验证。 [组织实现 PR #28](https://github.com/llwsb001/seafile-server/pull/28) · [组织样本 PR #20](https://github.com/llwsb001/seafile-compat-tests/pull/20)
- **组织分享。** C 的组织群组库列表漏用 `org_id`，组织群组路径查询引用错误的表名。Go 按组织、库归属和群组关系查询。11 个步骤验证完整的 Go 返回结果，包括合法命中、隔离、空结果和动态群组关联。CE 注册表提供组织分享读入口，Python 的组合收件人方法仍依赖 CE 未注册的 `get_org_groups_by_repo`。组织专用写方法同样仅在客户端声明。 [实现 PR #29](https://github.com/llwsb001/seafile-server/pull/29) · [样本 PR #19](https://github.com/llwsb001/seafile-compat-tests/pull/19)
- **历史库元数据与旧客户端行为。** 缺少 `RepoInfo` 时，分享列表从主分支提交补齐响应，Go 只读连接不会回填数据库缓存。锁定 SQLite C 的账号 active/inactive 列表过滤及从群组库排除组织的 `get_top_groups(0)` 在冻结场景返回空，Go 保留对应结果。删组会撤销成员的分享授权，`RepoGroup` 行由调用方通过 `seafile_remove_repo_group` 清理。 [分享读取 PR #20](https://github.com/llwsb001/seafile-server/pull/20) · [账号 PR #25](https://github.com/llwsb001/seafile-server/pull/25) · [群组 PR #24](https://github.com/llwsb001/seafile-server/pull/24)
- **加密与进程内状态。** Go Argon2id 支持最多 255 lanes，超出的存量参数返回口令错误。库密钥和 Web 文件令牌沿用进程内生命周期，重启后需要重新解锁或签发。持久同步令牌随数据库接管。 [加密实现 PR #23](https://github.com/llwsb001/seafile-server/pull/23) · [Web 文件令牌约定](go-migration-web-access-tokens.md#实现约定)
- **启动与传输。** 管理 RPC 默认关闭，接入身份库需要 `-c`。管理模式和 Web 文件令牌模式都在 socket 可连接前校验 `0700`，可信调用方使用同一服务账号。既有普通库分享写入由 `--enable-share-writes` 控制，也需要 `-c`。通知发送入口已验证合成分支更新。Go 核心在实际分支提交后触发通知，以及完整上传下载、Range、ZIP、桌面同步和生产切换仍由后续阶段验收。 [启动入口](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/cmd/seafserver/main.go) · [迁移总计划](go-migration-plan.md)

## 复现命令

在 Linux 上分别检出表中的两个集成提交。令 `SEAFILE_SERVER_DIR`、`SEAFILE_COMPAT_TESTS`、`SEAFILE_BASELINE_BIN` 和 `SEAFILE_TARGET_BIN` 指向对应检出目录及可执行文件的绝对路径。让 Python 可以导入锁定版 `pysearpc`，让动态链接器可以加载 C 基线库。v5 加密回放需提供 `openssl`。在兼容检出中准备 v1 的冻结归档。 [C 构建说明](../baseline/BUILD_NOTES.md) · [v1 存储归档](https://github.com/llwsb001/seafile-compat-tests/blob/3c19a0700642a7d05ae7e41c466cb55662f2a37a/fixtures/v1/README.md)

```sh
go -C "$SEAFILE_SERVER_DIR/fileserver" build -o "$SEAFILE_TARGET_BIN" ./cmd/seafserver
make -C "$SEAFILE_COMPAT_TESTS" verify-fixtures FIXTURES_DIR=fixtures/v1
```

v1 和 v2 的 RPC 入口接收已经运行的 C、Go socket。v1 启动两份归档副本。v2 每组用 `tests/compare_permissions.py fixtures/v2 <场景组> prepare <新目录>` 为两边准备各自的快照及配置。每组分别启动 C 和 Go。C 的启动环境需设置 `JWT_PRIVATE_KEY=fixture-only-key`。按 `-F`、`-d`、`-c`、`-p` 提供配置、数据、ccnet 和 socket 目录。令 `C_RPC_SOCKET` 和 `GO_RPC_SOCKET` 指向当前组的 socket。v2 依次运行 `identity`、`permissions`、`cloud`、`revoked`。 [v2 准备及运行说明](go-migration-authorization.md#v2-场景与验收)

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-api FIXTURES_DIR=fixtures/v1 \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" C_RPC_SOCKET="$C_RPC_SOCKET" GO_RPC_SOCKET="$GO_RPC_SOCKET"

make -C "$SEAFILE_COMPAT_TESTS" test-permissions FIXTURES_DIR=fixtures/v2 SCENARIO_SET="$SCENARIO_SET" \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" BASELINE_RPC_SOCKET="$C_RPC_SOCKET" TARGET_RPC_SOCKET="$GO_RPC_SOCKET"
```

v3–v5 的回放入口自行准备目录和运行服务。v5 将 `SEAFILE_TARGET_IMPL` 改为 `c` 可以复验 C／C，指定 `SCENARIO_SET=handoff` 可以单跑接管。 [兼容仓库 Makefile](https://github.com/llwsb001/seafile-compat-tests/blob/3c19a0700642a7d05ae7e41c466cb55662f2a37a/Makefile)

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-share-writes FIXTURES_DIR=fixtures/v3 SCENARIO_SET=all \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL=go SEAFILE_TARGET_BIN="$SEAFILE_TARGET_BIN"

make -C "$SEAFILE_COMPAT_TESTS" test-web-tokens FIXTURES_DIR=fixtures/v4 \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL=go SEAFILE_TARGET_BIN="$SEAFILE_TARGET_BIN"

make -C "$SEAFILE_COMPAT_TESTS" test-stage4 FIXTURES_DIR=fixtures/v5 SCENARIO_SET=all \
  SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL=go SEAFILE_TARGET_BIN="$SEAFILE_TARGET_BIN"
```

Go 消费者和回滚用例通过下列命令复验。先把 v1 归档解包到 `SEAFILE_FIXTURES_DATA` 指向的数据目录，使解密用例实际执行。全模块 `go test -p 1 ./...` 中，现有 `repomgr` 和 `searpc` 外部集成用例因环境缺少其 C 服务和数据库而失败，其余包通过。 [Go fileserver 测试](https://github.com/llwsb001/seafile-server/tree/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver)

```sh
SEAFILE_FIXTURES_V1="$SEAFILE_COMPAT_TESTS/fixtures/v1" SEAFILE_FIXTURES_DATA="$SEAFILE_FIXTURES_DATA" \
  go -C "$SEAFILE_SERVER_DIR/fileserver" test -p 1 ./ ./cmd/seafserver ./internal/seafserver ./internal/repocrypt ./utils

go -C "$SEAFILE_SERVER_DIR/notification-server" test ./... -count=1
```

## RPC 注册及 Python 消费入口

集成版本在 `seafserv-threaded-rpcserver` 下支持 131 个不同的 RPC 名称，含 v1–v4 已有入口。所有名称都匹配 `SeafServerThreadedRpcClient` 的装饰方法。表里列出实际传到 socket 的名称以及常用的 Python 包装方法。名称中的 `seafile_` 按注册表保留，包装方法可能省略前缀。 [Go 注册入口](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/cmd/seafserver/main.go) · [Python wire 声明](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/python/seafile/rpcclient.py)

| 能力和启动条件 | Searpc wire 名称 | Python 消费入口 |
| --- | --- | --- |
| 库、提交和路径，默认 | `seafile_get_repo`、`seafile_get_commit`、`seafile_list_dir_by_path`、`seafile_get_file_id_by_path`、`seafile_get_repo_owner`、`seafile_is_repo_owner`、`get_repo_status`、`get_repo_history_limit` | `SeafileAPI.get_repo`、`get_commit`、`get_file_id_by_path`、`get_repo_owner`、`is_repo_owner`、`get_repo_status`、`get_repo_history_limit`。目录列表由 `seaserv.service.list_dir_by_path` 调用。 |
| 普通分享读取，默认 | `seafile_list_share_repos`、`seafile_list_repo_shared_to`、`get_shared_users_by_repo`、`seafile_list_repo_shared_group`、`seafile_get_shared_groups_by_repo`、`seafile_get_group_repoids`、`get_group_repo_owner`、`seafile_get_repos_by_group`、`get_group_repos_by_owner`、`repo_has_been_shared`、`list_inner_pub_repos`、`list_inner_pub_repos_by_owner`、`count_inner_pub_repos`、`is_inner_pub_repo` | `SeafileAPI.get_share_in_repo_list`、`get_share_out_repo_list`、`list_repo_shared_to`、`list_repo_shared_group_by_user`、`get_shared_group_ids_by_repo`、`get_repos_by_group`、`get_group_repos_by_owner`、`get_inner_pub_repo_list` 等。 |
| 授权及用户群组库，`-c` | `check_permission`、`check_permission_by_path`、`get_group_repos_by_user` | `SeafileAPI.check_permission`、`check_permission_by_path`、`get_group_repos_by_user`。 |
| 个人路径与组织直接收件人，默认 | `get_shared_repo_by_path`、`org_get_shared_users_by_repo` | `SeafileAPI.get_shared_repo_by_path`、`seafserv_threaded_rpc.org_get_shared_users_by_repo`。 |
| 群组路径及组合转换，`-c` | `get_org_group_repos_by_user`、`get_group_shared_repo_by_path`、`convert_repo_path` | `SeafileAPI.get_org_group_repos_by_user`、`get_group_shared_repo_by_path`、`convert_repo_path`。 |
| 普通分享写入，`-c` 与 `--enable-share-writes` | `seafile_add_share`、`set_share_permission`、`seafile_remove_share`、`seafile_group_share_repo`、`set_group_repo_permission`、`seafile_group_unshare_repo`、`set_inner_pub_repo`、`unset_inner_pub_repo` | `SeafileAPI.share_repo`、`set_share_permission`、`remove_share`、`set_group_repo`、`set_group_repo_permission`、`unset_group_repo`、`add_inner_pub_repo`、`remove_inner_pub_repo`。 |
| 配额用量与上传检查，默认。分享用量需要 `-c` | `seafile_get_user_quota_usage`、`check_quota`、`seafile_get_user_share_usage` | `SeafileAPI.get_user_self_usage`、`check_quota`、`get_user_share_usage`。 |
| 配额管理，`--enable-management-rpcs` | `get_user_quota`、`set_user_quota`、`list_user_quota_usage` | `SeafileAPI.get_user_quota`、`set_user_quota`、`list_user_quota_usage`。 |
| 服务端设置，`--enable-management-rpcs` | `get_server_config_string`、`get_server_config_int`、`get_server_config_int64`、`get_server_config_boolean`、`set_server_config_string`、`set_server_config_int`、`set_server_config_int64`、`set_server_config_boolean` | `SeafileAPI` 同名方法。 |
| 库解锁，`--enable-management-rpcs` | `seafile_check_passwd`、`seafile_set_passwd`、`seafile_unset_passwd`、`seafile_is_passwd_set`、`seafile_get_decrypt_key` | `SeafileAPI.check_passwd`、`set_passwd`、`unset_passwd`、`is_password_set`、`get_decrypt_key`。 |
| 持久同步令牌，`--enable-management-rpcs` | `seafile_generate_repo_token`、`seafile_delete_repo_token`、`seafile_list_repo_tokens`、`seafile_list_repo_tokens_by_email`、`seafile_delete_repo_tokens_by_peer_id`、`delete_repo_tokens_by_email` | `SeafileAPI.generate_repo_token`、`delete_repo_token`、`list_repo_tokens`、`list_repo_tokens_by_email`、`delete_repo_tokens_by_peer_id`、`delete_repo_tokens_by_email`。 |
| 身份基本查询，`-c` | `get_emailuser`、`get_org_by_id`、`org_user_exists` | `CcnetAPI` 同名方法。 |
| 本地账号管理，`-c` 与 `--enable-management-rpcs` | `add_emailuser`、`remove_emailuser`、`validate_emailuser`、`update_emailuser`、`update_role_emailuser`、`update_emailuser_id`、`get_emailuser_with_import`、`get_emailuser_by_id`、`get_emailusers`、`search_emailusers`、`get_superusers`、`count_emailusers`、`count_inactive_emailusers`、`get_emailusers_in_list` | `CcnetAPI` 同名管理和列表方法。 |
| 群组查询，`-c` | `get_group`、`get_groups`、`get_group_members`、`get_all_groups`、`get_ancestor_groups`、`get_top_groups`、`get_child_groups`、`get_descendants_groups`、`list_all_departments`、`get_members_with_prefix`、`check_group_staff`、`search_groups`、`search_group_members`、`get_groups_members`、`is_group_user` | `CcnetAPI.get_group_members`、`get_groups`、`get_ancestor_groups`、`check_group_staff` 等。部门列表可从 `ccnet_threaded_rpc.list_all_departments` 直接调用。 |
| 群组管理，`-c` 与 `--enable-management-rpcs` | `create_group`、`remove_group`、`group_add_member`、`group_remove_member`、`group_set_admin`、`group_unset_admin`、`set_group_name`、`quit_group`、`remove_group_user`、`set_group_creator`、`seafile_remove_repo_group` | `CcnetAPI` 的群组写方法，及 `SeafileAPI.remove_group_repos`、`remove_group_repos_by_owner`。 |
| 组织管理及列表，`-c` 与 `--enable-management-rpcs` | `create_org`、`remove_org`、`get_all_orgs`、`count_orgs`、`get_org_by_url_prefix`、`get_orgs_by_user`、`get_org_emailusers`、`add_org_user`、`remove_org_user`、`is_org_staff`、`set_org_staff`、`unset_org_staff`、`set_org_name`、`add_org_group`、`remove_org_group`、`is_org_group`、`get_org_id_by_group`、`create_org_group`、`get_org_groups`、`get_org_top_groups`、`get_org_groups_by_user` | `CcnetAPI` 的组织与成员方法。组织群组按用户查询可从 `ccnet_threaded_rpc.get_org_groups_by_user` 直接调用。 |
| 进程内事件，默认 | `publish_event`、`pop_event` | `SeafileAPI.publish_event`、`pop_event`。 |
| Web 文件访问令牌，Go fileserver 模式 | `seafile_web_get_access_token`、`seafile_web_query_access_token` | `SeafileAPI.get_fileserver_access_token`、`query_fileserver_access_token`。 |

外部通知走 `POST /events`，使用 `Authorization: Token` 及 HS256 JWT。启用需要 `ENABLE_NOTIFICATION_SERVER=true` 和 `INNER_NOTIFICATION_SERVER_URL`，Go 核心启动时检查 `JWT_PRIVATE_KEY`。Go 核心复用现有 fileserver 的发送实现。 [发送入口](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/fileserver/internal/seafserver/notification.go) · [接收路由测试](https://github.com/llwsb001/seafile-server/blob/3a5adc3961da7f13cb077e141c60d576c58d42cf/notification-server/event_test.go)
