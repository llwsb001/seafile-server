# 阶段 4 第二批 · 普通库分享写入

Go 已能读取 SQLite 的用户、群组、组织和库权限，相邻兼容仓库提供 v2 的授权前后快照。下一批让现有 Python 客户端通过 `seafile.sock` 创建、修改和撤销普通库的个人分享、群组分享及内部公开权限。Go 写过的数据应能直接交给 C 继续读取和修改。 [身份 RPC](https://github.com/llwsb001/seafile-server/pull/9) · [权限 RPC](https://github.com/llwsb001/seafile-server/pull/10) · [v2 样本](https://github.com/llwsb001/seafile-compat-tests/blob/665059e6c3a92670e7363b508633944e62666201/fixtures/v2/README.md)

目录分享会创建或复用虚拟库，新建时还要写入提交和库元数据。把目录分享放到虚拟库写入具备后交付。组织分享、令牌、密钥、配额和通知继续按 [迁移总计划](go-migration-plan.md) 分批推进。 [目录分享入口](../common/rpc-service.c#L1739) · [虚拟库创建](../server/virtual-repo.c#L260)

## PR 顺序

| 顺序 | 改动 | 合入条件 |
| --- | --- | --- |
| 1 | 在 `seafile-compat-tests` 新建 `fixtures/v3` 和 `test-share-writes`。v3 引用 v2 快照及其摘要，记录写入步骤和每步的 C 响应、表变更与有效权限。 | 使用锁定的 C 基线 `fa46d4c43a9a23ff2f70acf92c14e6cb28955664`，两个独立 C 实例逐步对拍。缺少样本、摘要不符、场景未执行或存在差异时退出非零。 |
| 2 | 在 `seafile-server` 接通 SQLite 主库写入，迁移 `seafile_add_share`、`set_share_permission`、`seafile_remove_share`。 | 个人分享的创建、修改、撤销、重试和并发验收通过。Go 停止后，C 能打开同一副本读取并继续修改分享。现有只读启动路径通过回归。 |
| 3 | 迁移 `seafile_group_share_repo`、`set_group_repo_permission`、`seafile_group_unshare_repo`、`set_inner_pub_repo`、`unset_inner_pub_repo`。 | 群组及内部公开写入通过 C 对 Go、Go 重启和 C 接管。标准配置和 `cloud_mode` 都通过。个人写入及 v1、v2 回归通过。 |

## 实现边界

- 沿用现有 Unix socket 和 RPC 注册名，每个接口对齐自己的参数顺序、校验、整数返回值和错误字段。`set_group_repo_permission` 先接收 `group_id`，群组的创建和撤销先接收 `repo_id`。 [C 注册表](../server/seaf-server.c#L363) · [群组改权签名](../server/seaf-server.c#L480) · [内部公开注册](../server/seaf-server.c#L695) · [Python 客户端](../python/seaserv/api.py#L422)
- 保留主库当前的只读连接。为配置 `-c` 的实例接入独立写连接，按 `mode=rw` 延迟打开 `-d` 下已有的 `seafile.db`。启动时若缺少主库，报告文件路径。只读数据上的查询仍可运行，写连接不可用时，对应 RPC 返回写入失败的响应。身份库继续只读。 [Go 启动入口](../fileserver/cmd/seafserver/main.go#L66) · [主库读连接](../fileserver/internal/seafserver/reader.go#L39)
- 沿用 C 的 `SharedRepo`、`RepoGroup` 和 `InnerPubRepo` 表与索引。个人分享按库、分享者和接收者查重，现有表无该组合的唯一约束。Go 用一个原子 SQLite 写操作完成查重与插入，保留既有数据，后续更新与删除沿用 C 的匹配条件。群组和内部公开使用各自现有的唯一键。 [个人分享写入](../server/share-mgr.c#L88) · [群组写入](../server/repo-mgr.c#L3166) · [公开写入](../server/repo-mgr.c#L3574) · [SQLite 表定义](../scripts/sql/sqlite/seafile.sql)

## 写入验收

在锁定的 C 基线上，个人新增与改权会把邮箱转为 ASCII 小写，撤销按传入邮箱匹配。重复新增个人分享返回 `0` 并保留旧权限。重复新增同一库的同一群组返回 `-1`，原始响应没有 `err_code`。内部公开再次设置会替换旧权限，C 接受 `null` 并存为 SQL `NULL`。 [个人新增及改权](../server/share-mgr.c#L88) · [个人撤销](../server/share-mgr.c#L645) · [群组写入](../server/repo-mgr.c#L3166) · [公开写入](../server/repo-mgr.c#L3574)

| 场景 | 比较结果 |
| --- | --- |
| 个人分享 | 覆盖新增、重复新增、改权和撤销。验证邮箱大小写、无匹配行、已经存在的分享数据和互不影响的接收者。比较 `SharedRepo` 行与 `check_permission` 返回值。 |
| 群组分享 | 覆盖重复新增、改权、重复撤销、群组祖先继承，以及撤销后回退到其他分享来源。比较 `RepoGroup`、整数返回值、错误字段和有效权限。 |
| 内部公开 | 覆盖新增、替换、`null`、重复撤销和权限回退。标准和云模式下都比较 `InnerPubRepo` 与实际读到的权限。 |
| 参数、锁与重试 | 按接口覆盖参数缺失、无效标识和受拒权限值。用独立连接持锁触发写失败，验证失败后仍读取到原有的已提交状态。释放锁后重试。同一键并发写入的最终行和权限须对应一种串行结果，个人分享最多新增一行。 |
| 持久化与回退 | 每套服务使用独立副本。Go 写入后重启检查权限，再停止 Go 并在同一份数据上启动 C，验证 C 能读取、改权、撤销。核对 `Branch`、`VirtualRepo`、`RepoOwner` 及对象清单在普通库分享写入前后一致。 |

每步通过现有 Python 客户端发送一次写请求，在传输层捕获同一响应的原始 JSON。参数边界可直接发原始请求。每步执行后用只读连接比较分享表的行值、行数及 `NULL`，并通过权限 RPC 检查调用方看到的结果。写入对拍运行 C 对 C、C 对 Go 和 C 接管三条路径。

PR 1 交付以下入口，测试负责准备副本和启动实例。`SCENARIO_SET` 支持 `personal`、`group`、`public` 和 `all`。C 对 C 给两个角色传同一个锁定的 C 二进制，C 对 Go 给待测角色传 Go 二进制。运行环境提供基线 `pysearpc` 和 C 动态库。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-share-writes \
  FIXTURES_DIR="$SEAFILE_FIXTURES_V3" SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" \
  SCENARIO_SET=all SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_BIN="$SEAFILE_TARGET_BIN"
```

保留 v1 的 `test-data`、`test-api` 和 v2 的四组 `test-permissions` 回归。从 [只读权限验收](go-migration-authorization.md#v2-场景与验收) 运行这些入口。每个 PR 附 C 版本、样本版本、执行的场景数、差异和重启或接管结果。若实现复用时改动了现有 Go fileserver 的调用方，连同其 MySQL 矩阵一起验收。 [现有 CI](../ci/run.py#L267)
