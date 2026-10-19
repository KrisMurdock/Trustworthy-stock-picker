# AGENTS.md

本文件按主干提交 `3aae1de`（2026-10-19）核对，描述当前源码的运行契约、模块边界和验证入口。行为判断以源码为准；文档与源码不一致时，先核对源码，再同步修正文档。不要把源码状态等同于线上部署状态。

## Agent 工作约定

- 如使用本地 Markdown issue 跟踪，工作文件放在 `.scratch/<feature-slug>/`；该目录及浏览器截图、诊断草稿不提交。
- 架构与行为判断以当前源码为准。`.codegraph/` 只是本地辅助索引；使用前运行 `codegraph sync .`，不要沿用旧索引的文件数、节点数或调用关系。
- 优先做最小、聚焦的改动，保持现有模块边界和中文文档风格；不要为修一个问题顺手重构无关代码。
- 不提交凭证、Token、`.env`、私有 `feishu.config`、行情数据库、日志、发布包或浏览器截图。
- 涉及行为变化时同步更新对应文档和测试；涉及网页、登录边界、静态资源或网关路径时，必须在真实浏览器中验证，不能只用源码搜索或接口 200 代替。

## 公开文档入口

- [仓库布局](docs/repository-layout.md)
- [开发、验证与发布](docs/development.md)
- [行情来源与回退](docs/data-sources.md)
- [环境变量](docs/environment-variables.md)
- [部署、升级与排障](docs/deployment.md)
- [算法接入契约](docs/algorithm-interface.md)
- [推荐门槛诊断](docs/strategy-diagnostics.md)
- [策略实验](docs/experiments.md)、[实验快照](docs/experiment-result-snapshots.md)
- [单股价格策略](docs/单股价格策略.md)、[底部形态研究](docs/bottom-signals.md)
- [测试范围](tests/README.md)、[Compose 部署](docker/README.md)

## 项目定位

`stock-watcher` 是用纯 Go 构建的 A 股研究型行情引擎和只读网页。它从多个公开行情源同步龙头股池，校验交易日、复权窗口、上市日期、停牌状态和十年历史完整性，把行情、报告、实验和投递状态事务化保存到 Windows/CIFS 共享盘上的 SQLite，并生成下一交易日的观察排名、短周期研究和飞书通知。

- 生产链路只有一个原生二进制：行情下载、校验、指标计算、策略研究、网页服务和消息投递都在同一进程内完成。
- 运行时不依赖 Python、Node.js、AKShare 或独立前端服务；网页资源通过 `go:embed` 打进二进制。
- `go.mod` 声明 Go 1.26.0；CI 和镜像使用 Go 1.26.4。
- SQLite 使用纯 Go 的 `modernc.org/sqlite`，构建时 `CGO_ENABLED=0`。
- 生产行情与投递状态位于 CIFS；共享盘不可用时必须停止写入，不允许回退到服务器本地行情目录。
- 项目用于数据工程和量化研究，不连接券商、不自动下单，也不把机会分或历史统计表述为成功概率、收益承诺或投资建议。

## 入口与命令

### 二进制入口

- `cmd/stock-watcher/main.go`：唯一生产入口，负责参数校验、命令编排、存储和网页生命周期。
- `cmd/stock-watcher/services.go`：`run` 的 HTTP 服务与监控循环生命周期；先绑定网页端口，再启动监控，任一循环退出会取消另一条。
- `cmd/stock-watcher/experiment.go`：实验和保留区间验证命令。
- `cmd/stock-watcher/calendar.go`：为实验快照冻结交易日历，优先复用 `calendar.db`，必要时请求 BaoStock/Tushare 并写回缓存。
- `cmd/bottom-study/main.go`：离线研究工具，读取不可变实验快照，输出确定性 JSON；不会打开实时行情库。`-archive` 仅用于保存速度优化结果。
- `cmd/resolve-pool/main.go`：股池名称与代码核对辅助工具，不是生产同步入口。

### CLI 命令

```text
check-config | init | check | health | replay | sync | sync-intraday
status-backfill | status-refresh | status-audit | preview | research
experiment | plan | run | once | status | retry | version
```

