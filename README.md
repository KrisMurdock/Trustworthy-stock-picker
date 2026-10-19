<h1 align="center">Trustworthy-stock-picker</h1>

<p align="center">
  <strong>A 股龙头股池 · 十年日线 · 多源回退 · 短线研究 · 内嵌看板</strong>
</p>

<p align="center">
  <a href="https://github.com/KrisMurdock/Trustworthy-stock-picker/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/KrisMurdock/Trustworthy-stock-picker/actions/workflows/ci.yml/badge.svg?branch=main"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white">
  <img alt="SQLite" src="https://img.shields.io/badge/Storage-SQLite-003B57?logo=sqlite&logoColor=white">
  <img alt="Dashboard" src="https://img.shields.io/badge/UI-Embedded-2ea44f">
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/License-Apache%202.0-green?style=flat&labelColor=555555"></a>
</p>

<p align="center">
  <a href="#快速开始">快速开始</a> ·
  <a href="#核心能力">核心能力</a> ·
  <a href="#工作原理">工作原理</a> ·
  <a href="#配置">配置</a> ·
  <a href="#部署">部署</a> ·
  <a href="#开发与测试">开发与测试</a> ·
  <a href="#文档导航">文档导航</a> ·
  <a href="#license">License</a>
</p>

Stock Watcher 是一个用纯 Go 构建的 A 股研究型行情引擎。它从多个公开行情源同步龙头股池，校验交易日、复权窗口和十年历史完整性，将行情、报告与投递状态事务化保存到 Windows 共享盘上的 SQLite，并生成下一交易日的观察排名、短线研究结果、内嵌看板和飞书通知。

整个生产链路只有一个原生二进制：行情下载、数据校验、指标计算、策略研究、网页服务和消息投递都在同一进程内完成，不依赖 Python、Node.js、AKShare 或独立前端服务。

> [!NOTE]
> 本项目用于数据工程与量化研究，不构成投资建议。它不连接券商、不自动下单，也不会把研究排名描述为成功概率或收益承诺。

## 核心能力

- **纯 Go、单二进制**：没有解释器、子进程和隐藏兼容层；网页资源通过 `go:embed` 打包进程序。
- **多源行情回退**：默认按 `腾讯 → 新浪 → BaoStock → Tushare → 东方财富` 尝试完整数据窗口。
- **十年原始日线 + 复权分析窗口**：长期留存不复权价格，分析使用独立刷新的前复权窗口，不跨来源或跨轮拼接。
- **严格的数据完整性**：校验交易日历、上市日期、分页、复权口径、成交量单位和历史覆盖；覆盖率不足时不发布推荐。
- **SQLite 事务存储**：行情、同步状态、报告和投递账本保存在 `<data_dir>/market.db`，同一数据目录只允许一个写入进程。
- **可复核的短线研究**：前复权特征、真实费用与滑点、T+1 成交模拟、滚动验证和独立留出样本。
- **内嵌只读看板**：提供全池、个股 K 线、分钟周期、历史排名、策略实验室和仓位计算。
- **可恢复的通知投递**：飞书通道采用持久账本和至少一次语义，失败重试不会重新下载整池行情。
- **生产级运行约束**：默认要求 CIFS，拒绝本地磁盘回退；日志统一文本/JSON、stdout/文件双通道和滚动压缩。

## 快速开始

### 环境要求

- Linux、Go 1.26.4+、Git、GNU Make、Bash。
- 生产运行需要已挂载的 CIFS 共享目录，并将 `data` 链接到共享盘；本地测试可临时设置 `STOCK_WATCHER_REQUIRE_CIFS=false`。
- 默认读取当前目录的 `leaders.yaml` 股池和 `feishu.config` 私有凭证；无需通知时设置 `STOCK_WATCHER_FEISHU_ENABLED=false`。
- Docker Engine 与 Compose ≥ 2.30.0 仅用于容器部署和容器验收。

### 构建与首次启动

