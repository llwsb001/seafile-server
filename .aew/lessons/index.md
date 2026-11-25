# 已知问题

- SQLite 配置必须选中 `type = sqlite`，依赖 `sqlite3` CLI 初始化测试库。基线配置及踩坑记录见 [构建说明](../../baseline/BUILD_NOTES.md) 和 [serverctl](../../ci/serverctl.py)。
- `fixtures/v1/storage.tar` 随验收环境提供。新版校验入口在归档缺失时返回失败。 [样本验收](../standards/testing.md)