- 生产 CLI 不再接受 `-config`，也不读取 `config.yaml`；业务配置由 `config.Builtin()` 编译进二进制，再叠加环境变量。
- `check-config`：只校验内置配置、股池、凭证和可解析的配置值，不检查数据库或远端行情。
- `init`：显式创建或迁移 `market.db`，需要数据目录已存在且可写；不会创建缺失的 data 目录。
- `check`：检查存储可写性、数据库是否初始化、交易日历、任务和投递账本；会写一个临时探针文件，但不持有常驻写锁，不发送通知。
- `health`：只读读取存储、日历、任务、账本和分钟同步元数据，不写账本、不扫描整库、不访问远端；适合高频探针。
- `replay -revision <64位revision>`：用同一构建重算已冻结的报告输入，只输出结果，不保存、不发送。
- `sync [-as-of YYYY-MM-DD]`：同步整池日线、保存行情并生成报告，不发送通知；必须持有写锁和已初始化的数据库。
- `sync-intraday`：手动执行一轮 5 分钟采集，要求 `intraday.enabled=true`。
- `status-backfill [-as-of YYYY-MM-DD]`：补齐上市日至十年窗口的交易日历和逐日停牌/交易状态；首次部署或升级 schema 后必须先执行。
- `status-refresh [-as-of YYYY-MM-DD]`：只刷新最近配置窗口的状态证据，可重复执行。
- `status-audit [-as-of YYYY-MM-DD]`：只读审计交易日、停牌、缺口和可分析状态，不访问上游。
- `preview [-as-of YYYY-MM-DD]`：从本地已保存行情计算观察排名和研究结果，不写行情。
- `research [-as-of YYYY-MM-DD]`：输出短周期研究统计；需要启用策略。
- `experiment [-as-of YYYY-MM-DD] [-parent ID] [-policy NAME] [-from D -through D] [-holdout]`：运行或派生实验。`-from`/`-through` 必须成对出现；`-holdout` 只能配合 `-parent`，且不能与 `-as-of`、`-policy`、`-from`、`-through` 同用。
- `plan -code <代码> -entry-price <实际买入价>`：只按输入成交价和预算计算数量、成本、净止盈价和净止损价，不读取实时盘口。
- `run`：常驻模式，启动网页、日线监控、调度、投递和可选的分钟采集；首次启动即刷新全股池，不因已有报告跳过。
- `once`：只执行一次 `Cycle`（投递 + 到点调度），不执行启动全池刷新；不是 `run` 的替代品。
- `status`：只读查看各通道投递账本。
- `retry [channel]`：解锁指定通道或全部通道的永久错误；可能触发后续投递。
- `version`：输出版本字符串。

`-as-of` 只允许用于 `sync`、`status-backfill`、`status-refresh`、`status-audit`、`preview`、`research` 和 `experiment`。手工写入命令需要先停止常驻 `run` 实例，因为同一数据目录只允许一个写进程。

## 主流程

### 配置加载

`config.Load()`（无参数）返回内置生产配置，依次完成：

1. 应用 `STOCK_WATCHER_*` 环境变量覆盖；
2. 解析 `leaders.yaml` 股池（`schema_version: 1`）；
3. 读取可选 `feishu.config`，再应用 `FEISHU_WEBHOOK`/`FEISHU_SECRET`；
4. 规范化绝对数据目录；
5. 读取 Tushare Token；
6. 执行严格字段、范围、端点和渠道校验。

`config.Load(path)` 仍保留给内部夹具和离线研究工具，但生产 CLI 不传路径。任何未知 YAML 字段、重复股票代码、无效渠道、非法端点或空股池都会使启动失败。

### 日线同步

```text
config.Load
  -> calendar 确认已完成交易日和目标买入日
  -> monitor.Sync
  -> 上市日期、交易日历、停牌状态准备
  -> market.Sources.Attempt 按源尝试完整窗口
  -> 日历、复权、分页、单位与历史覆盖校验
  -> storage.MarketDB 事务保存行情、隔离原因、任务与同步状态
  -> 达到推荐覆盖率后构建并保存报告
```

- 原始不复权窗口至少覆盖最近十个自然年或分析窗口，两者取更长；`listed_from` 晚于起始日时从上市日开始，不补造上市前数据。
- 前/后复权分析窗口每轮完整刷新，不跨来源或跨轮拼接；不复权增量只在来源、单位、字段一致且重叠日线完全一致时合并，否则完整回补。
- 单股请求失败时按错误类型记录并继续后续股票；取消、整体截止时间、共享盘或存储错误立即中止本轮，不当作单股隔离。
- 全部来源失败后，仅可隔离错误才跳过单股；未被备用源恢复的网络/限流错误中止本轮发布。
- 默认要求十年完整性覆盖率 `95%`、当日可分析覆盖率 `90%`；不达标仍可保存有效行情和隔离原因，但不生成推荐报告。
- 整池遍历结束后事务落库，不逐股发布。任务失败保留已有行情，不能把旧报告标成本轮成功。

### 常驻调度

- `run` 先绑定网页端口（默认 `127.0.0.1:8080`），再启动监控；端口绑定失败不会留下不可见的后台监控。
- 启动阶段执行 `prepareStartup`：刷新完整股池、确认完成交易日未变化、写入投递账本；启动刷新成功后才进入常规 `Cycle`。
- 启动刷新失败按轮询间隔重试，失败重试间隔至少 1 分钟；首次刷新成功前不得发送缓存推荐。
- `Cycle` 先处理到期投递，再按内置 `daily.run_at`（默认北京时间 18:05）判断当日是否到期；到期且没有保存报告时执行 `Sync`，然后入队并投递。
- T+1 目标在下一交易日 09:30 前有效；过期推荐不再发送。正常周末和交易日历假日不要求产生新数据。
- 默认轮询 5 分钟。常驻实例持有数据目录写锁；`check`、`status`、`health` 等只读入口可与它并行。

### 分钟采集

