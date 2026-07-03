# Spec: 板块增强 (Sector Enrichment)

## Overview

将新闻标题通过关键词匹配到概念板块，获取板块成分股及实时行情，基于技术指标（MA/MACD/RSI/连涨/量比）进行趋势打分，再通过 DeepSeek AI 精选短期趋势最强的 Top5 个股，为推送卡片提供分级排序依据。

---

## Requirements

### SE-S01: 板块关键词匹配 (SectorMatcher)

SHALL maintain a keyword→sector mapping table in `config.yaml` under `news.sector_keywords`.

Mapping format:
```yaml
news:
  sector_keywords:
    机器人: ["机器人执行器", "人形机器人", "减速器"]
    新能源: ["新能源车", "固态电池", "光伏设备"]
    AI: ["人工智能", "ChatGPT", "算力概念"]
```

SHALL perform case-insensitive keyword matching against news `title` and `summary` fields.

SHALL use partial matching: if news title contains "机器人产业爆发", keyword "机器人" SHALL match.

SHALL return deduplicated list of sector names (multiple keywords may map to same sector).

SHALL cache the 495 concept sector name list at startup (via `akshare.stock_board_concept_name_em()`) for validation — only return sectors that exist in the live list.

SHALL return empty list `[]` if no keyword matches.

SHALL load keyword mappings once at startup; changes require config hot-reload.

#### Scenario: 新闻标题命中关键词

- **WHEN** news title is "机器人执行器概念股午后异动拉升"
- **THEN** `SectorMatcher.match(news)` SHALL return `["机器人执行器"]`

#### Scenario: 一个关键词映射多个板块

- **WHEN** `sector_keywords` maps "机器人" → `["机器人执行器", "人形机器人", "减速器"]` and title contains "机器人概念"
- **THEN** `SectorMatcher.match(news)` SHALL return `["机器人执行器", "人形机器人", "减速器"]`

#### Scenario: 多个关键词命中同一板块

- **WHEN** two different keywords both map to "新能源车"
- **THEN** sector name SHALL appear only once in result (dedup)

#### Scenario: 无关键词命中

- **WHEN** news title is "央行发布货币政策执行报告"
- **THEN** `SectorMatcher.match(news)` SHALL return `[]`

---

### SE-S02: 板块成分股获取 (SectorConstituentFetcher)

SHALL use `akshare.stock_board_concept_cons_em(sector_name)` to fetch constituent stocks for each matched sector.

SHALL return list of `SectorStock` dataclass with fields:
- `code: str` — stock code (e.g. "688017")
- `name: str` — stock name (e.g. "绿的谐波")
- `sector: str` — source sector name
- `price: float` — 最新价
- `change_pct: float` — 涨跌幅 (%)
- `high: float` — 今日最高
- `low: float` — 今日最低
- `open: float` — 今开
- `prev_close: float` — 昨收
- `volume: int` — 成交量
- `turnover: float` — 换手率 (%)
- `pe: float` — 市盈率-动态
- `pb: float` — 市净率

SHALL deduplicate stocks by `code` when a stock appears in multiple matched sectors (keep first occurrence).

SHALL handle per-sector fetch errors independently: one sector failing SHALL NOT block others.

SHALL skip sectors whose name is not found in the live sector list.

SHALL respect East Money API rate limit: interval ≥ 3 seconds between sector fetches.

#### Scenario: 成功获取板块成分股

- **WHEN** `SectorConstituentFetcher.fetch(["机器人执行器"])` is called
- **THEN** SHALL return list of `SectorStock` with ≥ 20 items, each containing price, change_pct, high, low, open fields

#### Scenario: 板块不存在

- **WHEN** sector name "不存在的板块" is passed
- **THEN** SHALL log warning and return `[]` for that sector, not crash

#### Scenario: 跨板块去重

- **WHEN** stock "688017" appears in both "机器人执行器" and "人形机器人" sectors
- **THEN** result SHALL contain stock "688017" exactly once

---

### SE-S03: 技术指标趋势打分 (TrendScorer)

SHALL fetch 60 trading days of daily OHLCV data via `akshare.stock_zh_a_hist_tx(symbol)` (Tencent source).

SHALL compute the following indicators per stock:

| 指标 | 计算方式 | 满分 |
|------|----------|:----:|
| MA 多头排列 | MA5 > MA10 > MA20 全部满足=30, 部分满足=15, 空头=0 | 30 |
| MACD 方向 | DIF > DEA 且 MACD 柱 >0 → 25; DIF > DEA 但柱缩 → 15; DIF < DEA → 0 | 25 |
| RSI 强度 | 60 ≤ RSI(14) ≤ 80 → 20; 50 ≤ RSI < 60 → 12; 40 ≤ RSI < 50 → 5; RSI > 80 or RSI < 40 → 0 | 20 |
| 连涨天数 | 连续上涨 ≥ 3 天 → 15; 2天 → 10; 1天→5; 下跌→0 | 15 |
| 量比放大 | 今日量 / 5日均量 ≥ 1.5 → 10; ≥ 1.0 → 5; < 1.0 → 0 | 10 |