```bash
git clone https://github.com/KrisMurdock/Trustworthy-stock-picker.git
cd Trustworthy-stock-picker

# 首次体验可先关闭通知；需要飞书推送时移除此行并配置凭证
export STOCK_WATCHER_FEISHU_ENABLED=false

make build
./bin/stock-watcher check-config
./bin/stock-watcher init
./bin/stock-watcher check

# 首次部署：补齐十年停牌状态与交易所日历
./bin/stock-watcher status-backfill -as-of YYYY-MM-DD
./bin/stock-watcher status-audit -as-of YYYY-MM-DD

# 下载行情、校验覆盖率并保存报告
./bin/stock-watcher sync
./bin/stock-watcher preview -as-of YYYY-MM-DD

# 前台运行监控、分钟采集与网页
make run
```

将 `YYYY-MM-DD` 替换为最近已完成的交易日。`check-config` 只校验配置、股池、凭证和日志设置，`check` 才检查数据库、交易日历和投递状态。首次部署或升级数据库 schema 后，必须先完成 `status-backfill`；未完成时 `sync` 和 `run` 会拒绝发布。

`make run` 启动后会先绑定网页端口，再执行启动同步。网页默认地址为 `http://127.0.0.1:8080/`；常驻进程按内置的 `daily.run_at`（默认北京时间 18:05）处理后续交易日。

## 常用命令

| 命令 | 作用 | 写入行情 | 发送通知 |
| --- | --- | --- | --- |
| `check-config` | 校验内置配置、股池、凭证与日志参数 | 否 | 否 |
| `init` | 显式创建 `market.db` | 是 | 否 |
| `check` | 检查存储、日历、任务和投递状态 | 否 | 否 |
| `status-backfill` | 补齐股池十年停牌状态与交易所日历 | 是 | 否 |
| `status-refresh` | 刷新最近一段时间的证券状态 | 是 | 否 |
| `status-audit` | 只读审计交易日、停牌、缺口和可分析状态 | 否 | 否 |
| `sync` | 同步整池行情并生成报告 | 是 | 否 |
| `sync-intraday` | 手动执行一轮 5 分钟采集 | 是 | 否 |
| `preview` | 输出全池观察排名和研究结果 | 否 | 否 |
| `research` | 输出研究统计 | 否 | 否 |
| `experiment` | 运行或派生策略实验 | 是 | 否 |
| `plan` | 按实际成交价和预算计算交易计划 | 否 | 否 |
| `run` | 常驻日线同步、分钟采集、调度和网页 | 是 | 是 |
| `once` | 执行一次受时间窗口约束的调度 | 是 | 是 |
| `status` | 查看各通道投递状态 | 否 | 否 |
| `retry` | 解锁指定通道或全部通道的永久错误 | 是 | 可能 |

`sync` 不发送消息；`run` 和 `once` 才会执行调度与投递。常驻进程持有数据目录写锁，手动写入命令需要先停止常驻实例。修改股池、环境变量或凭证后需要重启。

## 工作原理

```text
                       ┌─────────────────────────────┐
                       │ leaders.yaml / 内置配置      │
                       └──────────────┬──────────────┘
                                      │
  Tencent ─┐                          ▼
  Sina ────┤                 ┌──────────────────┐
  BaoStock ├── 完整窗口回退 ─>│ 日历与覆盖校验    │
  Tushare ─┤                 └────────┬─────────┘
  Eastmoney┘                          │
                                      ▼
                            ┌──────────────────┐
                            │ market.db (CIFS) │
                            └───────┬──────────┘
                                    │
             ┌──────────────────────┼──────────────────────┐
             ▼                      ▼                      ▼
    ┌─────────────────┐   ┌─────────────────┐   ┌─────────────────┐
    │ 指标与短线策略   │   │ 内嵌只读看板     │   │ 投递账本         │
    └────────┬────────┘   └─────────────────┘   └────────┬────────┘
             │                                            │
             └──────────────> 报告 <──────────────────────┘
                                 │
                                 ▼
                          飞书自定义机器人
```

一次完整同步遵循以下顺序：

1. 根据北京时间确定最近已完成交易日，跳过周末和内置交易日历中的节假日。
2. 按配置顺序尝试行情源，只有网络、供应商、权限、覆盖不完整等可回退错误才切换来源。
3. 校验上市日期、交易日缺口、复权口径、分页边界和成交量单位；可隔离错误逐股记录，临时性错误阻止本轮发布。
4. 整池遍历结束后事务化写入行情与同步状态；达到完整性覆盖率后才与报告同事务发布。
5. 生成排名或研究结果，经投递账本按数据日期和通道去重，再发送尚未过期的消息。
6. 内嵌看板只读同一数据库，展示最新有效行情和已保存报告，不触发同步或通知。