- 分钟采集独立于日线发布和覆盖率门槛，可由 `intraday.enabled` 单独关闭。
- 默认交易时段每 1 分钟轮询、单请求间隔 350ms、3 个并发 worker；默认 15:10 做盘后校准。
- 只持久化腾讯原生 5 分钟线；15/30/60 分钟在读取时由同一原始序列聚合，避免修订后残留旧聚合。
- 分钟失败不会阻断日线；日线失败也不会停掉分钟采集。`sync-intraday` 只跑一轮，不触发报告或通知。

### 通知投递

```text
selection.Report
  -> internal/notify/report 生成主卡片和详情卡片
  -> notify.Ledger 按事件/通道排队
  -> notify.Channel.Send
  -> internal/feishu 卡片、HMAC-SHA256 签名、HTTP/业务码确认
```

- 投递账本持久化到 `<data_dir>/state/delivery.json`，当前 schema 2；旧版单条状态会迁移。
- 每个事件/通道记录 `pending`、`in_flight`、`delivered`、`blocked`、`expired`、`unknown`；崩溃或发送确认丢失时可能产生重复消息。
- 永久错误可由 `retry` 解锁；可重试错误按退避重试；过期事件不再投递；终态记录保留 30 天后清理。
- 主卡与详情卡按依赖顺序投递。同一数据日期、同一通道只保留首个持久事件，包括待重试或 blocked 的记录。
- 发送前后都会重新检查共享盘挂载；延迟写入或共享盘故障不能通过替换通道、写本地目录或发送旧缓存绕过。

## 模块边界

- `internal/config`：内置生产配置、环境变量覆盖、严格 YAML 校验、股池、凭证、行情源、网页、策略、分钟和通知配置。
- `internal/calendar`：北京时间交易日、节假日、已完成交易日、T+1、推送过期窗口和冻结日历覆盖检查。
- `internal/market`：统一 `Source.Fetch` 接口、`Sources` 回退编排、上市日期、停牌状态、交易日历、分钟线、协议分页、复权、节流、超时/取消和数值校验。腾讯、新浪、BaoStock、Tushare、东方财富均为 Go 原生客户端。
- `internal/storage`：`Store` 管理 CIFS 检查、单写进程锁、安全路径和原子 JSON 状态；`MarketDB` 管理 SQLite 行情、同步窗口、报告、不可变快照、任务检查点和一致性读取；`dashboard.go` 提供只读看板查询。
- `internal/monitor`：整池同步、启动刷新、调度、状态回填/刷新/审计、覆盖率门槛、投递账本、重试、过期清理和报告重放。
- `internal/intraday`：5 分钟采集、盘中截止时间、盘后校准、并发 worker 和分钟同步状态。
- `internal/selection`：日常算法 `Engine` 契约、输入冻结、统一报告、趋势筛选和短周期策略组装。
- `internal/strategy`：前复权特征、不复权成交模拟、费用/滑点、T+1、价格限制、风险门槛、短周期排名、滚动验证、保留区间、结构门控和实验回抽状态机。
- `internal/chanlun`：缠论分型、笔和笔重叠区计算；生产门控在 `strategy` 中完成。
- `internal/experiment`：不可变实验快照、父子实验、开发/留出划分、结果快照、条件入场、次日触达和分钟路径校验。
- `internal/bottomstudy`：底部信号、反弹等待、先跌风险、分批/止损网格和板块/大盘对照研究；只读取冻结快照。
- `internal/diagnostics`：把报告中的规则统计、实际值、要求值和失败原因转换为只读诊断，不重新回测、不改排名。
- `internal/dashboard`：Go `embed` 打包原生 HTML/CSS/JavaScript、SVG 日 K、分钟图、策略实验室和本地仓位计算器。
- `internal/health`：只读汇总日线新鲜度、覆盖率、分析发布、任务心跳、分钟同步和通知积压。
- `internal/notify`：版本化消息、通道接口、错误分类和多事件/多通道投递账本；`internal/notify/report` 转换报告。
- `internal/feishu`：飞书卡片、HMAC-SHA256 签名、HTTP/业务码确认和可重试/永久错误分类。
- `internal/logging`：统一调用位置、凭证脱敏、stdout/滚动文件双通道、文本/JSON 格式和压缩轮转。

## 配置与凭证

### 内置默认值

| 区域 | 默认值 |
| --- | --- |
| 数据与存储 | `data_dir=data`，`require_cifs=true` |
| 网页 | `enabled=true`，`listen=127.0.0.1:8080` |
| 日线与调度 | `run_at=18:05`，`history_days=365`，`adjust=qfq` |
| 覆盖率 | 十年完整性 `95%`，当日可分析 `90%`，增量 5 根，状态刷新 30 天 |
| 趋势筛选 | 最低分 70，Top N 5，量比 1.2，RSI 50～80，日涨跌 -3%～7% |
| 策略 | 启用；净目标 3%，净止损 -3%，最长持有 5 日；预算 20000 元；佣金/过户/印花税/滑点按内置费率 |
| 分析 | `engine=auto`，算法超时 10 分钟，同步总时限 90 分钟 |
| 分钟 | 启用；1 分钟轮询，350ms 请求间隔，3 workers，15:10 校准 |
| 行情源 | `tencent,sina,baostock,tushare,eastmoney`；请求间隔 1s，单源尝试 60s，连续失败 3 次冷却 5 分钟 |
| 结构门控 | `structure_guard.mode=shadow` |
| 交易日历 | `2026-01-01` 至 `2026-12-31`，内置 2026 年节假日 |

