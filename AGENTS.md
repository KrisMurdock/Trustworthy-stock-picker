## Agent skills 与本地工作产物

- 如使用本地 Markdown issue 跟踪，工作文件放在 `.scratch/<feature-slug>/`，该目录已被忽略，不提交开发计划、诊断草稿或浏览器截图。
- 架构与行为判断以当前源码为准。`.codegraph/` 仅为本地辅助索引；使用前运行 `codegraph sync .`，不要沿用旧索引的文件数、节点数或调用关系。
- 公开文档入口：[仓库布局](docs/repository-layout.md)、[开发验证](docs/development.md)、[行情来源](docs/data-sources.md)、[环境变量](docs/environment-variables.md)、[部署](docs/deployment.md)、[单股价格策略](docs/单股价格策略.md)。

## 仓库架构摘要

本节按主干代码 `ac1434a` 核对，覆盖多行情源回退、SQLite 行情存储、十年原始日线、网页 K 线和多通道通知。后续修改行为时，应同步核对本节相关说明，不把源码状态等同于实际部署状态。

### 项目定位

`stock-watcher` 是 Go 1.26 原生 CLI 与内嵌只读网页，用于 A 股龙头股池日线同步、技术筛选、短周期策略研究和飞书推送。`go.mod` 声明 Go 1.26.0，CI/镜像使用 Go 1.26.4；SQLite 使用纯 Go 的 `modernc.org/sqlite`，构建关闭 CGO。生产行情与投递状态放在 Windows/CIFS 共享盘，日志可落本地；共享盘不可用时不得回退写入本地行情目录。

### 入口和主流程

- `cmd/stock-watcher/main.go` 是唯一生产入口，支持 `check-config`、`init`、`check`、`sync`、`preview`、`research`、`plan`、`run`、`once`、`status`、`retry` 和 `version`。`cmd/resolve-pool/main.go` 仅用于维护股池。
- `check-config` 校验配置；`init` 显式初始化 `market.db`；`check` 检查存储、日历及投递状态并列出启用行情源，不探测远端服务可用性。`preview`/`research` 从本地历史计算，`plan` 按实际买入价生成交易计划。
- `run` 经 `cmd/stock-watcher/services.go` 先绑定网页端口，再启动监控；首次启动即刷新全股池，不因已有报告跳过。刷新成功后进入 `Cycle`，处理默认北京时间 18:05 调度、T+1 目标交易日和通知重试。首次刷新失败按轮询间隔重试，不能发送缓存推荐；默认轮询 5 分钟，失败重试间隔至少 1 分钟。
- 行情路径：`config.Load` → `calendar` 确认完成日/目标日 → `monitor.Sync` → `market.Sources.Attempt` 按源尝试完整窗口 → 日历、复权与历史覆盖校验 → `storage.MarketDB` 事务保存行情、隔离原因和同步状态。达到推荐覆盖率才构建并保存报告。
- 通知路径：`selection.BuildWithExecution` 的报告 → `internal/notify/report` 生成消息 → `notify.Ledger` 按事件/通道排队 → `notify.Channel.Send` → 飞书实现；账本持久化到 `state/delivery.json`。`sync` 只同步和保存报告，`run`/`once` 才执行调度与投递。

### 内部模块边界

- `internal/config`：严格 YAML 字段校验、默认值、相对配置路径、股池 `listed_from`、行情源、网页、策略及通知通道配置；凭证由指定环境变量或私有通知凭证文件加载。
- `internal/calendar`：北京时间交易日、节假日、已完成交易日、T+1 和推送过期窗口；超出配置日历范围时报错。
- `internal/market`：统一 `Source.Fetch` 接口与 `Sources` 回退编排；腾讯、BaoStock、Tushare、东方财富均为 Go 原生客户端。负责协议、分页、复权、请求节流、超时/取消、元数据与数值校验；调用方补充日历与完整覆盖校验。
- `internal/monitor`：整池同步、十年历史与分析窗口分离、不复权增量/全量回补、覆盖率门槛、启动刷新、定时调度、账本迁移、投递重试和过期清理。
- `internal/indicators`：MA、MACD、RSI 等指标计算；`internal/selection` 保留趋势筛选并组装报告，按配置切换短周期策略，负责入选、排除和报告事件 ID。
- `internal/strategy`：前复权特征、不复权成交模拟、费用/滑点与交易计划、短周期机会排名、滚动验证和独立留出样本。机会分不是成功概率；研究结论以报告中的限制与验证结果为准。
- `internal/storage`：`Store` 管理 CIFS 检查、单写进程锁、安全路径及原子 JSON 状态文件；`MarketDB` 管理 SQLite 行情、同步窗口、报告事务和一致性读取。`dashboard.go` 为网页提供摘要、历史报告及最新有效日线视图。
- `internal/dashboard`：Go `embed` 打包原生 HTML/CSS/JavaScript，包含只读看板、SVG 日 K 和本地仓位计算器，无独立前端进程或外部图表 CDN。
- `internal/notify`：版本化消息、通道接口、错误分类及多事件/多通道投递账本；`internal/notify/report` 转换报告。`internal/feishu` 实现卡片、HMAC-SHA256 签名、HTTP/业务码确认和可重试/永久错误分类。
- `internal/logging`：统一调用位置、凭证脱敏、stdout/滚动文件双通道、文本/JSON 格式和压缩轮转；新增日志复用该模块。

