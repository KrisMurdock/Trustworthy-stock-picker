# Spec: 新闻聚合 (News Aggregation)

## Overview

从多个免费财经新闻源拉取新闻，以统一的 `NewsItem` 格式输出，支持去重和按自选股关联。

---

## Requirements

### NA-S01: 统一新闻模型

SHALL define `NewsItem` dataclass with fields:
- `id: str` — unique identifier (hash of source + url)
- `source: str` — source name (e.g. "eastmoney", "sina", "cls", "yahoo")
- `title: str` — headline
- `summary: str` — content preview (≤ 200 chars)
- `url: str` — link to full article
- `published_at: str` — ISO 8601 timestamp
- `related_codes: list[str]` — related stock codes (e.g. ["sh600519", "hk00700"])
- `source_priority: int` — 1=high (official), 2=medium (mainstream), 3=low (social)

If a field is unavailable from source, SHALL be empty string or empty list.

### NA-S02: 新闻源抽象基类

SHALL define `BaseNewsFetcher` abstract class with:
- `async fetch() → list[NewsItem]` — fetch latest news
- `source_name: str` — class attribute
- `source_priority: int` — class attribute
- `configurable enabled/disabled` via constructor

Each concrete fetcher SHALL handle its own HTTP errors gracefully (return `[]` on failure, log warning).

### NA-S03: 东财个股新闻 (EastMoneyStockNewsFetcher)

SHALL use `akshare.stock_news_em(symbol)` to fetch per-stock news.
SHALL accept a list of stock codes (A-share only, sh/sz prefix).
SHALL strip prefix before calling akshare (e.g. "sh600519" → "600519").
SHALL set `related_codes` to `[code]` for each news item.
SHALL deduplicate by URL within source.

### NA-S04: 东财全球快讯 (EastMoneyFlashNewsFetcher)

SHALL use akshare to fetch global financial flash news.
SHALL NOT require stock code parameter (market-wide feed).
SHALL attempt to extract related stock codes from headline text using regex (e.g. "贵州茅台" → "sh600519").
SHALL set `related_codes` to `[]` if no stock can be identified.

### NA-S05: 新浪全球快讯 (SinaFlashNewsFetcher)

SHALL use `akshare.stock_info_global_sina()` to fetch flash news.
SHALL handle rate limiting with exponential backoff.
Same stock-code extraction logic as NA-S04.

### NA-S06: 财联社电报 (CLSTelegraphFetcher)

SHALL use `akshare.stock_telegraph_cls()` to fetch breaking news.
CLS telegraph items are short (≤ 50 chars typical); SHALL use title as summary.
SHALL set `source_priority = 1` (CLS is primary breaking news source).

### NA-S07: 雅虎财经新闻 (YahooNewsFetcher)

SHALL use `yfinance.Ticker(ticker).news` for HK stocks.
SHALL accept list of HK stock codes (hk prefix).
SHALL convert codes to Yahoo format (e.g. "hk00700" → "0700.HK").
SHALL set `related_codes` to `[code]` for each news item.
SHALL NOT be used for A-share stocks (yfinance coverage poor; emit warning if attempted).

### NA-S08: 新闻聚合器 (NewsAggregator)

SHALL orchestrate all enabled fetchers.
SHALL call all fetchers concurrently with `asyncio.gather`.
SHALL deduplicate across sources by URL + title similarity (Levenshtein ratio > 0.85).
SHALL filter to only news relevant to watchlist stocks (if `related_codes` non-empty and intersects watchlist) OR market-wide news with significant impact.
SHALL persist seen news IDs in JSONL file (`news_seen.jsonl`) with rotation at 5000 entries.
SHALL support configurable poll interval per source.

### NA-S09: 错误隔离

SHALL NOT let one fetcher failure block others.
SHALL emit structured log on per-source fetch errors.
SHALL track per-source consecutive failures; when > 5, temporarily disable that source for 30 minutes.

---

## Acceptance Criteria

- [ ] `NewsItem` dataclass defined in `models.py`
- [ ] 5 concrete fetchers implemented, each with ≥ 1 unit test (mock HTTP)
- [ ] `NewsAggregator.aggregate()` returns deduplicated, watchlist-filtered `list[NewsItem]`
- [ ] Cross-source dedup works (same URL → single item)
- [ ] Title-similarity dedup works (Levenshtein > 0.85)
- [ ] One fetcher failure does not affect others
- [ ] `news_seen.jsonl` persists and rotates correctly