SHALL return a `TrendScore` dataclass with:
- `code: str`
- `total_score: int` — 0–100
- `ma_score: int`
- `macd_score: int`
- `rsi_score: int`
- `consecutive_up: int`
- `volume_ratio_score: int`
- `ma5: float`, `ma10: float`, `ma20: float`
- `rsi14: float`
- `consecutive_up_days: int`

SHALL handle insufficient data (< 20 trading days): return `total_score = 0` with reason "insufficient_data".

SHALL cache scores per stock per trading day (memory dict) to avoid repeated API calls within the same session.

SHALL use error isolation: one stock's data fetch failure SHALL NOT block others.

#### Scenario: 强势股高分

- **WHEN** stock has MA5 > MA10 > MA20, MACD 金叉向上, RSI=65, 连涨3天, 量比=1.8
- **THEN** `TrendScorer.score(stock)` SHALL return total_score ≥ 80

#### Scenario: 弱势股低分

- **WHEN** stock has MA5 < MA20 (空头), MACD 死叉, RSI=35, 今日下跌
- **THEN** `TrendScorer.score(stock)` SHALL return total_score ≤ 20

#### Scenario: 数据不足

- **WHEN** stock has < 20 trading days of history (new IPO)
- **THEN** SHALL return total_score = 0 with reason "insufficient_data"

---

### SE-S04: 趋势精选排序 (TrendRanker)

SHALL receive list of `SectorStock` with computed `TrendScore`.

SHALL apply Tier classification per stock:
- **Tier 1 (持仓)**: stock code in user positions → priority display
- **Tier 2 (自选)**: stock code in user watchlist → second priority
- **Tier 3 (趋势)**: remaining stocks, sorted by trend score

SHALL rule-filter: stocks with `total_score < 50` SHALL be excluded from Tier 3 candidate pool.

SHALL select top 10 from Tier 3 (by score) as DeepSeek input candidates.

SHALL construct DeepSeek prompt with:
- System: "你是一个量化分析师。根据以下股票的技术指标数据，选出未来3天最可能继续上涨的5只股票。按置信度排序。"
- User: JSON array of candidate stocks with `{code, name, price, change_pct, ma5, ma10, ma20, rsi14, consecutive_up_days, volume_ratio, total_score}`
- Temperature: 0.1
- Expected output: `{"top5": [{"code": "...", "name": "...", "confidence": 0.XX, "reason": "一句话"}]}`

SHALL merge final result:
```
Tier 1 (持仓命中) + Tier 2 (自选命中) + DeepSeek Top5 (Tier 3)
```
Each stock SHALL appear at most once (Tier 1 > Tier 2 > Tier 3).

SHALL cap total result at 8 stocks: fill Tier 1 first, then Tier 2, then Tier 3 Top N to reach 8.

SHALL fall back to rule-ranking (by `total_score` descending) when DeepSeek API is unavailable.

SHALL NOT call DeepSeek if Tier 1 + Tier 2 already reach 8 stocks (skip Tier 3 entirely).

#### Scenario: 持仓和自选命中

- **WHEN** 2 positions match, 1 watchlist matches, and Tier 3 has 30 candidates
- **THEN** result SHALL be: [pos1, pos2, wl1, DeepSeek top5 from Tier3], total ≤ 8

#### Scenario: 无持仓/自选命中

- **WHEN** 0 positions match, 0 watchlist matches, Tier 3 has 30 candidates with score ≥ 50
- **THEN** SHALL return DeepSeek Top 5 from Tier 3

#### Scenario: 持仓/自选已满 8 只

- **WHEN** Tier 1 + Tier 2 ≥ 8 stocks
- **THEN** SHALL skip DeepSeek call entirely, return first 8 from Tier1+Tier2

#### Scenario: DeepSeek API 不可用

- **WHEN** DeepSeek API returns error or times out
- **THEN** SHALL fall back to Tier 3 rule-ranking by `total_score` descending, return top N to fill to 8

---

## Acceptance Criteria

- [ ] `SectorMatcher.match()` returns correct sectors for sample news headlines
- [ ] Keyword mapping loaded from `config.yaml` and validated against live sector list
- [ ] `SectorConstituentFetcher.fetch()` returns `SectorStock` list with real-time quote fields populated
- [ ] Cross-sector stock dedup works correctly
- [ ] `TrendScorer.score()` returns 0–100 with all indicator sub-scores populated
- [ ] Scoring matches expected values for a known stock (e.g., verify with manual calculation)
- [ ] Insufficient data (< 20 days) returns score 0 with reason
- [ ] Score cache returns same result on second call within session
- [ ] `TrendRanker.rank()` returns correct Tier grouping (持仓 > 自选 > 趋势)
- [ ] DeepSeek prompt receives correct JSON format and produces valid `top5` output
- [ ] Fallback to rule-ranking works when DeepSeek unavailable
- [ ] Result capped at 8 stocks max