### 行情源与历史覆盖契约

- 默认顺序为 `tencent → baostock → tushare → eastmoney`，可通过 `market.providers` 调整。东方财富是直接 HTTP 上游，不是运行 AKShare；BaoStock 直接使用 TCP 协议，不依赖 Python SDK 或子进程。
- Tushare Token 仅从 `market.tushare_token_env` 指定的环境变量读取，默认 `TUSHARE_TOKEN`。缺 Token 时跳过该源；只配置 Tushare 且无 Token 时配置失败。默认单请求超时 10 秒、每源请求间隔 1 秒，间隔最小 350 毫秒。
- 网络/供应商错误以及 HTTP 成功但窗口校验失败均可触发备用源；调用者取消、上下文截止或存储错误不启动下一来源。所有来源失败后，仅当错误全部可隔离时跳过单股；未被备用源恢复的网络/限流等错误中止本轮发布。
- 每只有效股票保存至少最近十个自然年的不复权日线；若 `listed_from` 晚于请求起始日，则从该上市日期起校验，不虚构上市前数据。`daily.history_days` 控制分析窗口，默认 365，允许 90–7300 天；原始窗口取十年与分析窗口中更长者，与是否启用策略无关。
- 前/后复权分析窗口每轮完整刷新，不跨来源或跨轮拼接复权价格。不复权增量仅在旧历史已覆盖目标范围、来源/单位/字段一致且重叠日线完全一致时合并；旧短窗口、来源切换、重叠修订或断档会完整回补。
- 腾讯单次 640 根限制只约束分页请求，单个 `History` 窗口最多 6000 根，已入库的更早日线不主动删除。十年前边界可保留前一根日线作覆盖锚点；配置日历覆盖区间内逐日核对，不把更早的工作日当作交易日，不补造停牌日线。缺口、滞后或历史不足仍可能隔离股票，多源回退不会绕过校验。
- 复权分析窗口和原始历史分别回退、分别记录来源；同一窗口不混源、不平均价格。`History` 保持 `schema_version: 1`、`volume_unit: provider_raw`，入库另存按来源换算的股数；不能将不同来源的原始成交量直接混用。

### 存储、覆盖率与投递约束

- `require_cifs: true` 是生产默认。先挂载共享盘，再执行 `init`；普通写入命令不会创建缺失的数据库，也不得把 `data` 自动创建为本地替代目录。
- 行情主存储为 `<data_dir>/market.db`，SQLite schema 2；表包括 `stock`、`daily_bar`、`stock_sync`、`sync_run`、`selection_run` 和 `selection_result`。写入使用 `journal_mode=DELETE`、`synchronous=FULL`、外键及 5 秒 busy timeout；价格保存为百万分之一元整数。
- 整池遍历结束后事务落库；不逐股发布，不保存分页未完成的历史。默认 `sync.min_coverage_pct: 95`：低于门槛仍保存有效行情和隔离原因，但不生成本轮推荐报告；通过门槛时行情和报告同事务保存。中途非隔离错误或事务失败时保留已有行情，不能把旧报告标为本轮成功。
- 数据库读取按已保存窗口校验条数、来源与复权字段。旧 `daily/`、`execution/`、`reports/` JSON 仅供无库时的只读兼容和包级旧调用；CLI 生产同步始终要求 SQLite，不以旧 JSON 写入路径兜底。
- 行情 JSON、配置、研究/计划各自有版本字段；趋势报告为 schema 1，短周期策略报告为 schema 2；投递账本为 schema 2，可迁移旧单条状态。不要把所有数据统一视为 schema 1。
- 通知使用 `notify.channels`，当前实现飞书，可配置多个命名通道并兼容旧 `feishu` 配置。投递为至少一次语义，崩溃后未确认的发送可能重复；记录各通道的 pending/in_flight/delivered/blocked/expired/unknown 状态与重试信息，使用 `retry` 解锁永久错误，过期终态记录保留 30 天后清理。
- 配置、股池、行情源和交易日历修改后需重启。示例日历仅覆盖 2026 年，过期前必须更新；所有私有配置、共享盘数据、日志和凭证禁止提交。

