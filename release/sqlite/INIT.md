# 初始化 Linux SQLite 实例

用服务账号在实例根目录下新建版本目录，校验候选归档的 SHA-256 后解包到该目录。在版本目录的 `seahub/` 安装锁定的 Seahub 源树，准备其 Python 解释器和依赖，并使 `PYTHONPATH` 能加载该版本所需的 `seaserv` 与 `pysearpc`。 [组件版本锁](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/versions.lock) · [Seahub Python 依赖](https://github.com/llwsb001/seafile-compat-tests/blob/dd673840a161d7ebd36745b7c3ae085ca6760ba0/fixtures/v7/seahub-requirements.lock)

```sh
( cd "$VERSION_DIR" && sha256sum -c SHA256SUMS )
"$SEAHUB_PYTHON" "$VERSION_DIR/init.py" "$VERSION_DIR"
```

初始化在版本目录的父目录创建共享的 `conf/`、`seafile-data/`、`ccnet/`、`seahub-data/`、`logs/` 和 `pids/`，并在版本目录下创建 `runtime/`。脚本复制候选的配置样例，从归档 SQL 创建库数据库及四个 ccnet 数据库，并运行 Seahub 的 `manage.py migrate --run-syncdb --noinput` 生成 `seahub.db`。生成的 `conf/seahub_settings.py` 使用单独的 Django 密钥并指向实例的 SQLite 文件。

脚本逐个构建并核验临时文件，再发布完整文件。重复初始化保留已有配置和数据库。缺少归档 SQL 或 Seahub、Python 依赖不全、路径不可写或迁移失败时，补齐环境后可直接重跑。对正在运行的实例，按部署流程安排启动和升级。

初始化后由部署环境启动 Go controller 和持有同一 `JWT_PRIVATE_KEY` 的 Seahub。用 Seahub 的 Python 环境运行锁定的 `seahub/scripts/check_init_admin.py` 来创建首个管理员。执行前确认核心 RPC 已就绪，并设置实例的 `SEAFILE_CENTRAL_CONF_DIR`、`SEAFILE_DATA_DIR`、`CCNET_CONF_DIR` 和版本目录下 `runtime/` 对应的 `SEAFILE_RPC_PIPE_PATH`。随后通过正常的 Seahub 入口登录。 [首个管理员脚本](https://github.com/haiwen/seahub/blob/62ab79b210c45c77c32f3fc0d7eeaf68c710f5ab/scripts/check_init_admin.py)
