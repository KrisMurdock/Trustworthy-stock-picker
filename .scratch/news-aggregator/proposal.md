# Proposal: 新闻聚合与飞书推送（含板块增强）

## Summary

接入所有可用的免费财经新闻接口（A 股 + 港股），构建统一新闻聚合层。通过关键词匹配将新闻关联到概念板块，获取板块成分股实时行情，基于技术指标（MA/MACD/RSI/连涨/量比）和 DeepSeek AI 双重排序精选趋势最强个股，嵌入飞书推送卡片。同时补全飞书定时推送能力（开盘提醒、收盘汇总等）。

---

## Why

用户在飞书群聊只能收到巨潮资讯网的公司公告，缺少财经新闻覆盖（政策、行业、市场动态），且推送消息只有标题+摘要链接，无法获知新闻影响了哪些板块和个股、这些个股当前的行情表现。需要引入多源新闻聚合 + 板块识别 + 个股行情嵌入 + 趋势排序，让推送消息具备决策参考价值。

---

## Motivation

### 当前状态

- ✅ 巨潮资讯网公告（仅 A 股，仅公司官方公告）
- ✅ 飞书 Webhook 推送（价格卡片、告警卡片、汇总卡片、公告卡片）
- ✅ DeepSeek AI 摘要（公告 + 对话回复）
- ❌ 无财经新闻源（政策、行业、市场动态）
- ❌ 无港股新闻覆盖
- ❌ 无定时推送（开盘/收盘）

### 用户价值

- 用户在飞书群聊即可获取自选股相关的重大新闻，无需打开多个 App
- AI 自动筛选重要性，避免信息过载
- 定时简报帮助把握每日市场节奏

---

## What Changes

### 新增模块

| 模块 | 文件 | 说明 |
|------|------|------|
| 新闻模型 | `models.py`（扩展） | `NewsItem` dataclass，统一新闻格式 |
| 东财个股新闻 | `news/eastmoney_stock.py` | 自选股个股新闻（A 股） |
| 东财全球快讯 | `news/eastmoney_flash.py` | 全球财经实时快讯 |
| 新浪全球快讯 | `news/sina_flash.py` | 新浪财经实时快讯 |
| 财联社电报 | `news/cls_telegraph.py` | 财联社电报快讯 |
| 雅虎财经新闻 | `news/yahoo_news.py` | 港股个股新闻（yfinance） |
| 新闻聚合器 | `news/aggregator.py` | 统一调度、去重、按自选股关联 |
| 重要性分类 | `news/classifier.py` | DeepSeek AI 判断新闻重要性 |
| 新闻卡片 | `news/card_builder.py` | 飞书卡片构建（含板块+行情嵌入） |
| 定时推送 | `scheduled_push.py` | 开盘提醒、收盘汇总、定时简报 |
| 板块匹配 | `news/sector_matcher.py` | 新闻标题关键词→概念板块模糊匹配 |
| 趋势打分 | `news/trend_scorer.py` | 拉取60天日线，计算技术指标打分（100分制） |
| 趋势排序 | `news/trend_ranker.py` | 规则初筛 Top10 → DeepSeek 精选 Top5 |

### 修改模块

| 文件 | 变更 |
|------|------|
| `config.py` | 新增 `NewsConfig`；`ChatConfig` 新增 `news_webhook`/`news_secret` |
| `config.yaml` | 新增 `news:` 配置段；`chat:` 新增 `news_webhook` |
| `scheduler.py` | 新增定时推送调度（开盘/收盘/简报时间点） |
| `app.py` | 集成新闻调度器到主循环 |

### 飞书推送补全

| 推送类型 | 触发时机 | 内容 |
|----------|----------|------|
| 开盘提醒 | 交易日 09:25 | 自选股开盘价、涨跌幅概览 |
| 收盘汇总 | 交易日 15:05（A 股）/ 16:15（港股） | 持仓盈亏汇总 + 日内高低 |
| 重大新闻（板块增强） | 实时（轮询发现后即时） | 板块概览 + 分级个股（持仓T1/自选T2/趋势T3）+ 标准行情（涨跌幅/最新价/最高/最低/今开） |
| 定时简报 | 每日 09:00 / 20:00 | 过去 12 小时重要新闻汇总 |