### 网页与用户可见状态

- 默认 `web.enabled: true`、监听 `127.0.0.1:8080`；`STOCK_WATCHER_WEB_ADDR` 可覆盖。应用路径为 `/`，不支持子路径挂载；代理应保留 `/api/`、`/assets/` 与 `/calculator`。网页无账号权限系统，远程访问由受控网络或已有认证代理保护。
- 接口只允许 GET/HEAD：`/healthz`、`/api/dashboard`、`/api/reports?as_of=YYYY-MM-DD`、`/api/history?code=sz000001`。历史接口只允许股池内股票，可用 `adjust=none` 看原始历史；`adjust` 仅接受配置口径或 `none`，不接受任意日期参数。
- 看板每 60 秒刷新，支持股票名称/代码搜索、行业/数据状态筛选、排序和历史报告。`report_current` 表示推荐是否对应当前行情；已保存有效行情可独立于推荐报告展示，不能把覆盖率失败解释为本地一定没有数据。
- “查看已同步股票”可筛出有行情的股票；点击股票名称查看日 K、成交量、MA5/10/20 和每日开高低收。支持最近 60/120/250 根或全部，复权分析窗口与不复权长期历史可切换，显示实际起止日期、根数、来源及成交量单位；长历史横向滚动，滑块定位日期。
- 股池表优先用统一股数展示/排序成交量，日 K 保留来源原始股/手单位。缺失/隔离股票不以旧日线冒充当前有效数据；切换时取消旧请求，错误和超时清空旧图并提供重试。
- `/calculator` 仅在浏览器内计算仓位、已有持仓、剩余风险与净止盈价；不上传或持久化账户输入，不依赖行情接口。网页刷新不会触发后台同步或通知。

### 常用开发和部署命令

```bash
make fmt-check            # Go 格式检查
make vet                  # go vet ./...
make test                 # go test -race ./...
make build                # bin/stock-watcher，嵌入当前 Git HEAD
make smoke                # CLI、启动脚本与仓位计算模型检查
make sources-smoke        # 本地 HTTP/TCP 模拟行情源集成验证
make dashboard-smoke      # Chromium：真实 CLI、DOM/URL/网络与错误恢复
make image                # 构建同一 Go CLI 与内嵌网页的 Alpine 镜像
make docker-smoke         # 容器 CLI 冒烟
make docker-dashboard-smoke # 容器 + 同一套浏览器验证
make ci                   # fmt/vet/race/build/smoke/sources-smoke/package
make package              # Linux 发布包；BUILD_GOARCH 可选 amd64/arm64
./bin/stock-watcher check-config -config config.yaml
./bin/stock-watcher init -config config.yaml   # 首次部署，先确认共享盘已挂载
./bin/stock-watcher check -config config.yaml
./bin/stock-watcher sync -config config.yaml
./bin/stock-watcher preview -config config.yaml -as-of YYYY-MM-DD
./bin/stock-watcher research -config config.yaml -as-of YYYY-MM-DD
./bin/stock-watcher plan -config config.yaml -code sz000001 -entry-price 10
./bin/stock-watcher retry -config config.yaml feishu-main
```

- `make run` 调用 `scripts/run.sh`：构建、加载仓库私有 `.env` 后前台运行；必要时接管同仓库同配置的 systemd 实例。直接运行二进制和 `make check/status` 不自动加载 `.env`；不要为只读检查启动 `run`。
- systemd 与容器都执行同一 `stock-watcher run`。Compose 将共享盘映射到 `/data`、日志到 `/logs`，容器监听 `0.0.0.0:8080`，宿主机默认仅绑定 `127.0.0.1:8080`；运行变量由 `STOCK_WATCHER_RUNTIME_ENV` 指定文件注入，路径挂载禁止自动创建宿主目录。
- `make ci` 与现有 GitHub Actions 不包含浏览器验证；改动前端应另跑 `dashboard-smoke`，生产容器形态另跑 `docker-dashboard-smoke`。浏览器测试需要 Python Playwright 与 Chromium，均为测试依赖，不属于生产运行时。
- 测试使用临时 SQLite 与模拟 HTTP/TCP、关闭通知，不连接真实行情或飞书；界面验收覆盖空数据、有效行情、覆盖率不足、缺失股票、网关拒绝、错误/超时、取消恢复及移动布局。新增行情源或修改存储时还须覆盖来源一致性、量纲、增量边界和共享盘故障。
- 本地资源紧张可用 `GOFLAGS=-p=1 make test`。正式构建须记录提交 SHA/镜像标识，并核对二进制版本、实际静态资源和部署 URL；代码合并/推送或本地测试通过不能替代服务更新与部署验收。