### 外部运行时文件

- `leaders.yaml`：已确认股池，必须为 `schema_version: 1` 加 `stocks` 列表；股票代码必须带 `sh`/`sz` 前缀。
- `feishu.config`：可选私有凭证，只接受 `webhook` 和可选 `secret`；权限应为 `600`，不得提交。
- 生产配置已编译进二进制，不再需要 `config.yaml`；`config.yaml` 已被 Git 忽略。

### 关键环境变量

- `STOCK_WATCHER_DATA_DIR`、`STOCK_WATCHER_REQUIRE_CIFS`
- `STOCK_WATCHER_POOL_FILE`、`STOCK_WATCHER_FEISHU_CREDENTIALS_FILE`
- `STOCK_WATCHER_WEB_ENABLED`、`STOCK_WATCHER_WEB_ADDR`
- `STOCK_WATCHER_FEISHU_ENABLED`、`FEISHU_WEBHOOK`、`FEISHU_SECRET`
- `STOCK_WATCHER_STRATEGY_ENABLED`、`STOCK_WATCHER_INTRADAY_ENABLED`
- `STOCK_WATCHER_POLL_INTERVAL`、`STOCK_WATCHER_REQUEST_TIMEOUT`
- `STOCK_WATCHER_CALENDAR_VALID_FROM`、`STOCK_WATCHER_CALENDAR_VALID_THROUGH`、`STOCK_WATCHER_CALENDAR_HOLIDAYS`
- `STOCK_WATCHER_DAILY_HISTORY_DAYS`、`STOCK_WATCHER_MARKET_PROVIDERS`
- `STOCK_WATCHER_MARKET_*_ENDPOINT`、`STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS`、`TUSHARE_TOKEN`
- `STOCK_WATCHER_INTRADAY_POLL_INTERVAL`、`STOCK_WATCHER_INTRADAY_REQUEST_INTERVAL`、`STOCK_WATCHER_INTRADAY_WORKERS`、`STOCK_WATCHER_INTRADAY_CALIBRATION_AT`
- `STOCK_WATCHER_LOG_DIR`、`STOCK_WATCHER_LOG_MAX_MB`、`STOCK_WATCHER_LOG_MAX_DAYS`、`STOCK_WATCHER_LOG_BACKUPS`、`STOCK_WATCHER_LOG_JSON`、`STOCK_WATCHER_LOG_COLOR`

完整默认值、范围和 systemd/Compose 加载差异见 [环境变量](docs/environment-variables.md)。凭证只能来自环境变量或私有文件；日志不会输出 webhook、secret、Token 或 Authorization。

### 通知渠道

- 生产内置配置只启用一个飞书通道 `feishu-main`，兼容旧 `feishu` 配置。
- 框架支持 `notify.channels` 多个命名通道，但当前只实现 `feishu` 类型；多通道配置与旧 `feishu` 不能同时启用。
- 通道名必须匹配 `^[a-z0-9][a-z0-9._-]{0,63}$`，不能重复。

## 行情与数据契约

### 行情源

| 名称 | 协议 | 复权 | 原始成交量单位 |
| --- | --- | --- | --- |
| `tencent` | 腾讯 HTTP，640 根分页 | none / qfq / hfq | 科创板为股，其余为手 |
| `sina` | 新浪 KLC2 解码与复权因子 | none / qfq / hfq | 股 |
| `baostock` | 官方 TCP 协议，250 根分页 | 3/2/1 映射为 none/qfq/hfq | 股 |
| `tushare` | REST `daily` + `adj_factor` | 原价，或按因子复权 | 手 |
| `eastmoney` | 东方财富日线 HTTP | fqt 0/1/2 | 手 |

- 默认按 `tencent → sina → baostock → tushare → eastmoney` 尝试完整窗口；可通过 `STOCK_WATCHER_MARKET_PROVIDERS` 调整顺序。
- Tushare Token 仅从 `market.tushare_token_env` 指定的环境变量读取，默认 `TUSHARE_TOKEN`；缺失时跳过该源，若它是唯一来源则配置校验失败。
- 东方财富是直接 HTTP 上游，不是运行 AKShare；BaoStock 直接使用 TCP 协议，不依赖 Python SDK 或子进程。
- HTTP 端点只接受 HTTPS 或回环 HTTP，禁止内嵌凭证、查询参数、Fragment 和重定向；所有响应体、TCP 解压和页数都有上限。
- 单请求超时默认 10 秒，每源请求间隔默认 1 秒且不得低于 350ms；单源整窗尝试默认 60 秒，连续失败 3 次后冷却 5 分钟。
- 网络/供应商错误以及 HTTP 成功但数据校验失败都会触发备用源；调用者取消、上下文截止和存储错误不会启动下一来源。