## 数据与调度

- 默认行情源为 `tencent, sina, baostock, tushare, eastmoney`，可通过 `STOCK_WATCHER_MARKET_PROVIDERS` 调整。
- 每只有效股票保存至少最近十个自然年的不复权日线；上市不足十年的股票从上市日开始，不虚构上市前数据。
- 分析窗口由 `daily.history_days` 控制，默认 365 天，允许 90–7300 天；原始窗口取十年与分析窗口中更长者。
- 推荐要求十年完整性覆盖率默认不低于 95%，当日可分析覆盖率默认不低于 90%。
- 交易日 15:00 前使用上一已收盘交易日；目标买入日 09:30 后不再推送旧推荐。
- 内置交易日历当前覆盖 2026 年，必须在到期前按交易所新年度通知升级二进制。
- 生产保持 `STOCK_WATCHER_REQUIRE_CIFS=true`。共享盘不可用时停止本轮写入，不回退到服务器本地行情目录。

行情源协议、分页、复权和回退细节见[数据源接入与回退](docs/data-sources.md)。

## 短线研究

内置配置默认启用全池短周期排名。核心假设如下：

- 信号日收盘后排名，下一交易日开盘价加单边滑点买入；开盘偏离、无成交、一字板或预算不足时不模拟买入。
- 主目标为扣费后的 **+3%**，单独统计 **+2.5%**；每笔扣费后净亏损必须严格低于 3%。
- 最早在买入后的下一交易日卖出，默认第 5 个交易日收盘到期，同时比较 1/3/5 日窗口。
- 特征使用前复权价格，成交模拟使用独立的不复权价格；费用包含佣金、过户费、印花税和滑点。
- 开发区间逐日更新历史证据，最近 40 个完整信号日作为验证区间；独立留出区间通过 `-holdout` 单次消费。
- 推荐要求至少 60 个买入样本、40 个不同信号日、正的平均净收益、优于全池的达标率，以及满足机会分和波动门槛。

未通过推荐门槛时仍会生成观察排名，并明确展示缺失样本、未决交易、收益不足或风险超限的原因。统计门槛不是对未来收益的承诺，详见[推荐门槛诊断](docs/strategy-diagnostics.md)。

日线推荐还会读取已保存的完成 5 分钟线，派生 15/30 分钟结构并记录旧 5/15/30 诊断；该诊断只作对照，不改变正式结论。生产结构判断使用 `structure_guard`：`shadow` 模式只输出观察排名和结构通过排名，不产生可执行推荐；`enforce` 模式下只有日线结构通过池内的可入选候选才能成为正式推荐。`structure_guard=pass` 只表示未被结构过滤，不是买点。飞书主卡用原生表格展示 Top10 的结论、价格和主要风险；详情卡按页补齐“结构门控”、“旧 5/15/30 诊断”、匹配规则和“评分推导”，后者给出基础分、日线评分贡献、加减分项、历史证据融合及最终机会分的算式。

```bash
./bin/stock-watcher sync -as-of YYYY-MM-DD
./bin/stock-watcher preview -as-of YYYY-MM-DD
./bin/stock-watcher research -as-of YYYY-MM-DD
./bin/stock-watcher experiment -policy baseline
./bin/stock-watcher plan -code sz000001 -entry-price 10
```

`plan` 只根据输入成交价和预算推算数量、成本、净止盈价和净止损价，不读取实时盘口，也不代表实际可成交价格。

## 网页看板

网页由 Go `embed` 打包，随 `run` 一起启动，不需要独立前端进程或外部图表 CDN。

| 页面 | 用途 |
| --- | --- |
| `/` | 已完成交易日、覆盖率、筛选数量和导航摘要 |
| `/stocks` | 全股池搜索、行业/数据状态筛选与排序 |
| `/chart` | 日 K、成交量、MA5/10/20、前复权/不复权切换和分钟周期 |
| `/rankings` | 当前及历史日推排名 |
| `/experiments` | 策略实验室、父子实验、样本对比和固定数据快照 |
| `/calculator` | 浏览器内仓位、持仓、剩余风险与净止盈价计算 |

