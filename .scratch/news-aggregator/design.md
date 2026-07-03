# Design: 新闻聚合与飞书推送（含板块增强）

## Context

### Current State
Stock Watcher currently polls Sina/Tencent for real-time quotes and pushes price alerts + announcement cards to Feishu. It has no news aggregation capability and no sector/board awareness.

### Constraints
- Must reuse existing `chat_sender.send_feishu_card()` infrastructure
- Must use free data sources (AKShare MIT, yfinance Apache 2.0)
- Must respect East Money API rate limits (this server's IP is aggressively throttled)
- DeepSeek API budget ≤ 30 calls/min total
- No new persistent database (JSONL + in-memory only)

### Stakeholders
- Single user: push notifications to personal Feishu group chat

## Goals
1. Aggregate news from ≥ 5 free Chinese financial sources into unified `NewsItem` stream
2. Match news headlines to concept sectors via keyword mapping
3. Fetch constituent stocks with real-time quote data embedded
4. Score stocks by short-term trend strength (technical indicators + DeepSeek AI)
5. Push enriched Feishu cards with tiered stock display (positions > watchlist > trend)
6. Add scheduled pushes (market open, close summary, daily briefings)

## Architecture Overview

```
                      config.yaml
                           │
                    ┌──────┴──────┐
                    │  NewsConfig  │
                    └──────┬──────┘
                           │
              ┌────────────┼────────────────┐
              │            │                 │
     ┌────────▼───┐ ┌─────▼──────┐ ┌───────▼─────────┐
     │NewsAggregator│ │NewsClassifier│ │ScheduledPush   │
     └──────┬──────┘ └─────┬──────┘ └──────┬──────────┘
            │              │               │
     ┌──────┼──────┐       │               │
     │      │      │       │               │
  ┌──▼─┐ ┌─▼──┐ ┌─▼──┐ ┌──▼────┐  ┌──────▼──────────┐
  │ EM │ │Sina│ │CLS │ │DeepSeek│  │chat_sender      │
  │Stock│ │Flash│ │Tele│ │  API   │  │send_feishu      │
  └────┘ └────┘ └────┘ └───────┘  └─────────────────┘
     │      │      │
  ┌──▼──────▼──────▼──┐     ┌────────────────────────┐
  │     AKShare        │     │  SectorEnrichment      │
  │  (MIT, no API key) │     │  ┌──────────────────┐  │
  └────────────────────┘     │  │ SectorMatcher    │  │
                              │  │ (keyword→sector) │  │
  Also: yfinance → YahooNews  │  └────────┬─────────┘  │
         (HK stocks only)     │           │            │
                              │  ┌────────▼─────────┐  │
                              │  │ConstituentFetcher│  │
                              │  │ (sector→stocks)  │  │
                              │  └────────┬─────────┘  │
                              │           │            │
                              │  ┌────────▼─────────┐  │
                              │  │ TrendScorer      │  │
                              │  │ (腾讯源, 100分制) │  │
                              │  └────────┬─────────┘  │
                              │           │            │
                              │  ┌────────▼─────────┐  │
                              │  │ TrendRanker      │  │
                              │  │ (规则→DeepSeek)  │  │
                              │  └────────┬─────────┘  │
                              │           │            │
                              │  ┌────────▼─────────┐  │
                              │  │ CardBuilder      │  │
                              │  │ (enriched card)  │  │
                              │  └──────────────────┘  │
                              └────────────────────────┘
```

## Key Design Decisions

### D1: AKShare as primary library

**Decision**: Use AKShare for A-share news sources (East Money, Sina, CLS) and sector/board data.
**Rationale**: MIT license, actively maintained, wraps all major free Chinese financial APIs, zero authentication. Sector functions (`stock_board_concept_*`) provide real-time constituent data with embedded quote fields.
**Trade-off**: AKShare is a heavy dependency (~50MB). Mitigated by only importing needed submodules.

### D2: yfinance for HK news only

**Decision**: Use yfinance for HK stock news, not A-shares.
**Rationale**: yfinance has poor A-share coverage; AKShare covers A-shares well. yfinance excels at HK ticker news.
**Trade-off**: Extra dependency. yfinance is lightweight (~2MB).

### D3: NewsItem as standalone dataclass

**Decision**: New `NewsItem` in `models.py`, separate from `Announcement`.
**Rationale**: Announcements are official company filings with different fields (org_id, adjunct_url, etc.). News is headlines + summaries with different metadata.
**Trade-off**: Two parallel models. Acceptable because they serve different use cases.

### D4: Per-source fetcher, not monolithic

**Decision**: Separate fetcher class per source, with shared `BaseNewsFetcher` ABC.
**Rationale**: Error isolation, independent polling intervals, easy to add/remove sources.
**Trade-off**: More boilerplate. Each fetcher is ~50 lines — acceptable.

### D5: AI classification as post-filter, not pre-filter

**Decision**: Fetch all news → keyword pre-filter → AI classify → push only P0.
**Rationale**: Keywords provide immediate short-circuit; AI handles nuanced judgment. Batch classification minimizes API calls.
**Trade-off**: Still fetches more news than we push. Acceptable — network is cheap, AI API is the bottleneck.

### D6: Scheduled pushes in-process, no external cron

**Decision**: `asyncio` timer-based scheduling within the existing app loop.
**Rationale**: No external dependencies, shares TradingCalendar, hot-reloads with config.
**Trade-off**: Push timing drifts if main loop blocks. Mitigated by checking "is it past the target time?" on wake, not "sleep exactly N seconds".

### D7: News module as subpackage

**Decision**: `src/stock_watcher/news/` package with `__init__.py` re-exporting public API.
**Rationale**: Keep news-related code together, avoid bloating top-level package.
**Trade-off**: New directory. Simple with `__init__.py` re-exports.

### D8: Keyword matching for sector identification

**Decision**: Use config-driven keyword → sector mapping table, not AI-based NLP.
**Rationale**: 495 concept sectors have descriptive Chinese names; rule-based matching is fast (O(n) string search), deterministic, zero API cost. AI-based sector identification would add latency and hallucination risk.
**Trade-off**: Requires manual keyword table maintenance. Acceptable — sector names are stable, keyword table is config-driven (hot-reload).

### D9: Tencent source for historical daily data

**Decision**: Use `akshare.stock_zh_a_hist_tx` (Tencent) for 60-day OHLCV data, NOT `stock_zh_a_hist` (East Money).
**Rationale**: East Money aggressively rate-limits this server — SSL connections are reset after 3-4 requests. Tencent source is more lenient and provides sufficient fields (OHLC + amount) for our indicator calculations.
**Trade-off**: Tencent source lacks explicit volume field (has amount/成交额 instead). We use amount as volume proxy for the volume_ratio indicator — acceptable for the scoring model.

### D10: Technical indicator scoring as pre-filter

**Decision**: Compute MA/MACD/RSI/consecutive-up/volume-ratio locally with a 100-point weighted rubric, before sending candidates to DeepSeek.
**Rationale**: Technical indicators are deterministic and cheap to compute. Running them first shrinks the AI input from potentially 35+ stocks down to 10, saving DeepSeek token costs.
**Trade-off**: The rubric weights are fixed constants. Mitigated by config-driven weights in `config.yaml` for easy tuning.

### D11: Two-stage ranking (rule → AI)

**Decision**: Rule-based scoring (D10) narrows to Top 10, then DeepSeek selects final Top 5 with confidence scores.
**Rationale**: DeepSeek can assess qualitative factors (e.g., sector rotation, news sentiment alignment) that pure technical rules miss. The 10→5 narrowing keeps API costs bounded.
**Trade-off**: Adds ~1-2s latency to the push pipeline. Acceptable — news push is not real-time trading, 2s delay is negligible.

### D12: Tiered display priority

**Decision**: Card displays stocks in fixed priority: positions (Tier 1) > watchlist (Tier 2) > trend-ranked (Tier 3). Cap at 8 total.
**Rationale**: User's own holdings are most actionable, followed by stocks they're watching. Trend-ranked stocks provide discovery value as filler.
**Trade-off**: Strong trend stocks the user doesn't own may be pushed below irrelevant positions. Mitigated by the 8-stock cap — rarely will Tier1+Tier2 fill all 8 slots.

## Data Flow

```
1. NewsAggregator.aggregate(watchlist_codes)  [timer: every N seconds]
   ├─ EastMoneyStockNewsFetcher.fetch(codes)  → list[NewsItem]
   ├─ EastMoneyFlashNewsFetcher.fetch()       → list[NewsItem]
   ├─ SinaFlashNewsFetcher.fetch()            → list[NewsItem]
   ├─ CLSTelegraphFetcher.fetch()             → list[NewsItem]
   └─ YahooNewsFetcher.fetch(codes)           → list[NewsItem]
            │
2. Dedup by URL + title similarity
            │
3. Filter by watchlist relevance (related_codes ∩ watchlist)
            │
4. NewsClassifier.classify_batch(items)
   ├─ Keyword pre-filter (critical → P0, ignore → P2)
   └─ DeepSeek API → {level, reason}
            │
5. Filter to P0 items only
            │
6. [NEW] SectorMatcher.match(news) → list[sector_names]
            │
7. [NEW] SectorConstituentFetcher.fetch(sectors) → list[SectorStock] with real-time quotes
            │
8. [NEW] TrendScorer.score_batch(stocks) → list[TrendScore] (腾讯源60天日线)
            │
9. [NEW] TrendRanker.rank(stocks, positions, watchlist) → TieredStockList
   ├─ Tier 1: position stocks (持仓)
   ├─ Tier 2: watchlist stocks (自选)
   └─ Tier 3: rule top10 → DeepSeek top5
            │
10. [MODIFIED] NewsCardBuilder.build_card(news, sector, tiered_stocks)
   ├─ Header: sector name + change%
   ├─ Tier 1: 📌 你的持仓 — 股票 + 行情行
   ├─ Tier 2: 👁 你关注的 — 股票 + 行情行
   └─ Tier 3: 🔥 趋势最强 — 股票 + 行情行 + AI置信度
            │
11. chat_sender.send_feishu_card(cfg, card)
            │
12. Mark as seen in news_seen.jsonl
```

## Module Tree

```
src/stock_watcher/
├── news/
│   ├── __init__.py          # public API exports
│   ├── base.py              # BaseNewsFetcher ABC, NewsItem, SectorStock, TrendScore
│   ├── eastmoney_stock.py   # EastMoneyStockNewsFetcher
│   ├── eastmoney_flash.py   # EastMoneyFlashNewsFetcher
│   ├── sina_flash.py        # SinaFlashNewsFetcher
│   ├── cls_telegraph.py     # CLSTelegraphFetcher
│   ├── yahoo_news.py        # YahooNewsFetcher
│   ├── aggregator.py        # NewsAggregator
│   ├── classifier.py        # NewsClassifier
│   ├── sector_matcher.py    # [NEW] SectorMatcher — keyword→sector matching
│   ├── trend_scorer.py      # [NEW] TrendScorer — technical indicator scoring
│   ├── trend_ranker.py      # [NEW] TrendRanker — rule+AI ranking
│   └── card_builder.py      # [MODIFIED] NewsCardBuilder — sector + tiered quotes
├── scheduled_push.py        # ScheduledPush
├── models.py                # + NewsItem, SectorStock, TrendScore dataclasses
├── config.py                # + NewsConfig, sector_keywords mapping
└── app.py                   # + integration
```

## Config Schema

### `chat:` section (extended)

```yaml
chat:
  # 行情/告警/汇总推送
  feishu_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/..."
  feishu_secret: ""
  # 新闻推送专用 Webhook（独立频道，避免刷屏）
  news_webhook: "https://open.feishu.cn/open-apis/bot/v2/hook/66be3550-d40d-4b8e-8078-e4c1dba6cc71"
  news_secret: ""
```

### `news:` section (new)

```yaml
news:
  # 新闻源开关
  sources:
    eastmoney_stock: true      # 东财个股新闻
    eastmoney_flash: true      # 东财全球快讯
    sina_flash: true           # 新浪全球快讯
    cls_telegraph: true        # 财联社电报
    yahoo_news: true           # 雅虎港股新闻

  # 轮询间隔（秒）
  poll_interval: 120

  # 板块关键词映射（新闻标题→概念板块）
  sector_keywords:
    机器人: ["机器人执行器", "人形机器人", "减速器"]
    新能源: ["新能源车", "固态电池", "光伏设备", "风电设备"]
    AI: ["人工智能", "ChatGPT", "算力概念", "AIGC概念"]
    半导体: ["半导体", "芯片", "光刻机(胶)", "存储芯片"]
    医药: ["创新药", "CRO", "中药", "医疗器械"]
    消费: ["新零售", "跨境电商", "预制菜"]
    军工: ["航天航空", "船舶制造", "北斗导航"]
    金融: ["券商", "银行", "保险"]

  # 技术指标打分权重
  trend_scoring:
    ma_weight: 30              # MA多头排列
    macd_weight: 25            # MACD方向
    rsi_weight: 20             # RSI强度
    consecutive_up_weight: 15  # 连涨天数
    volume_ratio_weight: 10    # 量比放大
    lookback_days: 60          # 历史数据回溯天数
    min_score_threshold: 50    # 初筛最低分
    top_n_candidates: 10       # 送入 DeepSeek 的候选数量
    top_n_final: 5             # DeepSeek 最终精选数量
    max_stocks_per_card: 8     # 单卡片最大股票数

  # 重要性分类关键词
  critical_keywords:
  - 停牌
  - 退市
  - 重大资产重组
  - 并购
  - 业绩暴雷
  - 立案调查
  - 强制退市
  - 暂停上市

  ignore_keywords:
  - 龙虎榜
  - 大宗交易
  - 融资融券

  # 定时推送
  scheduled_push:
    morning_briefing:
      enabled: true
      time: "09:00"
    market_open:
      enabled: true
      time: "09:25"
    a_share_close:
      enabled: true
      time: "15:05"
    hk_close:
      enabled: true
      time: "16:15"
    evening_briefing:
      enabled: true
      time: "20:00"

  # 已推送新闻 ID 持久化
  seen_ids_path: "news_seen.jsonl"
```

## Error Handling Strategy

| Error Type | Strategy |
|------------|----------|
| Single source fetch failure | Log warning, return [], continue others |
| All sources fail | Log error, skip this cycle, no push |
| Sector keyword match failure | Return [], fall back to basic news card |
| Sector API failure (East Money) | Log warning, skip that sector, continue others |
| Tencent historical data failure | TrendScore = 0, stock enters Tier 3 with score 0 |
| DeepSeek API failure (classify) | All news → P1 (notable), no push (safe default) |
| DeepSeek API failure (trend rank) | Fall back to rule-ranking by total_score |
| Feishu webhook failure | Log error, don't retry (news is time-sensitive) |
| AKShare import failure | Graceful degradation: skip that source, log warning |
| Rate limit hit | Backoff per source (exponential, max 5 min) |
| Source disabled after 5 consecutive failures | Auto-reenable after 30 min |
| Insufficient historical data (< 20d) | TrendScore = 0, include in Tier 3 with note |

## Risks And Trade-Offs

| Risk | Likelihood | Impact | Mitigation |
|------|:----------:|:------:|------------|
| East Money API blocks server IP entirely | Medium | High | Fall back to Tencent source for all data; `stock_zh_a_hist_tx` already verified working |
| DeepSeek API cost overrun | Medium | Medium | Rate limiter at 30 calls/min; skip DeepSeek for trend if Tier1+Tier2 ≥ 8; configurable weights to tune false positives |
| Keyword mapping becomes stale | Low | Low | Config-driven, hot-reloadable; startup validation against live sector list warns on unmatched sectors |
| Trend scoring produces false positives (pushing weak stocks as "strong") | Medium | Medium | DeepSeek second-stage review catches rule errors; weights configurable in `config.yaml` |
| 飞书 card too large with 8 stock rows | Low | Low | Each stock row is ~80 chars (compact format); 8 rows ≈ 640 chars — well within Feishu card limits |
| Historical data fetch timeouts (60 stocks × Tencent API) | Medium | Medium | Sequential with 1s interval = ~60s worst case; scores are cached within session; failed fetches log warning and score=0 |
