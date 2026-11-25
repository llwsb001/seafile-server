# C Baseline 构建记录（sqlite3 + C fileserver）

环境：Ubuntu 22.04，4 核 7G，无 docker。

**安装位置：`~/opt/seafile-baseline`（用户目录），不装 `/usr/local`。** 容器随时回收，`/usr/local` 与 apt 状态都会丢；恢复方式是一条 `scripts/bootstrap.sh`（见 `llwsb001/seafile-compat-tests`）。已验证：清空 `/usr/local` 依赖（PATH 只留 `prefix/bin:/opt/go/bin:/usr/bin:/bin`、`LD_LIBRARY_PATH` 只留 prefix）时，测试仍是 **117 passed**。

## 依赖

```bash
sudo apt-get install -y build-essential autoconf automake libtool libtool-bin pkg-config \
  valac libglib2.0-dev libevent-dev libjansson-dev libsqlite3-dev libssl-dev uuid-dev \
  zlib1g-dev libcurl4-openssl-dev libarchive-dev libargon2-dev libjwt-dev libmysqlclient-dev \
  libhiredis-dev libfuse-dev gettext sqlite3
```

## 构建顺序与坑

下列命令记录最初使用 `/usr/local` 的构建过程。当前基线用兼容仓库的 `scripts/bootstrap.sh` 安装到用户目录。

1. **libsearpc**（上游 master，`3a45486`）：`./autogen.sh && ./configure --prefix=/usr/local --disable-static && make -j4 && sudo make install`
2. **hiredis**：Ubuntu 22.04 只有 0.14.1，seafile-server 要求 >= 0.15.0。从源码装 1.6.0（`redis/hiredis` master，`make && sudo make install`）。
3. **libevhtp**（haiwen fork）：OpenSSL 3 下 `evhtp.c` 因 `evhtp_ssl_sess_t` 不完整类型编不过。基线用 `-DEVHTP_DISABLE_SSL=ON -DEVHTP_BUILD_SHARED=ON`，只出 `libevhtp.so`，因此基线只跑 HTTP，不跑 HTTPS。上游没有 `.pc` 文件，configure 时需显式给 `CPPFLAGS=-I/usr/local/include LDFLAGS=-L/usr/local/lib`。
4. **ccnet-server**（上游 master `4ae8ea2`）：它的 `ccnet-db.c` 里有 `my_bool`，MySQL 8 客户端头文件已删除该类型，编译失败。基线是 sqlite，处理方式是把 `mysqlclient.pc` 临时移出 pkg-config 路径，configure 不启用 MySQL：
   ```bash
   sudo mv /usr/lib/x86_64-linux-gnu/pkgconfig/mysqlclient.pc /tmp/mysqlclient.pc.off
   ./configure --prefix=/usr/local --disable-static --disable-python
   ```
   代价：基线二进制不含 MySQL 支持。要 MySQL 时把 `.pc` 移回并重新 configure。
5. **seafile-server**：同样隐藏 `mysqlclient.pc`，`./configure --prefix=/usr/local --disable-static --disable-fuse CPPFLAGS=-I/usr/local/include LDFLAGS=-L/usr/local/lib`。

## 跑测试（sqlite）

在 Linux 上完成基线构建后，从产出 `server/seaf-server` 的源码仓库根目录运行。以下命令假设兼容仓库位于相邻目录，且基线使用 `/opt/python3.12/bin/python3`，与其 `bootstrap.sh` 生成的 `py.test` 启动脚本一致。

```bash
source ../seafile-compat-tests/scripts/env.sh
export SEAFILE_INSTALL_PREFIX="$SEAFILE_PREFIX"
uv pip install --python /opt/python3.12/bin/python3 \
  --target "$SEAFILE_PREFIX/lib/python3.12/site-packages" -r ci/requirements.txt
uv run --no-project --python /opt/python3.12/bin/python3 python ci/run_sqlite_tests.py
```

`ci/run_sqlite_tests.py` 自行启动测试实例，并使用源码树中的 `server/seaf-server`。安装前缀提供 `ccnet-server`、`py.test` 和动态库。建库还需要 PATH 中的 `sqlite3`。

## 已解：sqlite 模式下 serverctl 的两个坑

1. `add_seafile_sqlite_db_conf()` 只写了空的 `[database]`，C 端读不到 `type`，seaf-server 启动即失败（`seafile-session.c:153` 后报 `Failed to create seafile session`）。补上 `type = sqlite` 后正常。
2. `sqlite3` CLI 不在 PATH 时，建库静默失败，现象是 socket 不存在、全部 RPC 超时。

修正后：**117 passed / 0 failed，耗时 234s**（`c_fileserver` + sqlite）。