网页接口全部只读，仅接受 GET/HEAD：`/healthz`、`/api/dashboard`、`/api/reports?as_of=YYYY-MM-DD`、`/api/history?code=sz000001` 和 `/api/intraday?code=sz000001&period=5`。摘要页和股票页每分钟刷新，刷新不会触发行情下载或消息推送。

网页没有内置账号或权限系统，默认仅监听 `127.0.0.1:8080`。远程访问必须使用受控网络或已有认证反向代理；代理应保留根路径、`/api/` 和 `/assets/`。

## 配置

生产配置已编译进二进制，启动不读取 `config.yaml`。部署差异通过环境变量覆盖，私有运行时文件仍保持在仓库之外或 Git 忽略范围内。

| 文件 / 变量 | 作用 |
| --- | --- |
| `leaders.yaml` | 已确认股池；股票代码必须带 `sh`/`sz` 市场前缀 |
| `feishu.config` | 可选飞书凭证文件，权限应为 `600`，不得提交 |
| `STOCK_WATCHER_DATA_DIR` | 行情与状态目录；默认 `data`，生产指向 CIFS |
| `STOCK_WATCHER_REQUIRE_CIFS` | 是否强制 CIFS；生产保持 `true` |
| `STOCK_WATCHER_WEB_ADDR` | 网页监听地址，默认 `127.0.0.1:8080` |
| `STOCK_WATCHER_FEISHU_ENABLED` | 是否启用飞书投递 |
| `STOCK_WATCHER_STRATEGY_ENABLED` | 是否启用全池短周期排名 |
| `STOCK_WATCHER_INTRADAY_ENABLED` | 是否启用 5 分钟采集 |
| `STOCK_WATCHER_MARKET_PROVIDERS` | 逗号分隔的行情源顺序 |
| `TUSHARE_TOKEN` | 可选 Tushare Token；缺失时跳过 Tushare |

完整变量、默认值、优先级和 systemd/Compose 加载差异见[环境变量](docs/environment-variables.md)。凭证只能来自环境变量或私有文件，日志不会输出 webhook、secret 或 Token。

## 部署

原生、systemd 与容器都直接运行同一个 Go CLI。同一数据目录同时只允许一个写入实例。

### 原生或 systemd

```bash
make build
./bin/stock-watcher version
./bin/stock-watcher check

sudo install -m 644 deploy/stock-watcher.service /etc/systemd/system/stock-watcher.service
sudo systemctl daemon-reload
sudo systemctl enable --now stock-watcher.service
sudo systemctl status stock-watcher.service
```

服务模板读取 `/etc/stock-watcher.env`，工作目录和二进制路径按实际安装位置调整。完整准备、SELinux、升级、回滚和排障步骤见[部署与排障](docs/deployment.md)。

### Docker Compose

```bash
make image
make docker-smoke
```

Compose 将宿主机共享盘映射到 `/data`、日志映射到 `/logs`，容器监听 `0.0.0.0:8080`，宿主机默认只绑定 `127.0.0.1:8080`。容器根文件系统只读，仅 `/data` 和 `/logs` 可写。操作步骤见[Compose 部署](docker/README.md)。

发布前记录完整提交 SHA、镜像标识或发布包校验和。镜像 tag 建议使用提交 SHA，部署后用真实 URL 验证健康检查、页面、API、权限边界和退出行为。

## 开发与测试

### 构建门禁

| 命令 | 内容 |
| --- | --- |
| `make fmt-check` | Go 格式检查 |
| `make vet` | `go vet ./...` |
| `make test` | `go test -race ./...` |
| `make smoke` | 真实 CLI、脚本和仓位计算模型 |
| `make sources-smoke` | 本地 HTTP/TCP 模拟行情源与回退 |
| `make intraday-smoke` | 分钟采集、schema 迁移、只读 API 和 SIGTERM |
| `make dashboard-smoke` | 真实 Chromium 页面、DOM、URL、网络和错误恢复 |
| `make docker-smoke` | 容器 CLI、权限、Compose 健康检查和生命周期 |
| `make docker-dashboard-smoke` | 容器形态浏览器验收 |
| `make ci` | 格式、静态检查、竞态测试、构建、CLI/行情/分钟冒烟和发布包 |
| `make package` | Linux 发布包；`BUILD_GOARCH=amd64` 或 `arm64` |

