# 已知问题

- SQLite 配置必须选中 `type = sqlite`，依赖 `sqlite3` CLI 初始化测试库。基线配置及踩坑记录见 [构建说明](../../baseline/BUILD_NOTES.md) 和 [serverctl](../../ci/serverctl.py)。
- `fixtures/v1/storage.tar` 在兼容仓库中由验收环境提供。核对对象数量可以发现归档缺失造成的零对象校验。 [样本验收](../standards/testing.md)
