# Execution Contract: 新闻聚合与飞书推送（含板块增强）

> **Change**: `news-aggregator`
> **Generated**: 2025-07-03
> **Status**: ⏳ awaiting approval (DP-3)
> **Supersedes**: previous contract (2025-07-02) — scope expanded with sector enrichment

---

## 1. Intent Lock

**Problem**: 用户只能收到巨潮资讯网的公司公告推送，缺少财经新闻覆盖，且推送消息只有标题+摘要链接，无法获知新闻影响了哪些板块和个股、这些个股当前行情如何。

**Solution**: 接入 ≥5 个免费新闻源 → DeepSeek AI 筛选重大新闻 → 关键词匹配概念板块 → 获取成分股实时行情（最新价/涨跌幅/最高/最低/今开）→ 技术指标打分（MA/MACD/RSI/连涨/量比）→ DeepSeek 精选趋势 Top5 → 飞书卡片按三级展示（📌持仓 > 👁自选 > 🔥趋势）。

**Scope fence**: A 股概念板块（495个）。不包括港股板块、美股、TUI 内展示、全文正文抓取、情感分析。

---

## 2. Approved Behavior Summary

### 新闻聚合 (news-aggregation · 9 requirements)

| ID | Requirement | Task |
|----|-------------|:----:|
| NA-S01 | `NewsItem` dataclass — id, source, title, summary, url, published_at, related_codes, source_priority | T01 |
| NA-S02 | `BaseNewsFetcher` ABC — async fetch(), source_name, source_priority, configurable enabled | T03 |
| NA-S03 | EastMoneyStockNewsFetcher — `akshare.stock_news_em()`, per-watchlist, prefix strip | T03 |
| NA-S04 | EastMoneyFlashNewsFetcher — global flash, regex code extraction | T04 |
| NA-S05 | SinaFlashNewsFetcher — `akshare.stock_info_global_sina()`, rate-limit backoff | T05 |
| NA-S06 | CLSTelegraphFetcher — `akshare.stock_telegraph_cls()`, short items → title as summary | T06 |
| NA-S07 | YahooNewsFetcher — `yfinance.Ticker().news`, hk00700→0700.HK conversion | T07 |
| NA-S08 | NewsAggregator — concurrent fetch, URL+Levenshtein dedup, watchlist filter, JSONL persistence | T08 |
| NA-S09 | Error isolation — per-source failure ≠ all fail; auto-disable after 5 consecutive failures × 30min | T08 |

### 新闻分类 (news-classification · 5 requirements)

| ID | Requirement | Task |
|----|-------------|:----:|
| NC-S01 | 3-level classification: critical(P0)/notable(P1)/ignore(P2) | T09 |
| NC-S02 | Structured DeepSeek prompt with temperature=0.1, max_tokens=50 | T09 |
| NC-S03 | Batch ≤ 5 items/call, retry 2× with 1s backoff, safe default=notable, ≤30 calls/min | T09 |
| NC-S04 | In-memory classification cache by news_id (session only) | T09 |
| NC-S05 | Keyword pre-filter short-circuit: critical_keywords → P0, ignore_keywords → P2 | T09 |

### 板块增强 (sector-enrichment · 4 requirements) [NEW]

| ID | Requirement | Task |
|----|-------------|:----:|
| SE-S01 | SectorMatcher — config-driven keyword→sector mapping, case-insensitive, partial match, dedup, validated against live 495-sector list | T14 |
| SE-S02 | SectorConstituentFetcher — `akshare.stock_board_concept_cons_em()`, return `SectorStock` with full real-time quotes, cross-sector stock dedup, error isolation, ≥3s interval | T14 |
| SE-S03 | TrendScorer — 60-day Tencent source OHLCV, 100-point rubric (MA30+MACD25+RSI20+Consec15+Vol10), per-stock per-day memory cache, insufficient data → score=0 | T15 |
| SE-S04 | TrendRanker — Tier classification (持仓>自选>趋势), rule filter score≥50 → top10 candidates → DeepSeek structured prompt → top5, cap at 8 stocks, fallback to rule-ranking, skip AI if Tier1+2 ≥8 | T16 |

### 新闻推送 (news-push · 4 requirements) [MODIFIED]

| ID | Requirement | Task |
|----|-------------|:----:|
| NP-S01 | Enriched Feishu card: sector header + tier labels (📌👁🔥) + stock rows with quote fields, fallback to basic card when no sector matched | T10 |
| NP-S02 | Per-sector push dedup (30min), multi-stock single card | T10 |
| NP-S03 | Rate cap 10 pushes/min + overflow queue | T10 |
| NP-S04 | Use `cfg.news_webhook` (independent from main webhook) | T10 |