### 上市日期与十年窗口

- 上市日期优先使用股池 `listed_from`，其次 `metadata/listings/<code>.json` 缓存，最后查询新浪发行资料。
- 原始不复权窗口起点为 `max(上市日期, 截止日前十个自然年)`；上市日晚于截止日时跳过。
- 前复权分析窗口由 `daily.history_days` 控制，默认 365 天，允许 90～7300 天；原始窗口不会因超长分析窗口扩大下载跨度。
- 旧更早日线不主动删除；上市日期正确不代表后续历史完整，交易所日历、停牌状态、分页完整性、覆盖率门槛仍然生效。

### 完整性与隔离

- 交易日历覆盖区间内逐日核对；停牌状态只解释已知无交易，未知、冲突或来源不可用的日期仍按缺口隔离。
- 十年完整性、当日可分析比例、复权字段、成交量单位和来源一致性都是发布条件；不能通过放松门槛把未知当通过。
- 停牌状态来源不可用时不伪造 K 线，也不把旧状态当本轮成功；分钟备用源不在本轮范围内。

### 分钟线

- 当前只采集腾讯原生 5 分钟线；15/30/60 分钟由同一原始序列聚合，不单独落库。
- 请求/展示上限为最近 640 根；研究按日期区间读取本地已积累数据，最多 20 年，不会补出未曾下载的历史。
- 实验整体快照有 32 MiB 编码上限；超过时明确拒绝，长范围研究应缩小股池或分析窗口。

## 存储、schema 与恢复

### 数据根目录

- 默认 `<data_dir>` 为 `data`，生产必须指向已挂载 CIFS；`require_cifs=true` 时只接受 CIFS/SMB2 文件系统。
- `Store.OpenView` 和 `Store.Open` 都不会创建缺失的数据根；共享盘不可用时不会回退到本地目录。
- 写入口使用 `.stock-watcher.lock` 获取非阻塞独占锁；同一数据目录只允许一个写入进程。
- 数据目录内部路径禁止绝对路径、`..`、非规范路径和符号链接；JSON 原子写入使用临时文件、文件 `fsync`、重命名和目录同步。
- `check` 的存储探针会创建并删除临时文件；`health` 不写文件。

### SQLite 文件

| 文件 | schema | 内容 |
| --- | --- | --- |
| `<data_dir>/market.db` | 5 | 行情、同步、报告、分钟、不可变快照、任务检查点、停牌状态和交易所日历 |
| `<data_dir>/experiments.db` | 1 | `experiment_snapshot`、`experiment_run` |
| `<data_dir>/calendar.db` | 1 | `calendar_history`，冻结交易日历缓存 |
| `<data_dir>/state/delivery.json` | 2 | 通知投递账本 |

`market.db` 当前共 18 张表：`stock`、`daily_bar`、`stock_sync`、`sync_run`、`selection_run`、`selection_result`、`intraday_bar`、`intraday_sync`、`history_snapshot`、`history_revision`、`report_input`、`report_archive`、`sync_task`、`sync_checkpoint`、`session_status`、`status_sync`、`market_calendar`、`calendar_sync`。

- 写连接使用 `journal_mode=DELETE`、`synchronous=FULL`、外键和 5 秒 busy timeout；不要启用 WAL。
- 价格保存为百万分之一元整数；`daily_bar` 保留来源原始成交量和按来源换算的股数，读取时校验来源/字段一致性。
- 只读连接使用 `query_only=ON`，要求 schema 已是 5；写入入口支持从 schema 2/3/4 迁移到 5，但拒绝未声明支持的旧版本。
- `init` 会迁移已有数据库，不重建、不清空；迁移后旧二进制不兼容 schema 5，回滚必须恢复升级前完整备份。
- 生产同步始终使用 SQLite；旧 `daily/`、`execution/`、`reports/` JSON 只供无库时的只读兼容和包级旧调用，不能作为生产写入兜底。
- 新报告保存 `input_id`、`revision`、算法描述、配置摘要和构建版本；`replay` 依赖冻结输入和相同构建，不读取后续修订。
- 采集任务按股票检查点恢复；重启后同日期、同参数任务复用已完成股票，覆盖失败会重新尝试隔离股票。

### 报告版本

- 趋势报告 `schema_version: 1`。
- 短周期策略报告在无分钟输入时为 2，有分钟结构输入时为 3；schema 3 增加 `structure_guard`、`guard_passed`、`observation_rank`、`eligible_rank` 和评分推导。
- 旧 schema 1/2 报告保持只读兼容，不迁移、不回写。
- 投递账本 schema 2，可迁移旧单条状态；不要把所有数据统一视为 schema 1。

### 备份与升级

