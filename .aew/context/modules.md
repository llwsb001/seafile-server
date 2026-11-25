# 模块

| 位置 | 职责 |
| --- | --- |
| `server/`、`common/` | C 主进程、RPC、数据库、对象格式、库操作与 C HTTP |
| `fileserver/` | Go HTTP 服务及其数据库、存储、同步和 Searpc 客户端包 |
| `notification-server/` | Go 通知服务，独立 Go 模块 |
| `controller/`、`server/gc/`、`fuse/` | 进程管理、垃圾回收、检修和可选挂载 |
| `python/`、`lib/` | Seahub 使用的 Python RPC 封装及 C 构建用的对象类型和辅助库 |
| `ci/`、`tests/` | 服务回归入口、启动配置和 Python 集成测试 |