### 定时推送 (scheduled-push · 7 requirements)

| ID | Requirement | Task |
|----|-------------|:----:|
| SP-S01~S07 | 5 push types (09:25/15:05/16:15/09:00/20:00), TradingCalendar integration, configurable times, missed-push skip, individual enable/disable | T11 |

---

## 3. Out of Scope

- 美股新闻
- 港股板块映射（仅 A 股概念板块）
- 新闻全文抓取/正文提取
- 历史新闻检索/回看
- TUI 内新闻展示
- 情感分析
- 实时流式行情（轮询间隔 ≥ 60s）

---

## 4. Constraints

| 约束 | 来源 |
|------|------|
| Python ≥ 3.10, async httpx, 复用现有基础设施 | proposal |
| AKShare 用于板块+实时行情（东方财富 API, MIT） | D1 |
| 个股历史日线使用腾讯源 `stock_zh_a_hist_tx`（东方财富历史接口限流） | D9 |
| yfinance 用于港股新闻（Apache 2.0） | D2 |
| 不新增持久化数据库（JSONL + 内存） | D6 |
| DeepSeek API: 分类 + 趋势排序合计 ≤ 30 calls/min | NC-S03, SE-S04 |
| 飞书 Webhook: ≤ 10 pushes/min（远低于 100/min 上限） | NP-S03 |
| 东方财富 API 限流: 分页 ≤ 4 pages/cycle, interval ≥ 3s | D1 |
| 板块关键词映射 config-driven, hot-reloadable | D8 |
| 技术指标权重 configurable（`trend_scoring` 配置段） | D10 |
| 卡片展示上限 8 只股票 | NP-S01 |

---

## 5. Execution Batches

### Batch 1 — Foundation (T01–T02)
- **T01**: NewsItem + SectorStock + TrendScore models, NewsConfig
- **T02**: `pip install akshare yfinance` + smoke tests (concept board, constituent, Tencent hist)

**Done when**: dataclasses pass unit test; all 3 akshare smoke tests pass.

### Batch 2 — Fetchers (T03–T07) [parallel]
- **T03**: EastMoneyStockNewsFetcher
- **T04**: EastMoneyFlashNewsFetcher
- **T05**: SinaFlashNewsFetcher
- **T06**: CLSTelegraphFetcher
- **T07**: YahooNewsFetcher

**Done when**: all 5 fetcher unit tests pass with mocked APIs.

### Batch 3 — Aggregation + Classification (T08–T09)
- **T08**: NewsAggregator + cross-source dedup + JSONL persistence
- **T09**: NewsClassifier (keyword pre-filter + DeepSeek batch classify)

**Done when**: integration test passes (aggregate → classify with mock data).

### Batch 4 — Sector Enrichment (T13–T16) [NEW]
- **T13**: `sector_keywords` config + `SectorKeywordsConfig`
- **T14**: SectorMatcher (keyword→sector matching, live list validation)
- **T15**: TrendScorer (100-point technical indicator scoring, Tencent source)
- **T16**: TrendRanker (rule pre-filter + DeepSeek top5, tiered output)

**Done when**: all 4 unit tests pass; TrendRanker produces correct Tier grouping for mock data.

### Batch 5 — Push + Scheduling (T10–T11)
- **T10**: NewsCardBuilder — enriched card with sector overview + tiered stock quotes, rate cap, per-sector dedup
- **T11**: ScheduledPush — 5 push types, configurable, missed-push skip

**Done when**: card JSON validates against Feishu schema; scheduled push fires at correct times in test.

### Batch 6 — Integration (T12)
- **T12**: Wire full pipeline into app.py, config.yaml defaults, e2e smoke test

**Done when**: app starts, news → sector match → trend rank → card push flows end-to-end.

---

## 6. Test Obligations

> **Rule**: Every SHALL requirement describing observable behavior must have a test that fails before implementation.