- 备份至少覆盖 `market.db`、`experiments.db`、`calendar.db`、`state/`、`metadata/listings/` 和私有配置，保留权限。
- 安全流程是停止所有写入实例、确认数据库连接关闭，再在 Windows/备份盘完整复制；不要只复制 SQLite 主文件或删除 journal。
- schema 升级后先执行 `status-backfill`，再用 `status-audit` 确认覆盖，之后启动 `run`。
- 回滚使用升级前完整备份和兼容构建；不要用旧二进制直接打开 schema 5。

## 选股、策略与实验

### 算法契约

- 日常同步与算法的唯一调用边界是 `selection.Engine`：`Descriptor()` 描述版本、周期、复权口径和缺失策略，`Analyze(ctx, Input)` 只计算并返回统一 `selection.Report`。
- 框架负责采集、覆盖率、保存、任务期限和通知；引擎不得自行下载行情、发送消息或修改持久化数据。
- 内置引擎为 `leader_daily_v1`（仅日线）和 `short_horizon_v1`（日线 + 30m + 5m，`RawRequired=true`）。
- 引擎必须响应 context 取消，返回相同的 `as_of`、目标交易日、版本和复权口径；结果代码只能来自输入股池，不能重复。
- 输入会深拷贝并剥离路径、凭证和网络客户端；报告保存算法描述、配置摘要、构建版本和冻结输入标识。

### 短周期策略

- 特征使用前复权价格；成交模拟使用独立的不复权价格；费用包含佣金、过户费、印花税和滑点。
- 信号日收盘后排名，下一交易日开盘价加单边滑点买入；开盘偏离、无成交、一字板或预算不足时不模拟买入。
- 主目标为扣费后的 3%，同时统计 2.5%；每笔扣费后净亏损必须严格低于投入的 3%，等于 3% 也否决。
- 最早在买入后的下一交易日卖出，默认第 5 个交易日收盘到期；T+1、涨跌停和历史 ST 状态未知都要显式处理。
- 推荐默认要求至少 60 个入场样本、40 个不同信号日、无未决/不完整信号日、正的平均净收益、优于全池基准的达标率，以及满足机会分和风险门槛；代码还设有 30 样本/30 信号日的硬下限。
- 未通过推荐门槛时仍生成观察排名，并展示缺失样本、未决交易、收益不足或风险超限的原因。
- 机会分不是成功概率；历史统计不代表未来收益。

### 结构门控与缠论

- `structure_guard.mode` 默认 `shadow`：记录日线结构、30m/5m 否决证据、`observation_rank` 和 `eligible_rank`，正式推荐仍走旧门控。
- `enforce`：正式 Top N 只能来自 `guard_status=pass` 的 `eligible_rank`；没有合格股票时输出空推荐，不从全池递补。
- 日线决定方向，30m/5m 只有否决权；数据不足、错日、错源、复权不一致或聚合不一致时标记 `unknown`，未知不按通过处理。
- `internal/chanlun` 固定包含关系、分型、笔和笔重叠区规则；`stroke_zone` 是连续三个完成笔的公共交集，不冒充严格缠论中枢。
- 结构通过只表示“未被结构过滤”，不是第一、二、三类买点，也不能绕过历史证据、机会分、风险和留出验证。
- `strategy.EvaluateReboundSignal` 只是实验原语（`watch/armed/confirmed/failed/expired`），尚未接管正式推荐或实验逐笔回放。

### 实验

- `stock-watcher experiment` 只读 `market.db` 生成快照，写 `experiments.db`；必要时写 `calendar.db` 缓存。它不创建行情客户端，不发送消息。
- 基线实验默认使用最新已保存行情日期，也可指定 `-as-of`。子实验从父记录恢复股池、日历、参数、前复权日线和不复权日线，即使后续行情或配置变化也沿用原输入。
- `-from`/`-through` 是包含两端的信号日期，不是成交日期；必须成对出现，交易日历和完整卖出窗口必须覆盖。
- `-holdout` 只消费父开发记录预留的区间，创建独立子记录，父记录不变；同一快照首次消费未污染，再次消费标记 `contaminated`，不能声称独立样本外验证。
- 实验独立取得 `.experiments.lock`，可与行情监控并行，但一次只允许一个实验；最长运行 10 分钟，取消或失败会尽量保存状态。
- 实验策略包括 `baseline`、`upper-wick`、`risk-qualified`、`conditional-*` 和 `next-day-*`；对比类策略通常要求 `-parent`。
- 实验结果不会自动进入日常推荐或飞书推送。

### 底部研究

- `cmd/bottom-study` 读取冻结实验快照，输出新触发次数、反弹等待时间和先跌风险；不模拟实盘成交，也不写网页交易档案。
- `-optimize`、`-optimize-speed`、`-compare-context` 和 `-archive` 属于研究路径；`-archive` 只保存速度优化结果到实验档案。
- 快照输入上限 32 MiB，工具默认 2 分钟超时；研究结论必须保留数据区间、训练/后段划分和限制。

## 网页与 API

### 页面

