# SQLite 文件服务迁移验收

在 Linux ARM64 隔离环境中，分别启动锁定的 C 核心及其 HTTP 服务、Go 核心及独立的 Go fileserver。对拍使用同一批 SQLite 和对象目录副本。部署演练使用同一份数据，从 C 切到 Go，随后由 C 接管并继续处理已有库写入。 [任务与验收条件](go-migration-sqlite-fileserver.md)

| 版本 | 验收使用的提交 |
| --- | --- |
| C 服务端 | `fa46d4c43a9a23ff2f70acf92c14e6cb28955664` |
| Go 服务端候选 | `519856694ea7ade6bf7335eb21a1d5f8999a725f`。 [组合代码](https://github.com/llwsb001/seafile-server/commit/519856694ea7ade6bf7335eb21a1d5f8999a725f) |
| 兼容仓库候选 | `886979b9772cee6556408502e9763b76b40c8bbf`。 [组合样本](https://github.com/llwsb001/seafile-compat-tests/commit/886979b9772cee6556408502e9763b76b40c8bbf) |
| Seahub | `62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab` |
| 桌面客户端 | `5826a1330038420204ddde57973f575cff9184ea` |

## 回归结果

| 样本 | 结果 |
| --- | --- |
| v1 对象与读取 | 113 个对象校验通过。C/Go 只读 RPC 对拍通过 41 个场景，Go 从真实归档读取普通和加密库。 |
| v2 权限与身份 | 66 个场景与 C 一致，覆盖身份、权限、云模式和撤销。 |
| v3 分享写 | 64 步与 C 一致，涵盖个人、群组、公开和云模式公开分享。重启和 C 接管均通过。 |
| v4 Web 令牌 | 49 步与 C 一致。额外验证私有 socket 权限和并发单次消费。 |
| v5 SQLite 核心 | 541 步通过，其中 21 步按已冻结的 Go 响应校验。另有 2 步只回放 C。 |
| v6 库与文件 RPC | 109 步与 C 一致。涵盖新库、虚拟库、目录分享、同库文件操作、普通可写库跨库移动及 C 接管。 |
| v7 HTTP 和文件 RPC | C/C 与 C/Go 各通过 25 个场景、473 步。冻结差异随各场景记录。 |

v7 验证了鉴权和即时撤销、浏览器下载及 Range、ZIP 的准备及取消、普通和块文件写、断点续传、桌面同步读写、二父提交、虚拟库 HTTP 和基础文件 RPC 的双向传播及接管。对拍校验 HTTP 状态、业务响应、文件摘要、分支 head 和 SQLite 行。锁定 C 在并发提交碰到 SQLite 写锁时偶尔保留旧统计缓存，虚拟库场景用目录计数 RPC 对拍 C 的文件树，再检查 Go 持久化的大小和文件数是否对应当前 head。 [v7 入口和范围](https://github.com/llwsb001/seafile-compat-tests/tree/886979b9772cee6556408502e9763b76b40c8bbf/fixtures/v7)

分支发布的测试覆盖旧 head、两连接池竞争、缺失对象和 SQLite 提交回滚。真实写请求的通知验收覆盖 JWT、WebSocket 顺序和接收端拒绝响应。分支一旦提交，通知失败保留已提交的 head，并写入日志。 [分支发布](https://github.com/llwsb001/seafile-server/pull/37) · [通知回放](https://github.com/llwsb001/seafile-compat-tests/pull/37)

跨库移动的已验收范围是两个普通未加密库，操作者在两侧具备写权限。涉及加密库或虚拟库的跨库移动还待迁移。虚拟库的库内改名、移动、删除及源库双向传播已经验收。 [共享库跨库移动](https://github.com/llwsb001/seafile-server/pull/50) · [虚拟文件 RPC](https://github.com/llwsb001/seafile-server/pull/51)

## 同一数据的切换和回退

1. C 接真实 Seahub 登录，签发上传链接并写入文件。
2. 停止 C 组合，Go 核心和 Go fileserver 从同一份 SQLite 和对象目录启动。用 Seahub 完成 Web 上传，并为两个锁定版本的桌面客户端签发独立同步令牌。
3. 两个客户端克隆后双向增量。离线同时改同名文件，恢复连接后两份内容均可读。Seahub 注销一台设备后，旧令牌返回 403。重新登录并取得新令牌后，原客户端恢复同步。
4. 重启 Go 并复查已写文件。停下 Go 后让 C 用同一份数据接管。真实 Web 下载核对文件摘要，C 继续接受上传和桌面同步写入。最后核对分支 head、有效字节数与 10 个文件的计数。

在两次 Linux ARM64 Docker 演练中，Go 使用组合提交 `cd4ace5`。下表记录其中一次运行的约 12 MiB 文件样本。吞吐从实际 HTTP 传输计时，RSS 取处理 HTTP 的进程。 [双客户端回退记录](https://github.com/llwsb001/seafile-compat-tests/pull/42)

| 阶段 | 上传 MiB/s | 下载 MiB/s | 下载耗时 | 文件服务 RSS |
| --- | ---: | ---: | ---: | ---: |
| C 初始 | 343.26 | 2985.86 | 4.0 ms | 21.7 MiB |
| Go | 223.20 | 5267.37 | 2.3 ms | 34.4 MiB |
| C 接管 | 276.70 | 3057.16 | 3.9 ms | 21.4 MiB |

## 复现入口

- `make test-fileserver` 使用锁定的 C 与真实 Go 二进制复跑 v7，`SCENARIO_SET=all` 选择全部场景。 [参数和冻结规则](https://github.com/llwsb001/seafile-compat-tests/tree/886979b9772cee6556408502e9763b76b40c8bbf/fixtures/v7)
- `make test-fileserver-controller` 验证默认 C、显式 Go、私有 socket、两个进程各自失效及恢复、错误配置与回切 C。 [Controller 验收](https://github.com/llwsb001/seafile-compat-tests/pull/39)
- `make test-fileserver-objects` 验证 C 可以读 Go 写入的普通和加密对象。 `make test-fileserver-notification` 验证实际通知接收进程和顺序。 [对象回放](https://github.com/llwsb001/seafile-compat-tests/pull/25) · [通知回放](https://github.com/llwsb001/seafile-compat-tests/pull/37)
- `make test-fileserver-seahub-clients` 启动真实 Seahub、通知进程和两个客户端，复跑切换、撤销和回退。 [回放与依赖版本](https://github.com/llwsb001/seafile-compat-tests/pull/42)
- `make test-fileserver-ci` 按锁文件构建 C 与 Go，在隔离环境复跑。 [CI](https://github.com/llwsb001/seafile-compat-tests/pull/38)