---

## Scope

### In scope

1. 接入 ≥5 个免费新闻源（东财个股、东财快讯、新浪快讯、财联社电报、雅虎财经）
2. 新闻去重（跨源 + 时序）
3. 新闻标题关键词→概念板块模糊匹配（495个概念板块）
4. 板块成分股获取 + 实时行情（最新价/涨跌幅/最高/最低/今开/昨收/换手率/市盈率）
5. 技术指标趋势打分：MA多头(30) + MACD(25) + RSI(20) + 连涨(15) + 量比(10) = 100分制
6. 两级排序：规则初筛 Top10 → DeepSeek AI 精选 Top5
7. DeepSeek AI 重要性三分类（重大/关注/忽略）
8. 飞书卡片推送（仅「重大」级新闻，含板块概览+分级个股行情）
9. 开盘提醒 / 收盘汇总飞书推送
10. `config.yaml` 可配置：新闻源开关、关键词过滤、板块关键词映射、推送开关、推送时间

### Out of scope

- 美股新闻（后续迭代）
- 港股板块映射（仅 A 股概念板块）
- 新闻全文抓取/正文提取（仅推送标题 + 摘要 + 链接）
- 历史新闻检索/回看
- TUI 内新闻展示（纯飞书推送，不在 TUI 新增 UI）
- 情感分析（免费 API 不支持）
- 实时流式行情（轮询间隔 ≥ 60s）

---

## Capabilities

### New Capabilities

- **news-aggregation**: 多源新闻聚合、去重、自选股关联
- **news-classification**: AI 三分类 + 关键词预筛选
- **sector-enrichment**: 板块匹配 + 成分股行情 + 技术指标打分 + 趋势排序
- **news-push**: 飞书卡片推送（含板块+行情嵌入）
- **scheduled-push**: 开盘/收盘/简报定时推送

### Modified Capabilities

- **card-builder**: 扩展卡片格式 → 支持板块概览 + 分级个股 + 标准行情字段
- **config**: 新增 `sector_keywords` 映射表、`trend_scoring` 配置段

---

## Impact

### Affected Code
- `src/stock_watcher/models.py` — 新增 `NewsItem`, `SectorStock`, `TrendScore` dataclasses
- `src/stock_watcher/config.py` — 新增 `NewsConfig`, `SectorKeywordsConfig`, `TrendScoringConfig`
- `src/stock_watcher/news/` — 新增 12 个模块文件（fetchers, aggregator, classifier, sector_matcher, trend_scorer, trend_ranker, card_builder）
- `src/stock_watcher/app.py` — 集成新闻 pipeline 和定时推送
- `config.yaml` / `config.yaml.example` — 新增 `news:` 配置段

### Affected APIs
- **DeepSeek API**: 新增两种调用模式 — 新闻三分类 + 趋势精选排序，合计 ≤ 30 calls/min
- **飞书 Webhook**: 新增独立 `news_webhook` 频道，≤ 10 pushes/min
- **东方财富 API**: 板块+成分股接口（≤ 4 pages/cycle, interval ≥ 3s）
- **腾讯行情 API**: 历史日线数据（`stock_zh_a_hist_tx`）

### Dependencies
- `akshare` ≥ 1.18 — 板块数据 + 腾讯历史数据
- `yfinance` — 港股新闻
- `python-Levenshtein` — 标题去重
- `httpx` — 异步 HTTP（已有）

---

## Constraints

- Python ≥ 3.10，异步 httpx，复用现有基础设施
- AKShare 作为板块+实时行情数据源（东方财富 API，无 API key）
- 个股历史日线使用腾讯源 `stock_zh_a_hist_tx`（东方财富历史接口限流严重）
- yfinance 作为港股补充（Apache 2.0，无 API key）
- 不新增持久化数据库（JSONL 文件 + 内存集合）
- DeepSeek API 调用频率受限：新闻分类 + 趋势排序合计 ≤ 30 calls/min
- 飞书 webhook 频率限制：单机器人 ≤ 100 条/分钟
- 东方财富 API 限流：分页请求连续 ≤ 4 页，间隔 ≥ 3s