浏览器测试额外需要 Python 3.12 与 Playwright Chromium，策略模型测试需要 Node.js 20+；这些依赖不属于生产运行时。资源紧张时可使用 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci`。

### 项目结构

| 路径 | 职责 |
| --- | --- |
| `cmd/stock-watcher` | 唯一生产入口和命令编排 |
| `cmd/bottom-study` | 底部形态与止损/持有期研究工具 |
| `cmd/resolve-pool` | 股池名称与代码核对辅助工具 |
| `internal/config` | 内置生产配置、环境变量覆盖和严格校验 |
| `internal/calendar` | 北京时间交易日、节假日、T+1 与过期窗口 |
| `internal/market` | 行情源协议、分页、复权、超时与回退 |
| `internal/storage` | CIFS 检查、SQLite、原子状态与只读看板查询 |
| `internal/monitor` | 整池同步、启动刷新、调度、重试和账本迁移 |
| `internal/selection` | 趋势筛选与报告事件组装 |
| `internal/strategy` | 前复权特征、成交模拟、排名、滚动验证和留出样本 |
| `internal/dashboard` | 内嵌 HTML/CSS/JavaScript、SVG K 线和只读 API |
| `internal/notify` | 版本化消息、通道接口和投递账本 |
| `internal/feishu` | 卡片、签名、HTTP/业务码确认和错误分类 |
| `internal/logging` | 调用位置、脱敏、stdout/滚动文件和文本/JSON 输出 |
| `tests/` | CLI、容器、行情源、分钟和浏览器冒烟 |
| `docs/` | 数据、部署、策略和实验文档 |

## 文档导航

| 主题 | 文档 |
| --- | --- |
| 环境与配置 | [环境变量](docs/environment-variables.md) · [工程结构](docs/repository-layout.md) |
| 数据与运行 | [行情来源与回退](docs/data-sources.md) · [部署与排障](docs/deployment.md) |
| 开发与发布 | [开发、CI 与发布包](docs/development.md) · [测试范围](tests/README.md) |
| 策略研究 | [推荐门槛诊断](docs/strategy-diagnostics.md) · [策略实验室](docs/experiments.md) · [实验快照](docs/experiment-result-snapshots.md) |
| 实验专题 | [条件入场](docs/experiments-conditional-entry.md) · [指标组合](docs/experiments-indicators.md) · [次日触达](docs/experiments-next-day.md) · [缠论门控](docs/experiments-chan.md) |
| 研究与工具 | [单股价格策略](docs/单股价格策略.md) · [底部形态](docs/bottom-signals.md) · [行业与大盘对比](docs/sector-market-comparison.md) · [持有速度](docs/holding-speed.md) |
| 容器 | [Compose 部署与升级](docker/README.md) |

## 已知边界

- 仅覆盖沪深 A 股，不提供逐笔行情、盘口深度或自动交易。
- 日线来自多个上游，分钟线当前只采集腾讯 5 分钟原始线并派生 15/30/60 分钟；上游窗口和修订规则可能变化。
- 内置交易日历当前覆盖 2026 年，过期后必须升级；测试覆盖变量不得用于生产。
- 网页没有账号、角色和写入接口，远程暴露必须依赖受控网络或已有认证代理。
- 飞书投递为至少一次语义，请求成功后、确认落盘前崩溃可能产生重复消息。
- 共享盘故障、临时网络错误或数据覆盖不足时，本轮不会发布缓存推荐；已有数据保留等待重试。
- 研究结果受样本、费用假设、流动性和成交边界影响，历史统计不代表未来收益。

## 贡献

欢迎提交问题、文档修正和代码改进。提交 PR 前建议：

1. 从最新 `main` 创建独立分支，控制改动范围。
2. 运行受影响的单元/集成测试；行为变化至少运行 `make ci`，前端变化另跑 `make dashboard-smoke`。
3. 不提交凭证、行情数据库、日志、浏览器截图或私有环境文件。
4. 在 PR 中说明改动、用户可见行为和验证命令；详细规范见[贡献指南](CONTRIBUTING.md)，并使用仓库的 [PR 模板](.github/pull_request_template.md)。

## License

本项目采用 [Apache License 2.0](LICENSE)。提交贡献即表示同意按该许可证授权。

---

如果这个项目对你有帮助，欢迎通过 Issue 分享使用场景、数据源问题和策略验证结果。
