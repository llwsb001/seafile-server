# Seafile Server

交流和书写优先中文。Python 使用 `uv`，TypeScript 使用 `bun`。

- 兼容验收。涉及 C → Go、对象格式、SQLite 或协议等价时，查阅相邻仓库 `../seafile-compat-tests/`。其中的 `fixtures/v1`、`versions.lock` 和 `results/baseline-report.md` 分别记录冻结样本、C 基线版本和历史验证结果。验收工作按 [迁移计划](doc/go-migration-plan.md) 和 [测试说明](.aew/standards/testing.md) 执行。
- Seahub。涉及 Seahub 页面、API 或调用方行为时，查阅相邻仓库 `../seahub/`。Seahub 修复在独立维护的 fork 提 PR，目标分支为 `master`。锁定消费者的兼容回放仍按兼容仓库的版本锁执行。 [llwsb001/seahub](https://github.com/llwsb001/seahub)
- 架构与改动范围。查阅 [架构](.aew/context/architecture.md)、[模块](.aew/context/modules.md) 或 [术语](.aew/context/glossary.md)。
- 构建与交付。查阅 [编码](.aew/standards/coding.md)、[安全](.aew/standards/security.md)、[Git](.aew/standards/git.md) 和 [文档](.aew/standards/documentation.md)。
- 任务与已知问题。查阅 [工作约定](.aew/workflow.md) 或 [经验](.aew/lessons/index.md)。
