# 术语

- 库（repo 或 library）是同步和分享的单位。`master` 是当前服务读库头时查询的内部数据分支。
- 虚拟库（virtual repo）引用源库的子目录和存储。`repo_id` 指向逻辑库，`store_id` 用于定位 FS 和块。
- FS 对象描述文件或目录。文件对象有有序块列表，提交的 `root_id` 指向根目录对象。
- Searpc 是 Seahub 的 Python 绑定和 Go fileserver 调用核心进程的本地 RPC 协议。 [客户端](../../fileserver/searpc/searpc.go)
