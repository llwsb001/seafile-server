# Linux SQLite 启停与回退

使用 Linux 服务账号直接运行发行包的 `seafile/bin/seafile-sqlite`。入口使用支持 Linux pidfd 的系统 `/usr/bin/python3`（Python 3.10 或更高版本）。SeafDAV 仍可通过 `PATH` 选择自己的 Python。版本目录直接位于实例根目录下。同一实例的 `conf`、`ccnet`、`seafile-data`、`logs` 和 `pids` 由各版本共享。

在实例的 `conf/seafile.conf` 设置 `[database] type = sqlite` 和 `[fileserver] use_go_fileserver = true`。使用正常部署方式为该进程提供 `JWT_PRIVATE_KEY`。各命令中的 `GO_VERSION` 和 `C_VERSION` 指向实例下各自的版本目录。

执行 `restart` 前先停止 Seahub。若 Seahub 仍在运行，入口会报错，保留当前核心和文件服务进程。

```sh
SERVICE="$GO_VERSION/seafile/bin/seafile-sqlite"
"$SERVICE" --version-dir "$GO_VERSION" check
"$SERVICE" --version-dir "$GO_VERSION" start
"$SERVICE" --version-dir "$GO_VERSION" status
"$SERVICE" --version-dir "$GO_VERSION" restart
"$SERVICE" --version-dir "$GO_VERSION" stop
```

`start` 先检查共享 PID、版本的 RPC socket 及已配置 HTTP 端口。Go controller、核心和 fileserver 的实际可执行路径与选定版本一致，且 RPC 和 `/protocol-version` 可用后，命令返回成功。启动诊断写入 `logs/sqlite-launch.log`。停止命令核对进程对应版本，再通过 pidfd 发送信号并等待子服务退出。换版本前先停止 Seahub 及其它写入方。启用的 Go SeafDAV 由 controller 管理，Seahub 和通知服务各自由部署入口启动。

`status` 输出一条 JSON。核心进程和两个探针均就绪时，`state` 为 `running`。未发现进程或监听的 RPC 时为 `stopped`。发现部分组件、版本路径不匹配或无法读取进程时为 `degraded`。这三种观察结果都以退出码 0 返回。Seahub 和 DAV 的 PID 可用于诊断，未启用时不影响核心状态。命令可在停服时执行，无需创建 PID 目录、操作锁或修改数据。

`queue.state` 为 `empty`、`pending`、`not-present` 或 `unavailable`。`pending` 同时报告持久队列的行数。遇到 WAL 模式、读锁冲突或缺失数据库时报告 `unavailable`，保留已有文件状态。回退仍以 C `start` 的队列检查为准。

使用同一入口运行离线命令时，`--` 后的选项交给该版本的工具。入口拒绝写入方仍在运行的实例，并用选定版本对应的配置与数据路径调用工具。

```sh
"$SERVICE" --version-dir "$GO_VERSION" fsck -- -s "$REPO_ID"
"$SERVICE" --version-dir "$GO_VERSION" gc -- -D "$REPO_ID"
```

### 用原 C 版本接管

1. 停止写入入口，确认 Go 的持久队列已排空，统计已追上最新 head。用 Go 版本所在路径执行 `stop`。
2. 在共享配置中把 `use_go_fileserver` 改为 `false`。保留 C 版本的动态库环境，执行以下命令。入口先检查 Go 队列，再调用原版本的 C `seaf-server` 及其内置 HTTP。队列仍有工作时，先恢复 Go 运行至队列排空。
3. 让 Seahub 等外部组件使用 C 版本的 `runtime/seafile.sock`。核对存量数据读写后，再恢复客户端入口。 C 版外部组件继续沿用原安装约定。

```sh
"$SERVICE" --version-dir "$C_VERSION" --implementation c check
"$SERVICE" --version-dir "$C_VERSION" --implementation c start
```
