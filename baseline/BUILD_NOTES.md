# C Baseline 构建记录（sqlite3 + C fileserver）

环境：Ubuntu 22.04，4 核 7G，无 docker。全部源码编译，安装到 `/usr/local`。

## 依赖

```bash
sudo apt-get install -y build-essential autoconf automake libtool libtool-bin pkg-config \
  valac libglib2.0-dev libevent-dev libjansson-dev libsqlite3-dev libssl-dev uuid-dev \
  zlib1g-dev libcurl4-openssl-dev libarchive-dev libargon2-dev libjwt-dev libmysqlclient-dev \
  libhiredis-dev libfuse-dev gettext sqlite3
```

## 构建顺序与坑

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

上游 `ci/run.py` 的 `main()` 硬编码 `dbs = ('mysql',)`。这里加 `ci/run_sqlite_tests.py` 复用 `start_and_test_with_db('sqlite3')`，走 `c_fileserver`：

```bash
PYTHONPATH=$PWD:$PYTHONPATH LD_LIBRARY_PATH=/usr/local/lib:$LD_LIBRARY_PATH \
  python3 ci/run_sqlite_tests.py
```

注意 `py.test` 解析到 `/usr/local/bin/py.test`（python3.10），所以 `requests_toolbelt`、`tenacity` 要装进 3.10；`sqlite3` CLI 必须在 PATH 里，否则建库阶段静默失败（现象是 ccnet 没有 socket、RPC 全超时）。

## 待解

- 首轮测试 117 个 error，全部是 pysearpc 找不到 `seafserv-threaded-rpcserver` socket，正在定位（ccnet-server 是否正常起来 / conf 目录是否被 serverctl 覆盖）。
