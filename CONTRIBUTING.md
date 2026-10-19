# 贡献指南

感谢参与 Stock Watcher。这个项目同时涉及行情采集、数据完整性、交易研究、网页和部署，提交前请优先保证行为可复现、数据可审计、凭证不泄漏。

## 开始之前

- 问题、使用疑问和功能建议通过 GitHub Issues 讨论。
- 较大改动先开 Issue 说明目标、兼容范围和验证方式，避免直接提交大规模重构。
- 安全漏洞、Token、Webhook、共享盘路径和真实账户信息不要发到公开 Issue。发现凭证误提交时，先撤销凭证，再处理 Git 历史。
- 本项目用于研究和数据工程，不接收以“保证收益”或“实盘荐股”为目标的改动。

## 开发环境

基础环境：

- Linux、Go 1.26.4+、Git、GNU Make、Bash、`jq`。
- Docker Engine 与 Compose ≥ 2.30.0，用于镜像和容器验证。
- Python 3.12、Playwright Chromium，用于浏览器测试。
- Node.js 20+，用于仓位计算模型对照测试。

生产配置通过 `.env` 环境变量加载，日常开发不需要创建 `config.yaml`。观测股池独立保存在 `leaders.yaml`：

- `leaders.yaml` 可以提交经过核对的股池修改。
- `leaders.pending.yaml` 保存待人工确认名单，不参与启动或选股。
- `.env`、行情数据库、日志、截图和临时研究产物不得提交。
- 本地测试需要临时关闭通知时设置 `STOCK_WATCHER_FEISHU_ENABLED=false`。
- 本地测试可使用 `STOCK_WATCHER_REQUIRE_CIFS=false`，生产环境必须保持 `true`。

## 工作流

1. Fork 仓库，或从最新 `main` 创建独立分支。
2. 只修改与目标相关的文件，保持最小改动。
3. 为行为变化补充回归测试；修复数据、存储或调度问题时说明失败场景。
4. 运行适用的验证命令，并记录实际结果。
5. 提交 PR，说明用户可见行为、兼容性、验证环境和未验证项。

分支名称建议：

```text
feat/<scope>-<summary>
fix/<scope>-<summary>
refactor/<scope>-<summary>
docs/<scope>-<summary>
```

## 提交信息

提交标题使用：

```text
<type>(<scope>): <真实改动摘要>
```

常用类型：`feat`、`fix`、`refactor`、`docs`、`test`、`build`、`chore`。

提交正文说明：

- 本次实现的主要逻辑。
- 相对之前版本新增、修改和删除了什么。
- 执行的验证命令与结果。
- 需要部署、迁移或回滚时的注意事项。

不要使用空泛的 `update`、`modify` 或递归 `fixup!` 作为最终提交信息。

## 验证矩阵

| 改动范围 | 至少验证 |
| --- | --- |
| 文档、README、链接 | `git diff --check`；确认本地链接存在 |
| Go 逻辑、配置、存储、调度 | `make fmt-check vet test smoke`，必要时 `make sources-smoke` |
| 分钟采集、K 线接口 | `make intraday-smoke` |
| 前端页面、导航、静态资源 | `make dashboard-smoke` |
| Dockerfile、Compose、systemd | `make image docker-smoke`，涉及页面时加 `make docker-dashboard-smoke` |
| 发布或合并前 | `make ci`；记录提交 SHA、镜像标识或包校验和 |

完整 CI 入口：

```bash
make ci
make dashboard-smoke
make docker-smoke
make docker-dashboard-smoke
```

资源紧张时可以使用：

```bash
GOMAXPROCS=2 GOFLAGS=-p=2 make ci
```

测试不得依赖真实行情、真实飞书群或生产写入目录。涉及 CIFS、Windows 隧道、认证代理或部署 URL 时，如果本地无法验证，必须在 PR 中明确写出“本地验证通过，部署后未验证”及待执行命令。

## 代码与数据约束

- 保持 Go 原生运行时，不引入 Python、Node.js、旧 CLI 或子进程作为生产兼容层。
- 行情源必须保持统一的 `Source.Fetch` 契约，把协议、分页、复权和错误分类放在 `internal/market`。
- 行情、报告和同步状态通过 SQLite 事务保存；不能退回逐股发布或本地磁盘兜底。
- 同一数据目录只允许一个写入实例。共享盘不可用时停止写入，不自动创建本地 `data` 目录。
- 新增配置优先使用内置默认值和 `STOCK_WATCHER_*` 环境变量；不要恢复启动时依赖 `config.yaml`。
- 日志使用统一 `internal/logging`，禁止拼接时间、级别、颜色或输出 Token、Secret、Webhook。
- 跨语言或跨版本数据必须版本化，并明确缺失字段、空值和校验失败行为。
- 修改数据 schema、投递账本或交易日历时，必须提供迁移、回滚和兼容性说明。

## PR 检查清单

- [ ] 改动范围单一，没有混入无关格式化或生成文件。
- [ ] 已补充或更新测试、文档和示例命令。
- [ ] 已运行适用的验证命令，并在 PR 中写明结果。
- [ ] 未提交凭证、行情数据、日志、浏览器截图或本地计划产物。
- [ ] 涉及部署时写明提交 SHA、镜像标识、部署后验收项和回滚方式。
- [ ] 涉及前端时提供真实浏览器断言，不以接口 200 代替页面验收。
- [ ] 涉及权限或远程访问时说明认证代理边界，不虚构应用内账号角色。

请同时使用仓库的 [PR 模板](.github/pull_request_template.md) 描述改动、验证和回滚。

## 许可证

提交贡献即表示你同意该贡献按本仓库的 [Apache License 2.0](LICENSE) 授权。
