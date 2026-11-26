# Linux SQLite 稳定性验收记录

本轮先排查验收工具对 SQLite 的影响，再独立运行已安装的 Go 候选。锁定 C `fa46d4c43a9a23ff2f70acf92c14e6cb28955664` 继续用于兼容对拍和同数据回退。 [稳定性计划](go-migration-sqlite-stability.md) · [发行验收记录](go-migration-sqlite-release-acceptance.md)

## 探针与在线查询

原生 arm64 Linux 对普通库和虚拟库分别发起八次真实 Web 写入。组间使用同一写入计划、相同的 RPC 查询时点和各自的快照副本。本轮记录的实际写入时点与计划最多相差 10.5 毫秒。回滚日志和 WAL 的外部事务均通过 `F_GETLK` 持锁及释放校准。 [探针对照与复现命令](https://github.com/llwsb001/seafile-compat-tests/pull/69)

| 分组 | 通过次数 | 观测方式 |
| --- | --- | --- |
| 锁定 C A | 5/6 | 周期采样和在线查询 |
| 锁定 C B | 6/6 | 在线 SQLite 查询 |
| 锁定 C C | 6/6 | 测试的 SQLite 断言延后到停写 |
| Go 队列查询开启 | 3/3 | 周期查询队列，保留 RSS 和被动文件锁采样 |
| Go 队列查询关闭 | 3/3 | 收敛和排空时查询队列，保留 RSS 和被动文件锁采样 |

C 的 A 组有一轮收到 8 次普通库写入确认。停服时统计仍为 7 个文件、14,680,064 字节，统计 head 落后，原库与虚拟库投影已对齐。短负载各组未复现历史 SQLite 锁错误，单次统计滞后仍无法归因。移除两组在线断言中的队列读取后，开启组共记录 681 次周期查询，关闭组为零。Go 独立负载沿用关闭配置，报告里的队列峰值是检查点观测值。

探针 PR 的 v7 回归有一次在 C/C 的 `nested-observes-origin-write` 等待超时，同一提交的第二次运行通过。v8 和 v9 也通过。 [C/C 首次回归](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45753307385/attempts/1) · [同提交复跑](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45753307385/attempts/2)

## Go 独立验收方式

从同一份已核验摘要的发行归档启动核心、fileserver、controller 和通知程序。每轮从新快照开始，预热后持续至少 180 秒。停写后核对队列、SQLite 完整性和 fsck，再用同一数据重启，读回虚拟库内容并让双客户端完成新一笔增量。兼容测试另外有 v9 的 C → Go → C 同数据回退演练。 [独立入口和 CI](https://github.com/llwsb001/seafile-compat-tests/pull/70)

错误归档摘要、轮次不足和重启后文件内容与期望不符的检查均能返回失败。错误归档及数据断言失败的回放会保留 JSON 记录。原生 arm64 冒烟覆盖两次完整负载和重启后的增量同步。

首次原生 amd64 回放完成 181.33 秒和 183.40 秒两轮。第三轮预热时，客户端在目录遍历与读取之间重命名了下载临时文件，验收工具因此中断。修正后轮询重新读取目录，仍要求双方文件树及内容等于预期。定向测试在读取临时文件前重命名它，验证校验完成重采样并检查最终字节。 [首次回放及失败报告](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45752655621)

原生 amd64 复跑入口如下。归档、执行日志和 JSON 报告由工作流上传。

```sh
cd "$SEAFILE_COMPAT_TESTS"
SQLITE_CI_GO_LOAD=1 SQLITE_CI_GO_REVISION=0f2289874dfa21da92295e2f89bd322204970181 \
  bash ci/test-sqlite-release.sh "$OUTPUT_DIRECTORY"
```

## Go 独立持续负载结果

服务端发行候选为 `0f2289874dfa21da92295e2f89bd322204970181`。各轮统计排除预热，重启后的文件数量包含预热写入及回放中的删除结果。

| 架构 | 轮次 | 测量时长（秒） | 完整循环 | 重启后核对的同步文件 | 重启后核对的虚拟文件 |
| --- | ---: | ---: | ---: | ---: | ---: |
| amd64 | 1 | 180.62 | 45 | 2943 | 46 |
| amd64 | 2 | 182.63 | 57 | 3711 | 58 |
| amd64 | 3 | 180.39 | 55 | 3583 | 56 |
| arm64 | 1 | 182.77 | 49 | 3199 | 50 |
| arm64 | 2 | 182.77 | 55 | 3583 | 56 |
| arm64 | 3 | 181.00 | 52 | 3391 | 53 |

amd64 使用兼容仓库合并检出 `58c77e0d9b0153a2c20a17a980a10243733155a6`，工作流一次构建并复用同一归档。下载归档的 SHA-256 为 `0779b692e704eedce0c7ab8544dfa45c8c907cff3d0376d17d115ce69b78aa96`，与持续负载报告及 v9 日志一致。三轮共完成 157 个完整循环，虚拟库收敛 p95 为 74.92 毫秒，停写后的最长排空检查耗时为 0.35 毫秒。相同归档通过安装、快照恢复、离线维护及 C → Go → C。 [原生 amd64 归档、原始报告与日志](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45754052607)

arm64 补充运行使用预构建的锁定 Linux 环境及兼容分支 `c78d1527c7eb93c1442cdfa4aecf40e90f1cb2b0` 的负载运行器。归档 SHA-256 为 `5409f4834f8fc5f3873714b51df8ef4242793731585b2043689e550d3ca4c049`。虚拟库收敛 p95 为 97.85 毫秒，三轮停写后的最长排空检查耗时为 7.71 毫秒。同一归档随后通过原生 arm64 v9 安装、快照恢复和同数据 C 接管。

## 签收状态

候选 `0f22898` 完成过整套独立验收。审阅后的同归档再次运行时，第二轮普通库上传 `web-23.bin` 收到成功响应，紧接着 Seahub 取下载链接返回 404。同轮 fileserver 曾记录一条处理 SQLite 文件写入队列时的 `SQLITE_BUSY`，两者的关系待确认。Go 独立稳定性因此重新标为待复验。兼容回归及同数据回退继续使用锁定 C。已记录的 C 持续负载基线仍未完成，C/Go 相对阈值与发行签收保持待通过。 [审阅后的失败回放](https://github.com/llwsb001/seafile-compat-tests/actions/runs/45755022191) · [现有发行门槛](go-migration-sqlite-release-acceptance.md#发行门槛)
