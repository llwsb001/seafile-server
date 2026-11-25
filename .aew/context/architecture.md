# 架构

- Seahub 通过 `python/seaserv` 调用 `seafile.sock` 上的 Searpc。C `seaf-server` 注册 RPC 方法，并在配置选用 C fileserver 时提供 HTTP。 [Python 入口](../../python/seaserv/service.py) · [RPC 注册](../../server/seaf-server.c) · [进程启动](../../server/seafile-session.c)
- Go `fileserver` 提供同步、上传和下载 HTTP 路由，访问数据库和存储，并通过 Searpc 查询令牌和密钥、发布事件。Go `notification-server` 接收事件并通知客户端。 [文件服务](../../fileserver/fileserver.go) · [通知服务](../../notification-server/server.go)
- 文件数据由 SQLite 或 MySQL 中的分支和存储目录中的对象共同表示。读取从 `Branch` 的 head 开始，经 commit 和 FS 对象找到块。虚拟库的 commit 属于虚拟库，FS 对象和块位于源库存储目录。 [分支和虚拟库表](../../scripts/sql/sqlite/seafile.sql) · [库加载](../../server/repo-mgr.c)