| Requirement | Test Type | Test File |
|-------------|-----------|-----------|
| NA-S01 NewsItem/SectorStock/TrendScore models | unit | `tests/test_news_models.py` |
| NA-S03 EM Stock Fetcher | unit (mock akshare) | `tests/test_news_em_stock.py` |
| NA-S04 EM Flash Fetcher | unit (mock akshare) | `tests/test_news_em_flash.py` |
| NA-S05 Sina Flash Fetcher | unit (mock akshare) | `tests/test_news_sina_flash.py` |
| NA-S06 CLS Telegraph | unit (mock akshare) | `tests/test_news_cls.py` |
| NA-S07 Yahoo News | unit (mock yfinance) | `tests/test_news_yahoo.py` |
| NA-S08/N09 Aggregator + dedup + error isolation | integration | `tests/test_news_aggregator.py` |
| NC classifier (S01–S05) | unit (mock httpx) | `tests/test_news_classifier.py` |
| SE-S01 SectorMatcher | unit | `tests/test_sector_matcher.py` |
| SE-S03 TrendScorer | unit (mock data) | `tests/test_trend_scorer.py` |
| SE-S04 TrendRanker | unit (mock DeepSeek) | `tests/test_trend_ranker.py` |
| NP card builder (S01–S04) | unit (schema validation) | `tests/test_news_card_builder.py` |
| SP scheduled push (S01–S07) | unit (mock clock) | `tests/test_scheduled_push.py` |
| E2E pipeline | integration | `tests/test_news_e2e.py` |

---

## 7. Review Gates

| Gate | When | What |
|------|------|------|
| **R1** | After Batch 2 | All 5 fetcher unit tests pass; verify AKShare API signatures still match |
| **R2** | After Batch 3 | Aggregator dedup logic correct; classifier returns valid JSON for 10 sample items |
| **R3** | After Batch 4 | SectorMatcher returns correct sectors for 20 sample headlines; TrendScorer scores match manual calculation for known stock; TrendRanker tier grouping correct |
| **R4** | After Batch 5 | Card renders in Feishu card builder preview with all tier sections; scheduled push timing verified |
| **R5** | After Batch 6 | Full e2e: app starts, news → sector match → trend rank → push to news webhook |

---

## 8. Rewind Triggers

Stop implementation and return to planning if:

- AKShare `stock_board_concept_name_em()` or `stock_board_concept_cons_em()` API signatures change
- Tencent source `stock_zh_a_hist_tx` becomes unavailable or rate-limited
- DeepSeek API becomes unavailable for > 1 hour (consider keyword-only classification fallback)
- yfinance drops HK news support (scope down to A-share only)
- Any requirement cannot be fulfilled without adding a paid API dependency
- More than 3 of 5 news sources require authentication
- TrendScorer scores consistently contradict manual expectation (> 30% of manual checks disagree)
- East Money blocks all API access from this server (full Tencent fallback needed)

---

## 9. Coverage Cross-Check

**Result: ALL COVERED** ✅

| Spec | Requirements | Tasks | Tests | Status |
|------|:------------:|:-----:|:-----:|:------:|
| news-aggregation | 9 (NA-S01–S09) | T01, T03–T08 | 5 test files | ✅ |
| news-classification | 5 (NC-S01–S05) | T09 | 1 test file | ✅ |
| sector-enrichment | 4 (SE-S01–S04) | T13–T16 | 3 test files | ✅ |
| news-push | 4 (NP-S01–S04) | T10 | 1 test file | ✅ |
| scheduled-push | 7 (SP-S01–S07) | T11 | 1 test file | ✅ |
| **Total** | **29** | **16 tasks** | **14 test files** | ✅ |

- Zero unmapped requirements
- Every SHALL requirement has ≥ 1 test obligation and ≥ 1 task
- Every task has explicit acceptance criteria

---

## 10. Escalation Rules

| Situation | Action |
|-----------|--------|
| AKShare fails to install | Fallback: implement direct HTTP calls to East Money JSONP endpoints |
| East Money API blocks server IP entirely | Migrate all data to Tencent source; `stock_zh_a_hist_tx` already verified |
| News noise too high (> 80% P2) | Tune `critical_keywords` / `ignore_keywords` in config; do not modify code |
| DeepSeek trend ranking consistently wrong | Adjust prompt or fall back to rule-only ranking; escalate to user if persistent |
| Feishu rate limit hit | Increase push interval cap; add exponential backoff to `send_news_card` |
| Scheduled push timing drifts > 30s | Refactor to wall-clock check instead of sleep accumulation |
| Sector keyword match rate < 20% | Expand keyword mapping table; consider adding AI-based sector identification |
| TrendScorer scoring disputes (user disagrees with ranked output) | Adjust weights in `config.yaml` via `trend_scoring` section; do not modify scoring logic |
