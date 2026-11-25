# 阶段 4 第一批 · 身份与库权限读取

本批把 SQLite 只读服务扩展到用户、群组、组织成员关系和库权限查询。交付标志是现有 Python 客户端连接 Go 的 `seafile.sock`，对同一份 C 生成的数据得到一致的结论。后续的分享写入、令牌签发与核销、解密密钥、配额及事件继续按 [迁移总计划](go-migration-plan.md) 分批交付。

## 起点

- 首批四个 SQLite 只读 RPC、库所有者／状态／历史保留 RPC 及其对拍均已合入。后续实现以两个仓库的默认分支为基线。 [只读 RPC](https://github.com/llwsb001/seafile-server/pull/6) · [库属性 RPC](https://github.com/llwsb001/seafile-server/pull/7) · [库属性对拍](https://github.com/llwsb001/seafile-compat-tests/pull/4)
- v1 有 3 条 `EmailUser` 记录和 1 条 `SharedRepo` 记录。`Group`、`GroupUser`、`Organization`、`RepoGroup` 和 `InnerPubRepo` 为空。本批在兼容仓库新增 v2，v1 继续跑存储和原有只读 RPC 回归。 [样本规则](https://github.com/llwsb001/seafile-compat-tests/blob/main/fixtures/SPEC.md)

## PR 顺序

| 批次 | 仓库与改动 | 合入条件 |
| --- | --- | --- |
| 1 | `seafile-compat-tests` 新建 `fixtures/v2` 和 `test-permissions`。用锁定的 C 版本和 RPC 生成身份、成员关系及分享数据，并记录逐场景的 C 响应。 | 两个 C 实例用独立副本对拍通过。提交场景清单、基线版本、配置、数据库和归档摘要。缺少样本、摘要错误、场景未执行或出现差异时退出非零。 |
| 2 | `seafile-server` 接通 `-c`／`--config-file` 指向的 SQLite 身份库。先迁 `get_emailuser`、`get_group`、`get_groups`、`get_group_members`、`is_group_user`、`get_org_by_id` 和 `org_user_exists`。 | 用现有 Python 客户端对拍对象字段、缺失值、错误和列表顺序。覆盖群组祖先查询、成员判断、分页和组织成员隔离。指定 `-c` 时，缺少身份库的诊断能定位对应文件。使用 `-f -d` 的 v1 启动路径也纳入回归。 |
| 3 | `seafile-server` 接入 `check_permission` 和 `check_permission_by_path`。由读库和群组查询支持一套库权限判定，再把结果映射为 Searpc 响应。 | 对拍库所有者、个人分享、群组分享、内部公开、虚拟库路径和撤销前后的结果。比较 `r`、`rw`、`null` 和 RPC 错误。v1 对象与 RPC 回归继续通过。 |

兼容仓库的测试入口按当前快照与配置选择场景，并连接两个 Unix socket。批次 1 先用它验证 C 基线，批次 2、3 再用它验证 C 与 Go。每个实现 PR 附上实际执行的用例数和差异结论。

## 实现边界

- 当前 Go 命令从 `-d` 打开主库。本批按 C 的目录约定从 `-c` 读取 `PeerMgr/usermgr.db`、`GroupMgr/groupmgr.db` 和 `OrgMgr/orgmgr.db`。把读库连接传给业务查询，主库与身份库各自按只读打开。身份查询和权限判定共用群组成员与祖先关系读取。 [Go 启动入口](../fileserver/cmd/seafserver/main.go) · [用户库](../common/user-mgr.c#L273) · [群组库](../common/group-mgr.c#L57) · [组织库](../common/org-mgr.c#L46)
- 以 C 注册表和实际 Python 调用确认参数与返回值。`get_group_members` 的 C 签名有 `group_id`、`start`、`limit`，`seaserv.api` 会传三个参数。`get_org_by_id` 在 C 注册为整数参数，Python 装饰器声明字符串。两处都按现有客户端发出的请求对拍。`get_emailuser` 会返回 `password` 字段，对拍仅使用合成账号，差异日志只列出字段名。 [C 注册表](../server/seaf-server.c#L824) · [Python 绑定](../python/seafile/rpcclient.py#L945) · [Python 调用](../python/seaserv/api.py#L1081) · [用户字段](../common/user-mgr.c#L619)
- 普通库先读取所有者记录。没有记录时返回 `null`。有记录时按所有者、个人分享、群组分享、内部公开的顺序判定。多个群组许可中 `rw` 优先于 `r`，`cloud_mode` 禁用内部公开权限回退。虚拟库先判断源库所有者，再沿虚拟库路径及其祖先查找最近的个人分享。没有命中时，才沿同一路径查找群组分享，最后回退到源库的分享权限。当前 C 基线的 `check_permission_by_path` 直接调用 `check_permission`，对拍须包含同一库、同一用户和不同 `path` 的结果。 [C 权限判定](../server/repo-perm.c#L14) · [C RPC 入口](../common/rpc-service.c#L4042)
- 现有 Go `fileserver/share` 可提供查询复用点。C 库权限判断请求含祖先的群组列表，Go `checkGroupPermByUser` 当前请求直接群组。两个调用场景先分别通过各自的回归，再决定哪些查询可以共享。SQLite 是本批对拍环境。若改动现有 Go fileserver 的查询入口，同时跑其 MySQL 回归。 [C 群组权限](../server/repo-perm.c#L38) · [Go 群组权限](../fileserver/share/share.go#L180) · [现有 CI 矩阵](../ci/run.py#L261)

## v2 场景与验收

| 场景 | C 与 Go 要比较的结果 |
| --- | --- |
| 用户和组织 | 存在、缺失、禁用用户及邮箱大小写结果。对比同一用户在两个组织的成员判断，以及组织记录缺失时的响应。 |
| 群组和成员 | 直接成员、祖先或后代关系、无关群组。对比 `return_ancestors`、`in_structure` 两种取值，以及成员列表的顺序、分页和空结果。 |
| 普通库优先级 | 所有者 `rw`、个人 `r` 与群组 `rw` 同时存在、多个群组分别给 `r`／`rw`、内部公开及 `cloud_mode`、未分享的私有库。 |
| 虚拟库与路径 | 源库所有者、同一分享类型中较近目录与祖先的冲突许可、较远祖先的个人分享与较近目录的群组分享同时存在、回退到源库许可，以及相邻目录路径。另用相同库和用户调用两个权限 RPC 核对 CE 的行为。 |
| 取消授权与边界 | C 分别固定移除成员、取消分享前后的副本，两套实现读取相同状态。核对未知库、空用户名、缺失参数和非法 ID 的返回形状。 |

每个实例使用独立数据库和对象目录副本。v2 的清单记录 C 提交、配置、每个用例依赖的快照及预期响应。原始 JSON 比较保留 `null`、整数和列表顺序，同一批请求也通过现有 Python 客户端比较解码后的结果。对拍失败时输出场景、方法和差异字段，记录可复现的样本版本。

现有 v1 回归从兼容仓和服务端的相邻工作树运行。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-data IMPL=c-baseline FIXTURES_DIR="$SEAFILE_FIXTURES_V1"
make -C "$SEAFILE_COMPAT_TESTS" test-data IMPL=go FIXTURES_DIR="$SEAFILE_FIXTURES_V1" SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR"
make -C "$SEAFILE_COMPAT_TESTS" test-api FIXTURES_DIR="$SEAFILE_FIXTURES_V1" SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" C_RPC_SOCKET="$C_RPC_SOCKET" GO_RPC_SOCKET="$GO_RPC_SOCKET"
```

这里的两个 socket 都对应 v1 副本。`SEAFILE_SERVER_DIR` 指向服务端仓库，`SEAFILE_COMPAT_TESTS` 指向兼容仓库，Python 环境提供基线 `pysearpc`。

批次 1 交付 `test-permissions` 后，用下面的入口按 v2 场景组对拍。`SCENARIO_SET` 对应 v2 清单里的组名，两个 socket 必须指向该组指定的独立副本。验收报告列出快照、配置、执行的用例数和差异。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-permissions \
  FIXTURES_DIR="$SEAFILE_FIXTURES_V2" SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" \
  SCENARIO_SET="$SEAFILE_SCENARIO_SET" \
  BASELINE_RPC_SOCKET="$SEAFILE_BASELINE_SOCKET" TARGET_RPC_SOCKET="$SEAFILE_TARGET_SOCKET"
```