| 路径 | 内容 |
| --- | --- |
| `/` | 摘要快照、已完成交易日、覆盖率、筛选数量和导航 |
| `/stocks` | 全股池搜索、行业/数据状态筛选与排序 |
| `/chart` | 日 K、成交量、MA5/10/20、前复权/不复权切换和 5/15/30/60 分钟周期 |
| `/rankings` | 当前及历史日推排名、推荐状态、观察依据和风险提示 |
| `/experiments` | 策略实验室、父子实验、样本对比和固定数据快照 |
| `/calculator` | 浏览器内仓位、已有持仓、剩余风险与净止盈价计算 |

### 只读接口

- `GET/HEAD /healthz`：HTTP 进程存活，不读取存储。
- `GET/HEAD /readyz`、`/api/status`：本地依赖和业务状态；`readyz` 在存储/日历不可用时返回 503，`/api/status` 会返回 `ready:false` 的诊断 JSON。
- `GET/HEAD /api/dashboard`：摘要和最新报告快照，不接受查询参数。
- `GET/HEAD /api/reports?as_of=YYYY-MM-DD`：历史报告，必须提供一个合法日期。
- `GET/HEAD /api/history?code=sz000001[&adjust=none]`：股池内股票的日线；`adjust` 只接受配置口径或 `none`，不接受任意日期参数。
- `GET/HEAD /api/intraday?code=sz000001&period=5|15|30|60`：股池内股票的分钟线。
- `GET/HEAD /api/experiments[?id=<实验ID>]`：实验列表或单条实验记录；列表最多 200 条。
- `GET/HEAD /api/experiment-history?id=<实验ID>&code=<代码>&date=YYYY-MM-DD`：实验固定快照 K 线。

所有接口只允许 GET/HEAD，POST 等返回 405；响应设置 `Cache-Control: no-store`、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY` 和严格 CSP。JS/CSS 资产使用 `no-cache`。应用使用根路径 `/`，不支持子路径挂载；反向代理应保留 `/api/` 和 `/assets/`。

### 用户可见行为

- 摘要和股票页每分钟刷新；刷新不会触发行情下载、报告生成或消息推送。
- 看板优先展示最新有效行情；覆盖率失败不等于本地一定没有数据，已保存有效行情可以独立于推荐报告展示。
- 缺失/隔离股票不能用旧日线冒充当前有效数据；切换股票时取消旧请求，错误或超时清空旧图并提供重试。
- 日 K 显示实际起止日期、根数、来源和成交量单位；长历史横向滚动，滑块定位日期。
- `/calculator` 只在浏览器内计算，不上传或持久化账户输入，不依赖行情接口，断网仍可使用。
- 网页没有内置账号、角色或写入接口；远程访问必须依赖受控网络或已有认证反向代理，代理的 401/403 分支需要单独验证，不能虚构普通用户/管理员角色。

## 健康检查与日志

### 健康状态

- `health.Read` 只读汇总预期交易日、有效股票数、报告是否发布、同步任务阶段/心跳、通知积压、分钟同步和问题列表。
- `business` 为 `healthy`、`degraded` 或 `syncing`；`issues` 包含数据陈旧、分析未发布、任务失败/心跳过期、通知 blocked/unknown/overdue、分钟不完整等原因。
- `/healthz` 只证明 HTTP 存活；`/readyz` 验证本地依赖；`/api/status` 即使降级也返回诊断 JSON。
- Docker 健康检查执行 `stock-watcher health`，每分钟一次，连续 3 次失败标记 unhealthy；Docker 不会仅因 unhealthy 自动重启。
- 健康检查不发送通知；需要确认存储可写性时用 `check`，高频只读检查用 `health`。

### 日志

- 统一行形态为 `[RFC3339Nano] [LEVEL] [调用位置] 消息 {"属性"}`；LEVEL 使用大写 `DEBUG/INFO/WARN/ERROR`。
- 调用位置为 `文件:行号`：项目源码使用仓库相对路径，外部依赖只保留文件名；跳过日志适配层和 slog 内部栈帧。
- 消息使用英文；token、secret、password、Authorization、webhook、credential 等敏感字段脱敏，注册的精确 secret 值也会被替换。
- 默认同时写 stdout 和 `logs/stock-watcher.log`；文件日志不含 ANSI 颜色，JSON 模式输出整条记录并把行前缀放入 `msg`。
- 文件按大小、天数和份数轮转，轮转产物 gzip 压缩；默认单文件 10 MiB、保留 7 天、10 份，由 `STOCK_WATCHER_LOG_*` 控制。
- 日志目录和文件权限分别为 `0700`/`0600`；不要自行拼接时间、级别、颜色或另建日志框架。

## 开发、测试与交付

### 常用命令

```bash
make fmt-check              # gofmt 检查 cmd/internal
make vet                    # go vet ./...
make test                   # go test -race ./...
make build                  # bin/stock-watcher，版本为当前 Git HEAD
make smoke                  # 真实 CLI、run.sh、仓位模型和策略模型
make sources-smoke          # 本地 HTTP/TCP mock 行情源、回退和上市缓存
make intraday-smoke         # 分钟采集、schema 迁移、只读 API 和 SIGTERM
make dashboard-smoke        # Chromium：真实 CLI、DOM/URL/网络和错误恢复
make image                  # 构建 Alpine 镜像
make docker-smoke           # 容器 CLI、权限、Compose 健康检查和生命周期
make docker-dashboard-smoke # 容器形态浏览器验收
make ci                     # fmt/vet/race/build/smoke/sources/intraday/package
make package                # Linux 发布包，BUILD_GOARCH=amd64 或 arm64
```

`scripts/ci.sh` 实际执行 `make fmt-check vet test build smoke sources-smoke intraday-smoke package`，并校验发布包 `SHA256SUMS`。GitHub Actions 在 `scripts/ci.sh` 之后额外安装 Python Playwright/Chromium，运行 `make dashboard-smoke`、`make docker-smoke` 和 `make docker-dashboard-smoke`，再上传 Linux amd64 发布包。`make ci` 本身不包含浏览器和容器验收。

测试依赖 Python 3 和 Playwright Chromium；策略模型测试还需要 Node.js 20+。这些是测试依赖，不属于生产运行时。资源紧张时可用 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci`。

