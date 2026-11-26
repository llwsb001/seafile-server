# 构建 Linux SQLite 候选归档

在可运行 Linux 容器的 amd64 或 arm64 Docker 主机上，从包含本构建入口的服务端检出执行以下命令。主机需提供 Bash、Git、Docker 和 `sha256sum`，容器构建需能从 Go 模块代理下载以 `go.sum` 校验的依赖。

```sh
release/sqlite/build.sh HEAD amd64 ./dist
release/sqlite/build.sh HEAD arm64 ./dist
```

首个参数接受 Git 提交或引用。构建从该提交提取源码、模板和构建锁，工作区改动不会进入归档。输出文件名为 `seafile-linux-sqlite-<完整提交>-<架构>.tar.gz`，旁边生成同名 `.sha256`。对同一个归档名重跑时，内容相同会复用已有文件，内容不同时构建报错。

`builder.lock` 依次记录 Go 版本、Docker 多架构镜像摘要、amd64 和 arm64 镜像摘要。Go 版本和多架构镜像与 SQLite 兼容 CI 一致。构建在 Docker 主机的原生 Linux 架构运行，使用 `GOOS=linux` 交叉编译指定架构的 Go 程序。amd64 采用 `GOAMD64=v1`，arm64 采用 `GOARM64=v8.0`。两个 Go module 都用 `CGO_ENABLED=0`、`-mod=readonly`、`-trimpath` 和 `-buildvcs=false` 构建。归档固定成员顺序、mtime、属主、权限和 gzip 头。 [SQLite 兼容 CI](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/ci/SQLiteFileserver.Dockerfile)

## 验证与安装布局

发布或验收时先核对从发布方取得的归档 SHA-256，再解压到一个新的版本目录。以下命令在归档目录中执行，`ARCHIVE` 为归档文件名，`VERSION_DIR` 为新版本目录。

```sh
sha256sum -c "$ARCHIVE.sha256"
mkdir "$VERSION_DIR"
tar -xzf "$ARCHIVE" -C "$VERSION_DIR"
( cd "$VERSION_DIR" && sha256sum -c SHA256SUMS )
file "$VERSION_DIR"/seafile/bin/*
```

归档根对应版本目录，版本目录直接位于实例根目录下。Controller 由自身路径定位 `seafile/bin`、版本目录内的 `runtime` 和实例根目录下的 `pids`。 [Controller 配置](../../fileserver/cmd/seafilecontroller/config.go)

| 路径 | 内容 |
| --- | --- |
| `seafile/bin/` | `seaf-server-go`、`fileserver`、`seafile-controller`、`seaf-fsck`、`seafserv-gc`、`notification-server` 六个 Linux 可执行文件。 |
| `seafile/share/sqlite/` | 仓库中的 SQLite 初始化 SQL。 |
| `seafile/share/config/` | `seafile.conf` 和 `seafdav.conf` 示例。既有实例继续使用自己的配置，运行所需密钥由部署环境提供。 |
| `seafile/bin/seafile-sqlite` | Linux SQLite 启停、配置预检及离线维护入口。 [启停与回退](SERVICE.md) |
| `BUILD.txt` | 提交、镜像、Go 版本和构建参数，每行一个 `key=value`。 |
| `SHA256SUMS` | 归档内文件的相对路径和摘要。 |

归档提供 Go 服务端组件。Seahub、SeafDAV、其 Python 依赖和部署启动脚本由安装环境提供。存量实例保留原 C 版本目录及其原始二进制，Go 版本使用单独的版本目录。执行回退前按计划关闭写入入口、排空 Go 队列并停止 Go 写入进程，再用原 C 安装目录接管同一份配置和数据。 [安装及回退验收](../../doc/go-migration-sqlite-release.md#用同一份数据完成安装升级和回退)
