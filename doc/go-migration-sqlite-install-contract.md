# Linux SQLite 发行安装契约

本页冻结[发行计划任务 2](go-migration-sqlite-release.md#任务与合入条件)的输入。支持目标是 Linux SQLite、普通服务账号、锁定的 Seahub 和桌面客户端，以及启用时的 SeafDAV 和通知服务。发行验收使用实际安装路径及真实进程。涉及版本切换时，Go 和保留的 C 发行树共享同一份配置和数据。

| 组件 | 核对版本 |
| --- | --- |
| 服务端 C / Go | `fa46d4c43a9a23ff2f70acf92c14e6cb28955664` / `12def1ec78fbe3977697993a18119ebd2a1851d6`。Go 运行探针使用代码相同的 `b7e3be540a6929de101b06582adb771423b2edf8` 二进制。 |
| Seahub / 桌面同步 | `62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab` / `5826a1330038420204ddde57973f575cff9184ea`，桌面端以 `seaf-daemon` 为验收入口。 |
| SeafDAV / seafobj / libsearpc | `1d29df54f8479de960500b1518fafae91c647c4b` / `65b5bd58b3ba95eb3e11aec8b76b5b3c22fe83f4` / `3a4548697a498c951bd421248bcbdb1873f7616d`。 |
| 兼容验收 / 历史 CI 部署 | `dd673840a161d7ebd36745b7c3ae085ca6760ba0` / `c22c1e17dfe9de228c3c58cd77f8794327a2af3f`。后者的 `bootstrap.sh` 用于搭建测试环境。 [版本锁](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/versions.lock) · [历史部署说明](https://github.com/haiwen/seafile-test-deploy/blob/c22c1e17dfe9de228c3c58cd77f8794327a2af3f/README.md) |

## 发行树与共享目录

任务 4 的归档解包到实例根目录直接下属的版本目录。六个 Go 可执行文件都安装到该版本的 `seafile/bin/`。C 发行树保留独立的脚本、运行时、动态库和原名 `seaf-server`。

```text
<实例根目录>/
  <Go 版本>/
    seafile/bin/
      seafile-controller
      seaf-server-go
      fileserver
      notification-server
      seaf-fsck
      seafserv-gc
    seafile/share/sqlite/    # 五个初始化 SQL 模板
    seafile/share/config/    # 未启用的配置样例
    runtime/seafile.sock     # 运行时创建
  <C 版本>/                  # 独立保留的原发行树
  seafile-server-latest      # 安装工具维护的版本链接
  conf/
  ccnet/
  seafile-data/
  seahub.db
  seahub-data/
  logs/
  pids/
```

Go controller 先解析自身可执行文件的符号链接，再从 `seafile/bin/` 向上推导版本目录及实例根目录。它优先找同目录的二进制，找不到时搜索 `PATH`。验收要从 `/proc/<pid>/exe` 核对实际启动的文件和版本。安装工具负责把 Seahub、Python 依赖和启动适配脚本放到所选版本能读取的位置，并为服务账号创建可写的共享目录和运行时目录。配置、密钥、数据库及 PID 文件由实例管理。 [Go 安装路径](../fileserver/cmd/seafilecontroller/config.go) · [C 编译与安装目标](../Makefile.am)

## 部署入口和配置优先级

| 入口 | 实际行为和发布验收 |
| --- | --- |
| `setup-seafile.sh` 与 `upgrade/minor-upgrade.sh` | 锁定的安装脚本从版本目录下的 `sql/sqlite` 读取五个 SQL 文件，从 Seahub 发行树读取 `seahub/sql/sqlite3.sql`，并建立实例顶层的配置、数据库、媒体目录和 `seafile-server-latest` 链接。上游脚本输出的 `seafile.conf` 只有 `[fileserver]` 端口。任务 5 从 Go 归档的模板路径创建 SQLite 数据，显式生成 `[database] type = sqlite`。锁定的 Seahub 源树走兼容回放已使用的 `manage.py migrate --run-syncdb --noinput`。任务 6 验证选定版本链接、共享媒体及原 C 版本回退。 [初始化脚本](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/setup-seafile.sh#L643-L723) · [升级脚本](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/upgrade/minor-upgrade.sh) |
| `seafile.sh start/stop/restart` | 锁定的 Seahub 发行脚本直接执行 C `seaf-server -F … -d … -l … -P … -p … -f`，随后启动 `seafile-monitor.sh`。该 monitor 可单独启用 fileserver 和 SeafDAV。任务 6 的 Go 启停入口保留同样的操作，启动 Go controller，并以实例 PID、监听地址及 socket 判定就绪。停服后核对两端只留一个运行组合，切回 C 时使用原发行树。 [启停脚本](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/seafile.sh#L141-L228) · [monitor](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/seafile-monitor.sh#L222-L256) |
| `seafile-controller` 和子进程 | `-F <conf> -c <ccnet> -d <seafile-data> -l <logs>` 指向实例目录。controller 自身 PID 优先用 `-P`，再用 `SEAFILE_PIDFILE`。子进程使用 `<实例根>/pids/{seaf-server,fileserver,seafdav}.pid`。新启动使用 `<版本>/runtime/seafile.sock`。当 SQLite 配置的 `use_go_fileserver` 为真，controller 启动同目录 `seaf-server-go` 及 `fileserver`，向子进程置 `ENABLE_GO_FILESERVER=true`，核心启用私有 socket 及管理、分享写 RPC。配置为假时核心名为 `seaf-server`。加 `-f` 或设置 `SEAFILE_LOG_TO_STDOUT=true` 时 controller 留在前台。任务 6 保留日志和实例级终止行为，使用 `GET /protocol-version` 检查 HTTP，预期 `{"version":2}`。 [Go controller](../fileserver/cmd/seafilecontroller/main.go) · [进程监督](../fileserver/cmd/seafilecontroller/supervisor.go) · [C controller](../controller/seafile-controller.c) |
| `seahub.sh start [port]/stop/restart` | 锁定脚本从版本目录启动 Gunicorn 加载 `seahub.wsgi:application`，使用实例 `conf/gunicorn.conf.py`、`pids/seahub.pid` 和 `logs/`。它先用 `check_init_admin.py` 通过 `seaserv.ccnet_api` 查询或创建管理员，再检查 Gunicorn 进程。锁定脚本还要求存在名为 `seafile-monitor.sh` 的进程。任务 6 的 Go 启动适配以 controller、RPC 和 HTTP 已就绪为条件。Python 使用实例的 `SEAFILE_DATA_DIR`、`SEAFILE_CENTRAL_CONF_DIR` 和所选版本的 `SEAFILE_RPC_PIPE_PATH`。包含锁定的 Seahub、`seaserv`、`pysearpc` 及运行依赖。 [Seahub 启动入口](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/seahub.sh) · [管理员入口](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/check_init_admin.py#L285-L289) · [Python RPC socket](../python/seaserv/service.py) |
| `seaf-fsck.sh` / `seaf-gc.sh` | 锁定包装脚本定位自己版本的 `seafile/bin/seaf-fsck` / `seafserv-gc`，加 `-F <conf> -d <seafile-data>`，传入所选库与工具选项。任务 6 保留参数边界、子命令退出码及实例级停写判断，任务 9 用安装版本的入口回放离线命令。 [fsck 包装](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/seaf-fsck.sh#L71-L102) · [GC 包装](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/seaf-gc.sh#L112-L157) |

- controller 读取 `<conf>/seafile.conf`，从 `[fileserver] use_go_fileserver` 决定组合。调用方给 controller 的同名环境开关不会代替配置。Go fileserver 对同名 HTTP 配置先读 `[httpserver]`，再读 `[fileserver]`。群组库路径优先用 Go fileserver 的 `-c`，其次用 `CCNET_CONF_DIR`。 [fileserver 配置](../fileserver/option/option.go) · [SQLite 群组库](../fileserver/database.go)
- 锁定的 Seahub 与旧启动脚本优先用已有 `JWT_PRIVATE_KEY`，未设置时才读取实例的 `conf/.env`。Go fileserver 与通知服务使用启动环境提供的值。Seahub 在常规发布设置中从同一实例配置加载密钥。发版验收给 Go fileserver、通知服务和 Seahub 使用同一测试密钥，并让启用通知的核心及 fileserver 读取相同的 `INNER_NOTIFICATION_SERVER_URL`。 [Seahub 环境准备](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/seahub.sh#L190-L241) · [核心事件配置](../fileserver/internal/seafserver/notification.go)

## SQLite 数据和初始化入口

| 实例路径 | 来源及使用方 |
| --- | --- |
| `seafile-data/seafile.db` | `seafile.sql`。C/Go 核心和 Go fileserver 使用同一 SQLite 业务表，离线工具读取该库与 `seafile-data/storage/` 的对象。 |
| `ccnet/PeerMgr/usermgr.db` | `user.sql`，C 与 Go 身份及账号管理。 |
| `ccnet/GroupMgr/groupmgr.db` | `groupmgr.sql`，核心与 fileserver 权限查询。通知服务用 `NOTIFICATION_SERVER_SQLITE_GROUP_DB` 读取同一文件。 |
| `ccnet/OrgMgr/orgmgr.db` | `org.sql`，组织及组织权限。 |
| `ccnet/misc/config.db` | `config.sql`，保留在 C 发行与存量 SQLite 布局中。 |
| `seahub.db`、`seahub-data/` | 实例级 Seahub Django 数据库和媒体目录。配置从 `conf/seahub_settings.py` 读取。初始化后用 `ccnet_api.get_emailusers` / `add_emailuser` 完成首次管理员登录。 [Seahub 发版数据库配置](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/settings.py#L1224-L1243) |

Go 维护的 `GoFileWriteQueue(repo_id, head_id)` 是新增表。Go 端用 `CREATE TABLE IF NOT EXISTS` 接管没有该表的旧库，任务行与对应分支发布在同一事务写入。升级与回退必须保留共享表、对象格式和历史边界。切回 C 前停止写入口，等待队列排空和统计追上已确认的 head，再停止 Go 并复核同一状态。 [SQLite SQL 模板](../scripts/sql/sqlite/) · [Go 身份数据库](../fileserver/internal/seafserver/identity.go) · [持久队列](../fileserver/internal/seafserver/file_write_queue.go)

## 消费者与验收入口

以下 Make 目标在锁定的兼容仓库运行。v7 和 v8 使用同一已推送 Go 提交和锁定 C 分别回放。现有真实消费者结果验证了协议路径，发行验收还要记录所选安装目录、各进程 PID 和可执行文件路径。 [现有回放参数](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/fixtures/v7/README.md) · [离线与生命周期验收](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/fixtures/v8/README.md)

| 消费者 | 请求、处理进程与可观察结果 | 现有回放与发行验收 |
| --- | --- | --- |
| Seahub 登录、库列表和目录 | `/api2/auth-token/` 的 Django 认证调用 ccnet 用户 RPC。库和目录入口调用 `get_owned_repo_list`、`get_repo`、`list_dir_with_perm` 等 `seaserv` 方法，C/Go 核心经同名 Searpc 服务返回用户、权限和目录对象。 [库入口](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/repos.py) · [目录入口](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/dir.py) · [Go RPC 注册](../fileserver/cmd/seafserver/main.go) | v2 权限、v5 写入及 `test-fileserver-seahub-clients`。空目录安装增加真实管理员登录、建库、写入和普通账号访问校验。 |
| Seahub 上传、下载及历史 | `upload-link`、`download-info` 和文件操作签发 `seafile_web_get_access_token`，客户端访问 `/upload-api/`、`/update-api/`、`/files/` 等路由。C 核心内置 HTTP 或独立 Go fileserver 返回上传结果和下载字节。历史及回收站调用 `seafile_get_commit_list`、`seafile_revert_file`、`restore_repo_from_trash` 等核心 RPC。 [Seahub 文件入口](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/file.py) · [历史入口](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/seahub/api2/endpoints/repo_history.py) · [C HTTP](../server/http-server.c) · [Go 路由](../fileserver/fileserver.go) | v7 `upload`、`download`、`blocks`、`zip`，v8 `file-history`、`file-restore`、`repo-restore` 和真实 Seahub 双客户端回放。比较状态、响应头、字节 SHA-256 及恢复后的 head。 |
| 锁定的 `seaf-daemon` | 协商 `/protocol-version`，通过 `/repo/<id>/permission-check`、`commit/HEAD`、commit、block、`pack-fs`、`recv-fs` 和 `quota-check` 克隆与增量写入。C HTTP 和 Go fileserver 校验同步令牌后返回状态、对象帧或新 head。带外通知由 `jwt-token` 签发。 [客户端 HTTP](https://github.com/haiwen/seafile/blob/5826a1330038420204ddde57973f575cff9184ea/daemon/http-tx-mgr.c) · [通知订阅入口](https://github.com/haiwen/seafile/blob/5826a1330038420204ddde57973f575cff9184ea/daemon/sync-mgr.c#L2548-L2556) | v7 `sync-read`、`sync-write`、`test-fileserver-sync-client`、`test-fileserver-sync-two-clients`。发行回放加锁定客户端真实读写、重启和同数据 C 接管。 |
| 通知服务 | 独立的 `notification-server -c <conf> -l <log>` 读取启动环境中的 `JWT_PRIVATE_KEY`、`NOTIFICATION_SERVER_SQLITE_GROUP_DB`、监听地址与端口。默认端口 8083，`GET /ping` 返回 `{"ret":"pong"}`。核心或 fileserver 在提交后发 `POST /events`，服务通过 WebSocket 推送对应库及 commit。 [通知进程和路由](../notification-server/server.go) | `test-fileserver-notification` 及真实 Seahub 双客户端回放。任务 7 用发行入口管理通知进程并重跑断线重连、真实事件及拒绝错误签名。 |
| SeafDAV | Go controller 在 `seafdav.conf` 的 `[WEBDAV] enabled=true` 时启动 `python3 -m wsgidav.server.server_cli --server gunicorn`。默认 DAV 入口为 `/seafdav/`，Gunicorn PID 和日志分别在实例 `pids/` 和 `logs/`。SeafDAV 用 `get_emailuser` 和 `validate_emailuser` 验证账号，通过 seafobj 读取对象，使用 Searpc 检查路径权限、列库和对文件提交写入。 [DAV 运行入口](https://github.com/haiwen/seafdav/blob/1d29df54f8479de960500b1518fafae91c647c4b/wsgidav/server/server_cli.py#L292-L320) · [DAV 认证](https://github.com/haiwen/seafdav/blob/1d29df54f8479de960500b1518fafae91c647c4b/wsgidav/dc/domain_controller.py#L49-L139) · [DAV 读写](https://github.com/haiwen/seafdav/blob/1d29df54f8479de960500b1518fafae91c647c4b/wsgidav/seafile_dav_provider.py#L187-L261) | `test-fileserver-controller SEAFILE_CONTROLLER_DAV=1` 检查匿名 401 和子进程重启。任务 7 还要用有效账号实际执行 PROPFIND、PUT、GET，检查内容、只读拒绝和 C 接管。 |
| 离线工具 | `seaf-fsck` 检查、`-s` 浅检、`-S` 文件大小检查、`-E <目录>` 导出及 `-r` 修复。 `seafserv-gc` 的 `-C` 校验、`-D` 预演、`-r` 清理已删除库、`-R` 清理不可达 FS 对象，支持位置参数选择库。工具进程结束后核对冻结的返回码、对象清单、SQLite 状态和历史边界。 [Go fsck](../fileserver/cmd/seaffsck/main.go) · [Go GC](../fileserver/cmd/seafservgc/main.go) · [C 离线入口](../server/gc/Makefile.am) | v8 `test-sqlite-maintenance` 与 `test-sqlite-fsck-repair`，任务 9 用离线副本验收实际安装的包装脚本和 C 接管。 |

## 缺项与复现

| 缺项 | 复现和当前结果 | 后续归属 |
| --- | --- | --- |
| Go 组合的配置预检仍依赖 C | 在版本目录只放六个 Go 工具，设置 SQLite 与 `use_go_fileserver=true`，用不含 C 发行目录的 `PATH` 执行 `seafile-controller -F <conf> -c <ccnet> -d <data> -t`。Linux arm64 实测退出 1，提示找不到 `seaf-server`，且未启动 RPC。现有 controller 回放把 C 同时放入版本目录，预检发生在 C 组合。 [Go 预检](../fileserver/cmd/seafilecontroller/main.go#L62-L70) · [现有 controller 回放](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/tests/check_fileserver_controller.py) | 任务 3 服务端。按选定组合预检，覆盖有效配置、无效配置及 C 模式，记录数据库、PID 和 socket 无变化。 |
| SeafDAV 文件内容写入缺少 RPC | 对现有空文件调用 `seafile_put_file(repo, 临时文件, "/", 文件名, 用户, null)`。在两个独立 v2 副本中，C 和 Go 的 `seafile_post_empty_file` 均返回 0。C 的 `put_file` 返回与下一次查询一致的 40 位对象 ID。Go 返回 500，提示找不到函数。锁定的 SeafDAV 在完成 PUT 时调用该方法。C 的实际 RPC 签名返回字符串，Python 装饰器仍写作 `int`，以真实 C 响应及 DAV 行为验收。 [C 注册](../server/seaf-server.c#L179-L182) · [C 实现](../common/rpc-service.c#L2749-L2797) · [Go 文件写入注册](../fileserver/internal/seafserver/file_write.go) | 任务 3 服务端与兼容仓库。补文件发布 RPC 对拍、权限失败与 C 接管。任务 7 补端到端 DAV 测试。 |
| 旧格式文件的 SeafDAV mtime 查询 | 锁定 DAV 对 v0 目录及无有效 mtime 的文件会读 `seafile_calc_files_last_modified`。在 v2 副本用同一库、根路径和限制 1 调原始 RPC，C 存在该函数，Go 报缺失。仍需用 v0 或 mtime 为零的 DAV 样本冻结具体返回与 HTTP 行为。 [DAV mtime 路径](https://github.com/haiwen/seafdav/blob/1d29df54f8479de960500b1518fafae91c647c4b/wsgidav/seafile_dav_provider.py#L159-L179) · [C RPC 注册](../server/seaf-server.c#L303-L307) | 任务 3 服务端与兼容仓库。用声明支持的存量格式回放后决定需要补齐的返回及旧格式分支。 |
| 上游 SQLite 安装及启停脚本绑定 C 布局 | 检查锁定 `setup-seafile.sh` 和 `seafile.sh` 的目标路径。前者读取归档约定之外的旧 SQL 路径、依赖 Seahub 发行物的 SQL 文件，且只写 fileserver 端口。后者直接拉起 C 与 monitor，`seahub.sh` 以进程名检查 monitor，停机用全局进程名匹配。离线包装脚本使用未保留参数边界的展开，并在子进程后输出另一条命令。 [安装脚本](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/setup-seafile.sh) · [启停脚本](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/seafile.sh) | 任务 5 接归档模板与初始化。任务 6 验收带空格路径、实例级 PID、版本链接及维护工具退出码。任务 9 从发行归档跑完整流程。 |
| 通知和 WebDAV 的完整安装链待验证 | 当前独立通知回放和 controller DAV 401 回放各自启动进程。发行验收要从实际启动入口启用通知、用两个实际客户端接收更新，再用普通账号和只读账号跑 DAV 读写及通知/DAV 各自重启。锁定 DAV 的联系人邮箱、登录 ID、WebDAV 专用密码和双因素认证分支使用其 MySQL 会话，SQLite 普通账号验收走 ccnet RPC 分支。 [DAV 账号分支](https://github.com/haiwen/seafdav/blob/1d29df54f8479de960500b1518fafae91c647c4b/wsgidav/dc/domain_controller.py#L57-L122) · [DAV 会话来源](https://github.com/haiwen/seafdav/blob/1d29df54f8479de960500b1518fafae91c647c4b/wsgidav/dc/seahub_db.py#L29-L72) | 任务 7 负责已选范围内的集成验收。任务 12 将 SQLite 普通账号范围写入交付记录。 |

`seafile_put_file` 复现可使用兼容仓库 `tests/compare_permissions.py` 的 `prepare` 创建两个 v2 `permissions` 副本。选样本库 `f2207773-8420-47d0-8f7c-dd8d7aeb1ea9` 和所有者 `owner@example.test`。分别启动锁定 C 和置 `--enable-share-writes --enable-management-rpcs` 的 Go，调用 `seafile_post_empty_file(repo, "/", "probe.txt", owner)`，再传入含测试字节的本地文件执行 `seafile_put_file`。用 `seafile_get_file_id_by_path(repo, "/probe.txt")` 对照 C 返回并读取 Go 错误。真实 DAV 的 PUT 回放沿用同一调用链。
