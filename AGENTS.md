## Agent skills

### Issue tracker

Issues live as markdown files under `.scratch/<feature-slug>/` in this repo (local-markdown convention). See `docs/agents/issue-tracker.md`.

### Triage labels

Default label vocabulary — all five canonical roles use their role name as the label string. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context layout: one `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.

## 仓库架构摘要

本节是给新 coding agent 的快速上下文，基于当前工作树的源码阅读和 `codegraph` 索引生成（索引统计：26 个文件、421 个节点、1,039 条边；`.codegraph/` 已被 `.gitignore` 忽略）。代码变更后如需更新关系，先运行 `codegraph sync .`，再用 `codegraph files`、`codegraph query <符号>`、`codegraph explore '<主题>'` 或 `codegraph callers/callees <符号>` 复查。

### 项目定位

`stock-watcher` 是 Go 1.26 原生 CLI，用于 A 股龙头股池的日线同步、技术规则筛选和飞书机器人推送。生产数据目录通常是挂载的 Windows/CIFS 共享盘；程序拒绝在生产配置下回退到本地磁盘。

### 入口和主流程

- `cmd/stock-watcher/main.go` 是唯一生产入口，支持 `check`、`sync`、`preview`、`run`、`once`、`status`、`retry` 和 `version`。它负责加载 YAML 配置、创建日志、响应 SIGINT/SIGTERM，并组装各内部模块。
- `internal/monitor` 是应用编排层。`Sync` 逐个下载完整股池，全部成功后调用筛选并写入报告；`Cycle` 处理 18:05 调度、T+1 交易日、持久投递状态、重试和过期；`Run` 按轮询间隔循环调用 `Cycle`。
- 正常数据路径为：`config.Load` → `calendar` 判定已完成交易日和下一个交易日 → `market.Client.Fetch` 请求腾讯日线 → `storage.Store.Write` 保存 `daily/<as-of>/<code>.json` → `selection.Build` 计算候选和报告 → 保存 `reports/<as-of>.json` → `feishu.Client.Send` 推送；发送成功后更新 `state/delivery.json`。
- `cmd/resolve-pool/main.go` 是维护工具，用于辅助核对股票代码和名称，不属于生产运行链路。

### 内部模块边界

- `internal/config`：YAML 反序列化、默认值和严格校验；股票代码格式、路径、时间、阈值、环境凭证均在这里约束。
- `internal/calendar`：北京时间日期、交易日/节假日、完成日、T+1 和推送过期窗口；日历覆盖范围不足时应报错。
- `internal/market`：腾讯 HTTP 行情协议、qfq/hfq/day 字段解析、日线元数据校验、超时和可重试错误；`Source` 接口由 `monitor` 依赖。
- `internal/selection`：基于同一窗口复权收盘价和原始成交量计算 MA、MACD、RSI、连涨和量比，过滤无效股票后按分数和代码排序取 Top N；`Build` 产出带固定事件 ID 的 `Report`。
- `internal/storage`：检查 CIFS/SMB 文件系统、单进程锁、路径穿越和软链接、原子临时文件写入、fsync、重命名和只读视图；共享盘故障时保留已有数据和待发送状态。
- `internal/feishu`：卡片构造、官方 HMAC-SHA256 签名、HTTP 200 且业务码为 0 的确认，以及 429/5xx/网络错误重试和 401/403/签名错误阻塞；`Sender` 接口由 `monitor` 依赖。
- `internal/logging`：统一文本/JSON 日志、真实调用位置、敏感字段脱敏、stdout 与滚动文件双通道，以及按大小/天数/份数压缩轮转。

### 关键运行约束

- `sync` 必须先完成整个股池下载，任一股票网络或格式错误都会阻止报告和推送，禁止生成部分推荐。
- `require_cifs: true` 是生产默认；`data` 不应被程序创建为普通本地目录。写入使用 `schema_version: 1` 的 UTF-8 JSON、ISO 日期和 RFC3339Nano 时间。
- 投递是至少一次语义：写入确认前进程退出可能重复发送；`state/delivery.json` 保存 pending、尝试次数、下一次重试时间和永久错误阻塞状态。修正永久配置后用 `retry` 解锁。
- 配置、股池和交易日历修改后需重启生效。2026 日历过期前必须更新 `config.yaml`/示例配置；不要把 webhook、secret 或私有配置提交到 Git。

### 常用开发和部署命令

```bash
make test                 # go test -race ./...
make vet                  # go vet ./...
make build                # bin/stock-watcher，写入版本提交 SHA
make image                # 构建同一 CLI 的 Alpine 镜像
./bin/stock-watcher check -config config.yaml
./bin/stock-watcher sync -config config.yaml
./bin/stock-watcher preview -config config.yaml -as-of YYYY-MM-DD
```

容器和 systemd 都执行同一个 `stock-watcher run`；生产必须先挂载 Windows 共享盘，再映射 `/data`，并通过环境文件注入飞书凭证。改动日志、存储、配置或投递逻辑时，优先补充对应 `internal/*/*_test.go` 的契约测试，并验证取消、超时、配置缺失、共享盘不可用和投递重试路径。
