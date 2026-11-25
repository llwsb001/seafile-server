# 阶段 4 第三批 · Web 文件访问令牌

`seafile_web_get_access_token` 和 `seafile_web_query_access_token` 已合入。现有 Python 客户端从 Go `seaf-server` 签发 Web 文件访问令牌，由现有 Go fileserver 通过同一个 Unix socket 查询。同一进程负责一次性令牌的签发、过期和消费。下一批见 [SQLite 后续迁移计划](go-migration-sqlite-next.md)。 [服务端 PR #16](https://github.com/llwsb001/seafile-server/pull/16) · [v4 样本 PR #7](https://github.com/llwsb001/seafile-compat-tests/pull/7) · [C RPC 注册](../server/seaf-server.c#L584) · [Python 入口](../python/seaserv/api.py#L24) · [Go fileserver 消费入口](../fileserver/fileop.go#L2988)

本批的目标部署是 Go 核心搭配现有 Go fileserver，验收两者的 Searpc 边界。C 内置 HTTP 的 ZIP 后台任务随 [HTTP 迁移](go-migration-plan.md#pr-顺序和验收条件) 处理，库同步令牌、JWT、解密密钥和配额按各自流程推进。 [C 令牌的 ZIP 分支](../server/web-accesstoken-mgr.c#L179)

## PR 顺序

| 顺序 | 改动 | 合入条件 |
| --- | --- | --- |
| 1 | 在 `seafile-compat-tests` 新建 `fixtures/v4` 和 `test-web-tokens`。引用 v2 快照及摘要，记录用例步骤、各步预期响应和令牌别名。 | 在两个独立 C 实例上跑完顺序场景。核对随机令牌格式、引用关系、原始 JSON、Python 解码和状态变化。缺少输入、摘要不符、未执行用例或有差异时退出非零。 |
| 2 | 在 `seafile-server` 同批接入两个 RPC、进程内令牌管理和 TTL 配置。用现有 Go fileserver 的令牌解析路径验证消费。 | v4 的 C 对 Go 顺序对拍、Go 单次消费并发测试、临界过期、进程重启和 socket 权限验收通过。Go fileserver 走真实 Unix socket 取得四个字段，并将失效令牌映射为 `403`。v1、v2、v3 回归通过。 |

## 实现约定

- 沿用 `[fileserver] use_go_fileserver` 和 `ENABLE_GO_FILESERVER=true` 启用 Go fileserver 模式。环境变量为 `true` 时启用该模式，其余值按配置文件决定。C 始终注册两个 RPC，该模式控制 C 内置 ZIP 任务。Go 核心仅在模式启用时注册两个 RPC，C 对 Go 顺序对拍使用这一模式。关闭时两个方法均返回 Searpc 未注册方法错误。 [C 配置与环境变量](../server/seafile-session.c#L133) · [C RPC 注册](../server/seaf-server.c#L584) · [Go 启动入口](../fileserver/cmd/seafserver/main.go)
- 签发 RPC 由可信的本地调用方使用。令牌模式下，Go 在 socket 可连接前确保权限为 `0700`，无法保证时启动失败。Python 签发方、Go fileserver 和 Go 核心以同一个服务账号访问该 socket。 [C socket 权限](https://github.com/haiwen/libsearpc/blob/3a4548697a498c951bd421248bcbdb1873f7616d/lib/searpc-named-pipe-transport.c#L172)
- 按 C 入口校验参数与操作类型，复用现有操作名称。`obj_id` 按客户端传入的字符串保存，验收覆盖文件 ID 格式、目录下载参数和上传调用使用的空字符串。查询返回 `repo-id`、`obj-id`、`op`、`username`。缺参和操作非法分别对拍错误码与消息，未知或失效令牌返回 `null`。 [C RPC 参数](../common/rpc-service.c#L1605) · [C 操作列表](../server/web-accesstoken-mgr.c#L140) · [返回对象](../lib/webaccess.vala#L3)
- 用安全随机数生成符合 C 格式的 UUID，令牌状态仅存进程内。`[fileserver] web_token_expire_time` 读取十进制 32 位正整数秒，缺失、非法或非正值时使用 `3600` 秒。到期秒数等于签发时的 Unix 整秒加 TTL，查询时的 Unix 整秒达到到期秒数即失效。定时回收过期记录，服务关闭时结束清理任务。 [C 令牌与清理](../server/web-accesstoken-mgr.c#L70) · [C TTL 配置](../server/seafile-session.c#L44)
- Go 对一次性令牌在同一临界区查询、判断到期并消费，多个连接同时查询时只有一个能得到有效响应。兼容仓库用顺序请求对拍 C 的生命周期，并对 Go 单独验证并发消费。 [C 查询契约](../server/web-accesstoken-mgr.c#L211)

## 验收场景

| 场景 | 比较结果 |
| --- | --- |
| 签发与解码 | 每边独立签发，验证令牌是 36 字符 UUID、连续签发不同值。比较返回对象的四个字符串和 Python 客户端的解码结果。报告与冻结数据用别名引用令牌。 |
| 消费和过期 | 一次性令牌首次查询成功，随后为 `null`。可复用令牌在过期前多次返回同一对象，到期后为 `null`。Go 测试用受控时钟覆盖在秒内签发、到期前仍有效、到期整秒即失效。C 对拍用短 TTL 和有界等待。 |
| 参数和配置 | 覆盖签发缺参、无效操作、查询的空值及未知令牌。验证 TTL 的默认值、带前导零的十进制正数与回退，覆盖配置文件启用、环境变量启用和默认关闭。默认关闭时，同时核对两个 RPC 的未注册错误。 |
| Socket 权限 | 检查 socket 权限为 `0700`。验证同一服务账号下的 Python 签发与 Go fileserver 查询可用，其他普通本地账号无法连接。 |
| 并发与重启 | 多个 Go RPC 连接查询同一一次性令牌，恰好一次成功。重启后旧令牌失效，新令牌可以签发并正常消费。 |
| Go fileserver 消费 | 使用现有 Searpc 客户端连接被测 socket，调用实际令牌解析入口。核对有效对象的四个字段、查询返回 `null` 时的 `403`，以及关闭模式下未注册方法错误对应的 `500`。文件内容、Range 与 ZIP 的完整 HTTP 验收见 [迁移总计划](go-migration-plan.md)。 |

签发和查询都按步骤执行一次，尤其是一次性令牌查询。沿用 v3 的做法，在 Python 客户端发送请求时捕获同一份原始响应，再比较解码结果。C 基线采用 `fa46d4c43a9a23ff2f70acf92c14e6cb28955664`，两套实例使用独立运行目录。 [v3 单次调用回放](https://github.com/llwsb001/seafile-compat-tests/blob/110922607a826bce103203b8194e60bc6606a2cc/fixtures/v3/README.md)

`test-web-tokens` 提供 v4 验收入口。C 对 C 设 `SEAFILE_TARGET_IMPL=c`，C 对 Go 设 `SEAFILE_TARGET_IMPL=go` 并传入待测二进制。运行环境提供基线 `pysearpc` 和 C 动态库。

```sh
make -C "$SEAFILE_COMPAT_TESTS" test-web-tokens \
  FIXTURES_DIR="$SEAFILE_FIXTURES_V4" SEAFILE_SERVER_DIR="$SEAFILE_SERVER_DIR" \
  SEAFILE_BASELINE_BIN="$SEAFILE_BASELINE_BIN" \
  SEAFILE_TARGET_IMPL="$SEAFILE_TARGET_IMPL" SEAFILE_TARGET_BIN="$SEAFILE_TARGET_BIN"
```

回归包含 v1 的 `test-data`、`test-api`，v2 的四组 `test-permissions` 和 v3 的 `test-share-writes`。PR 报告记录基线版本、执行的用例数、差异、消费并发和重启结果。 [v1、v2 验收入口](go-migration-authorization.md#v2-场景与验收) · [v3 验收入口](go-migration-share-writes.md#写入验收)
