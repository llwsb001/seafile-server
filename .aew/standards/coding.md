# 编码与构建

- 优先用 `rg` 搜索内容、用 `fd` 查找文件。可用时优先使用 `fzf`、`duckdb`、`git-delta`、`xh`、`watchexec`、`just` 和 `semgrep` 完成相应任务。
- C 构建由 Autotools 管理，入口为 [`configure.ac`](../../configure.ac) 和 [`Makefile.am`](../../Makefile.am)。SQLite 的 Linux 构建记录见 [基线说明](../../baseline/BUILD_NOTES.md)。
- Go 模块以各目录的 `go.mod` 为准。Go 检查从改动所属模块运行。 [fileserver](../../fileserver/go.mod) · [notification-server](../../notification-server/go.mod) · [Go CI](../../.github/workflows/golangci-lint.yml)