### 本地运行

- `make run` 调用 `scripts/run.sh`：先构建，再加载仓库私有 `.env`，前台执行 `bin/stock-watcher run`。
- 如果同仓库同配置的 systemd 实例正在运行，脚本会先校验内置配置，停止后台服务并确认 MainPID 为 0，再切换前台；构建/校验失败时保留后台服务。
- `make check`、`make status` 和直接运行二进制不会自动加载 `.env`；可在可信 shell 中执行 `set -a; source .env; set +a`。
- 本地测试可设置 `STOCK_WATCHER_REQUIRE_CIFS=false`，但生产保持 `true`；不要为绕过挂载故障创建本地 data 目录。

### 部署形态

- systemd 模板：[deploy/stock-watcher.service](deploy/stock-watcher.service)，读取 `/etc/stock-watcher.env`，执行同一 `stock-watcher run`。
- Compose：根 Dockerfile 是唯一镜像定义，`docker/compose.yaml` 将共享盘映射到 `/data`、日志到 `/logs`、股池只读挂载到 `/app/leaders.yaml`，容器监听 `0.0.0.0:8080`，宿主机默认绑定 `127.0.0.1:8080`。
- 容器根文件系统只读、删除全部 capabilities、限制 CPU 1 核、内存 256 MiB、PID 64；仅 `/data` 和 `/logs` 可写。
- 运行变量由 `STOCK_WATCHER_RUNTIME_ENV` 指定的私有文件注入；路径挂载禁止自动创建宿主目录。
- 原生、systemd 和容器只能选择一种常驻写入方式；同一数据目录同时只允许一个写入实例。

### 交付门禁

- 发布前记录完整提交 SHA、镜像 ID 或发布包校验和，并确认运行制品确实包含本次改动。
- 至少执行适用的单元/集成测试、静态检查和构建；涉及前端时运行浏览器冒烟，涉及数据库时覆盖迁移和空数据/已有数据场景。
- 涉及页面、导航、权限、重定向、Cookie、静态资源或网关路径时，必须在真实浏览器断言 DOM、URL、标题、控制台错误和网络请求；不能只凭源码搜索或接口 200 判断完成。
- 身份相关改动至少验证未登录、普通用户和管理员三种身份；本项目没有内置账号体系，远程鉴权由反向代理负责，本地可用 mock 验证代理拒绝分支，但不能虚构角色功能。
- 本地验证尽量复用已有服务和容器，使用最小数据集和单个浏览器实例，按身份串行执行，并设置明确的超时、并发和重试上限；结束后清理临时进程、容器、会话和测试数据。
- 无法访问真实部署环境、真实账号或必要依赖时，结论必须写明“本地验证通过，部署后未验证”，列出未验证项和需要执行的命令，不能使用“已完成”“线上可用”等确定性表述。
- 部署后使用真实 URL 验证健康检查、登录/会话边界、关键页面和 API、重定向/写入流程、滚动发布完成、旧实例退出、环境变量是否生效和错误日志；敏感变量只验证存在/生效，不输出值。
- 最终交付信息包含：改动文件、验证命令及结果、测试身份/环境、构建制品标识、部署验证 URL 与结果、已知风险。

### 已知边界

- 仅覆盖沪深 A 股，不提供逐笔行情、盘口深度或自动交易。
- 日线来自多个上游；分钟线当前只采集腾讯 5 分钟原始线并派生 15/30/60 分钟，上游窗口和修订规则可能变化。
- 内置交易日历当前只覆盖 2026 年，过期后必须升级；测试覆盖变量不得用于生产。
- 网页没有账号、角色和写入接口，远程暴露必须依赖受控网络或已有认证代理。
- 飞书投递为至少一次语义，请求成功后、确认落盘前崩溃可能产生重复消息。
- 共享盘故障、临时网络错误或数据覆盖不足时，本轮不会发布缓存推荐；已有数据保留等待重试。
- 研究结果受样本、费用假设、流动性和成交边界影响，历史统计不代表未来收益。
- env | grep GITHUB 可以查看 github token，用来提交推送。
