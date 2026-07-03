# Tasks: 新闻聚合与飞书推送（含板块增强）

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `src/stock_watcher/models.py` | Add NewsItem, SectorStock, TrendScore dataclasses |
| Modify | `src/stock_watcher/config.py` | Add NewsConfig, SectorKeywordsConfig, TrendScoringConfig |
| Modify | `config.yaml` | Add news: section with sector_keywords, trend_scoring |
| Modify | `config.yaml.example` | Mirror config.yaml news section |
| Modify | `pyproject.toml` | Add akshare, yfinance dependencies |
| Create | `src/stock_watcher/news/__init__.py` | Public API re-exports |
| Create | `src/stock_watcher/news/base.py` | BaseNewsFetcher ABC, shared constants |
| Create | `src/stock_watcher/news/eastmoney_stock.py` | EastMoneyStockNewsFetcher |
| Create | `src/stock_watcher/news/eastmoney_flash.py` | EastMoneyFlashNewsFetcher |
| Create | `src/stock_watcher/news/sina_flash.py` | SinaFlashNewsFetcher |
| Create | `src/stock_watcher/news/cls_telegraph.py` | CLSTelegraphFetcher |
| Create | `src/stock_watcher/news/yahoo_news.py` | YahooNewsFetcher |
| Create | `src/stock_watcher/news/aggregator.py` | NewsAggregator — fetch, dedup, filter |
| Create | `src/stock_watcher/news/classifier.py` | NewsClassifier — keyword + DeepSeek |
| Create | `src/stock_watcher/news/sector_matcher.py` | SectorMatcher — keyword→sector |
| Create | `src/stock_watcher/news/trend_scorer.py` | TrendScorer — 100-point technical scoring |
| Create | `src/stock_watcher/news/trend_ranker.py` | TrendRanker — rule pre-filter + DeepSeek top5 |
| Create | `src/stock_watcher/news/card_builder.py` | NewsCardBuilder — sector + tiered quote card |
| Create | `src/stock_watcher/scheduled_push.py` | ScheduledPush — timed push dispatcher |
| Modify | `src/stock_watcher/app.py` | Integrate news pipeline + scheduled push |
| Create | `tests/test_news_models.py` | NewsItem/SectorStock/TrendScore unit tests |
| Create | `tests/test_news_em_stock.py` | EastMoneyStockNewsFetcher unit tests |
| Create | `tests/test_news_em_flash.py` | EastMoneyFlashNewsFetcher unit tests |
| Create | `tests/test_news_sina_flash.py` | SinaFlashNewsFetcher unit tests |
| Create | `tests/test_news_cls.py` | CLSTelegraphFetcher unit tests |
| Create | `tests/test_news_yahoo.py` | YahooNewsFetcher unit tests |
| Create | `tests/test_news_aggregator.py` | NewsAggregator integration tests |
| Create | `tests/test_news_classifier.py` | NewsClassifier unit tests |
| Create | `tests/test_sector_matcher.py` | SectorMatcher unit tests |
| Create | `tests/test_trend_scorer.py` | TrendScorer unit tests |
| Create | `tests/test_trend_ranker.py` | TrendRanker unit tests |
| Create | `tests/test_news_card_builder.py` | NewsCardBuilder unit tests |
| Create | `tests/test_scheduled_push.py` | ScheduledPush unit tests |
| Create | `tests/test_news_e2e.py` | End-to-end pipeline test |

## Interfaces

### Batch 1 → Batch 2
- **Produces**: `NewsItem` dataclass, `NewsConfig` — consumed by all fetchers

### Batch 1 → Batch 4
- **Produces**: `SectorStock` dataclass, `TrendScore` dataclass, `sector_keywords` config — consumed by SectorMatcher, TrendScorer, TrendRanker

### Batch 3 → Batch 4
- **Produces**: `NewsAggregator.aggregate()` → `list[NewsItem]` (classified P0) — consumed by SectorMatcher
- **Produces**: Position list, watchlist list — consumed by TrendRanker

### Batch 4 → Batch 5
- **Produces**: `SectorMatcher.match()` → `list[str]` sector names — consumed by CardBuilder
- **Produces**: `TrendRanker.rank()` → `TieredStockList` — consumed by CardBuilder

### Batch 5 → Batch 6
- **Produces**: `NewsCardBuilder.build_card()` → `dict` Feishu card JSON — consumed by app.py push pipeline

---

## Dependency Graph

```
T01 (Models + Config)
  │
  ├── T02 (Akshare install + smoke test)
  │     │
  │     ├── T03 (EM Stock News Fetcher)
  │     ├── T04 (EM Flash News Fetcher)
  │     ├── T05 (Sina Flash Fetcher)
  │     ├── T06 (CLS Telegraph Fetcher)
  │     └── T07 (Yahoo News Fetcher)
  │           │
  ├───────────┴── T08 (Aggregator + Dedup)
  │                   │
  ├── T09 (NewsClassifier)
  │     │
  ├────── T13 (SectorStock + TrendScore models + sector config)  [NEW]
  │         │
  │         ├── T14 (SectorMatcher)
  │         ├── T15 (TrendScorer)
  │         └── T16 (TrendRanker)
  │               │
  ├── T10 (NewsCardBuilder — sector + tiered quotes)  [MODIFIED]
  │     │
  ├── T11 (ScheduledPush)
  │     │
  └── T12 (Integration: app.py + config.yaml + e2e)  [MODIFIED]
```

---

## Batch 1: Foundation

### T01 — 扩展 NewsItem + SectorStock + TrendScore 模型 + NewsConfig

**Depends on**: none

1. Write failing test: `tests/test_news_models.py`
```python
def test_news_item_creation():
    item = NewsItem(id="hash", source="eastmoney", title="测试", summary="摘要",
                    url="http://x", published_at="2025-07-01T00:00:00Z",
                    related_codes=["sh600519"], source_priority=2)
    assert item.id == "hash"
    assert item.related_codes == ["sh600519"]

def test_sector_stock_creation():
    s = SectorStock(code="688017", name="绿的谐波", sector="机器人执行器",
                    price=494.66, change_pct=19.76, high=495.66, low=416.20,
                    open=426.00, prev_close=413.05, volume=259204,
                    turnover=14.14, pe=694.72, pb=25.41)
    assert s.price == 494.66

def test_trend_score_range():
    ts = TrendScore(code="688017", total_score=85, ma_score=30, macd_score=25,
                    rsi_score=20, consecutive_up=3, volume_ratio_score=10)
    assert 0 <= ts.total_score <= 100
```
2. Run test → fail (no dataclasses defined)
3. Implement in `models.py`:
```python
@dataclass
class SectorStock:
    code: str; name: str; sector: str; price: float; change_pct: float
    high: float; low: float; open: float; prev_close: float
    volume: int; turnover: float; pe: float; pb: float

@dataclass
class TrendScore:
    code: str; total_score: int; ma_score: int; macd_score: int
    rsi_score: int; consecutive_up: int; volume_ratio_score: int
    ma5: float = 0.0; ma10: float = 0.0; ma20: float = 0.0
    rsi14: float = 0.0; consecutive_up_days: int = 0; reason: str = ""
```
Add `NewsItem` dataclass. Add `NewsConfig`, `SectorKeywordsConfig`, `TrendScoringConfig` to `config.py`.
4. Run test → pass
5. Commit: `feat: add NewsItem, SectorStock, TrendScore models and config`

**Files**: `models.py`, `config.py`

---

### T02 — 安装 AKShare + yfinance，冒烟测试

**Depends on**: T01

1. Run: `pip install akshare yfinance`
2. Write smoke-test in `tests/test_news_models.py`:
```python
def test_akshare_smoke():
    import akshare
    df = akshare.stock_board_concept_name_em()
    assert len(df) > 100  # at least 100 concept sectors

def test_akshare_constituent_smoke():
    import akshare
    df = akshare.stock_board_concept_cons_em(symbol="机器人执行器")
    assert len(df) > 10
    assert "最新价" in df.columns

def test_akshare_tx_hist_smoke():
    import akshare
    df = akshare.stock_zh_a_hist_tx(symbol="sh688017", start_date="20250601", end_date="20250703")
    assert len(df) >= 20
```
3. Run tests → verify all pass
4. Commit: `chore: add akshare, yfinance dependencies with smoke tests`

**Files**: `pyproject.toml`, `tests/test_news_models.py`

---

## Batch 2: News Fetchers (parallel)

**Depends on**: Batch 1

### T03 — EastMoneyStockNewsFetcher

1. Write failing test in `tests/test_news_em_stock.py`: mock `akshare.stock_news_em`, verify `fetch()` returns `list[NewsItem]`
2. Run test → fail
3. Implement `news/base.py` (BaseNewsFetcher ABC) + `news/eastmoney_stock.py`
4. Run test → pass
5. Commit: `feat: EastMoneyStockNewsFetcher`

**Files**: `news/__init__.py`, `news/base.py`, `news/eastmoney_stock.py`, `tests/test_news_em_stock.py`

### T04 — EastMoneyFlashNewsFetcher

1. Write failing test: mock akshare global flash, verify code extraction from headline
2. Run → fail
3. Implement `news/eastmoney_flash.py`
4. Run → pass
5. Commit: `feat: EastMoneyFlashNewsFetcher`

**Files**: `news/eastmoney_flash.py`, `tests/test_news_em_flash.py`

### T05 — SinaFlashNewsFetcher

1. Write failing test: mock `akshare.stock_info_global_sina`, verify code extraction
2. Run → fail
3. Implement `news/sina_flash.py`
4. Run → pass
5. Commit: `feat: SinaFlashNewsFetcher`

**Files**: `news/sina_flash.py`, `tests/test_news_sina_flash.py`

### T06 — CLSTelegraphFetcher

1. Write failing test: mock `akshare.stock_telegraph_cls`, verify title-as-summary
2. Run → fail
3. Implement `news/cls_telegraph.py`
4. Run → pass
5. Commit: `feat: CLSTelegraphFetcher`

**Files**: `news/cls_telegraph.py`, `tests/test_news_cls.py`

### T07 — YahooNewsFetcher

1. Write failing test: mock `yfinance.Ticker().news`, verify HK code conversion hk00700→0700.HK
2. Run → fail
3. Implement `news/yahoo_news.py`
4. Run → pass
5. Commit: `feat: YahooNewsFetcher`

**Files**: `news/yahoo_news.py`, `tests/test_news_yahoo.py`

---

## Batch 3: Aggregation + Classification

**Depends on**: Batch 2

### T08 — NewsAggregator + 去重

1. Write failing integration test: mock 3 fetchers, verify dedup and watchlist filter
2. Run → fail
3. Implement `news/aggregator.py` with concurrent fetch, URL+Levenshtein dedup, JSONL persistence
4. Run → pass
5. Commit: `feat: NewsAggregator with cross-source dedup`

**Files**: `news/aggregator.py`, `tests/test_news_aggregator.py`

### T09 — NewsClassifier

1. Write failing test: mock DeepSeek HTTP response, verify 3-level classification JSON parsing
2. Test keyword pre-filter: "停牌" in title → P0 without API call
3. Run → fail
4. Implement `news/classifier.py` with keyword short-circuit, batch classify, rate limiter, safe default
5. Run → pass
6. Commit: `feat: NewsClassifier with keyword pre-filter and DeepSeek batch classify`

**Files**: `news/classifier.py`, `tests/test_news_classifier.py`

---

## Batch 4: Sector Enrichment [NEW]

**Depends on**: Batch 3 (needs classified P0 NewsItems), Batch 1 (SectorStock/TrendScore models)

### T13 — sector_keywords 配置加载

**Consumes**: T01 (config infrastructure)

1. Write failing test: load `news.sector_keywords` from dict, verify mapping structure
```python
def test_sector_keywords_loading():
    cfg = load_config({"news": {"sector_keywords": {"机器人": ["机器人执行器", "人形机器人"]}}})
    assert cfg.news.sector_keywords["机器人"] == ["机器人执行器", "人形机器人"]

def test_keyword_case_insensitive():
    matcher = SectorMatcher(keywords={"机器人": ["机器人执行器"]})
    result = matcher.match("机器人概念股大涨")  # partial match
    assert "机器人执行器" in result
```
2. Run → fail
3. Add `sector_keywords` mapping to `config.yaml` with 8 initial keyword groups (机器人/新能源/AI/半导体/医药/消费/军工/金融). Add `SectorKeywordsConfig` to `config.py`.
4. Run → pass
5. Commit: `feat: sector_keywords config with 8 keyword groups`

**Files**: `config.py`, `config.yaml`, `config.yaml.example`

---

### T14 — SectorMatcher

**Consumes**: T13 (sector_keywords config), T02 (akshare smoke)

1. Write failing test: create SectorMatcher with 2 keyword groups, verify matching
```python
def test_direct_match():
    matcher = SectorMatcher(keywords={"机器人": ["机器人执行器", "人形机器人"]})
    result = matcher.match(title="机器人执行器概念股午后异动拉升", summary="")
    assert "机器人执行器" in result

def test_partial_keyword_match():
    matcher = SectorMatcher(keywords={"机器人": ["机器人执行器"]})
    result = matcher.match(title="机器人产业爆发", summary="")
    assert "机器人执行器" in result  # keyword "机器人" matches sector name

def test_no_match():
    matcher = SectorMatcher(keywords={"机器人": ["机器人执行器"]})
    result = matcher.match(title="央行发布货币政策报告", summary="")
    assert result == []

def test_multi_keyword_to_same_sector_dedup():
    matcher = SectorMatcher(keywords={"机器人": ["机器人执行器"], "自动化": ["机器人执行器"]})
    result = matcher.match(title="机器人和自动化产业", summary="")
    assert result.count("机器人执行器") == 1

def test_empty_title_returns_empty():
    matcher = SectorMatcher(keywords={"机器人": ["机器人执行器"]})
    assert matcher.match(title="", summary="") == []
```
2. Run → fail
3. Implement `news/sector_matcher.py`:
   - `SectorMatcher.__init__(sector_keywords: dict[str, list[str]])`
   - `SectorMatcher.match(title: str, summary: str) → list[str]`
   - Case-insensitive matching via `str.lower()`
   - Partial match: `keyword in title.lower() or keyword in summary.lower()`
   - Dedup: use `set` before returning
   - Validate against live sector list loaded at init (cache `stock_board_concept_name_em()`)
4. Run → pass
5. Commit: `feat: SectorMatcher with keyword→sector fuzzy matching`

**Files**: `news/sector_matcher.py`, `tests/test_sector_matcher.py`

---

### T15 — TrendScorer

**Consumes**: T02 (akshare.stock_zh_a_hist_tx smoke), T01 (TrendScore model)

1. Write failing test:
```python
def test_trend_scorer_bullish():
    # Mock historical data: close rising, MA5>MA10>MA20
    mock_data = _generate_rising_data(60)  # helper creates 60-day ascending prices
    scorer = TrendScorer()
    score = scorer.score("688017", mock_data)
    assert score.total_score >= 70  # strong bullish
    assert score.ma_score == 30
    assert score.consecutive_up_days >= 2

def test_trend_scorer_bearish():
    mock_data = _generate_falling_data(60)
    scorer = TrendScorer()
    score = scorer.score("688017", mock_data)
    assert score.total_score <= 25  # weak

def test_insufficient_data():
    mock_data = _generate_rising_data(10)  # only 10 days
    scorer = TrendScorer()
    score = scorer.score("688017", mock_data)
    assert score.total_score == 0
    assert score.reason == "insufficient_data"

def test_score_cache():
    scorer = TrendScorer()
    data = _generate_rising_data(60)
    score1 = scorer.score("688017", data)
    score2 = scorer.score("688017", data)  # second call uses cache
    assert score1.total_score == score2.total_score
```
2. Run → fail
3. Implement `news/trend_scorer.py`:
   - `TrendScorer.__init__(weights: dict)` with defaults MA=30/MACD=25/RSI=20/CONSEC=15/VOL=10
   - `TrendScorer.score(code: str, df: pd.DataFrame) → TrendScore`
   - Compute MA(5,10,20) from close prices; MA5>MA10>MA20 → 30, MA5>MA10 only → 15, else → 0
   - Compute MACD (12,26,9): DIF > DEA and histogram > prev → 25; DIF > DEA but hist shrinking → 15; DIF < DEA → 0
   - Compute RSI(14): 60≤RSI≤80 → 20; 50≤RSI<60 → 12; 40≤RSI<50 → 5; else → 0
   - Count consecutive up days (close > prev_close): ≥3 → 15; 2 → 10; 1 → 5; 0 → 0
   - Volume ratio: today_amount / 5d_avg_amount ≥ 1.5 → 10; ≥ 1.0 → 5; < 1.0 → 0
   - Cache: `{code: (date, TrendScore)}` in memory dict
   - Insufficient data (< 20 rows): return TrendScore with total_score=0, reason="insufficient_data"
   - `TrendScorer.score_batch(stocks: list[SectorStock], fetcher: callable) → dict[str, TrendScore]`
4. Run → pass
5. Commit: `feat: TrendScorer with 100-point technical indicator scoring`

**Produces**: `TrendScore` objects consumed by T16 (TrendRanker)

**Files**: `news/trend_scorer.py`, `tests/test_trend_scorer.py`

---

### T16 — TrendRanker

**Consumes**: T15 (TrendScore), T08 (watchlist/positions access)

1. Write failing test:
```python
def test_tier_classification():
    positions = {"sh688017", "sh300403"}  # 持仓
    watchlist = {"sh688017", "sh300403", "sh300503"}  # 自选
    stocks = [
        SectorStock(code="688017", name="绿的谐波", sector="机器人执行器", price=494.66, change_pct=19.76, ...),
        SectorStock(code="300503", name="昊志机电", sector="机器人执行器", price=109.89, change_pct=15.82, ...),
        SectorStock(code="000001", name="平安银行", sector="银行", price=12.50, change_pct=1.2, ...),
    ]
    scores = {
        "688017": TrendScore(code="688017", total_score=85, ...),
        "300503": TrendScore(code="300503", total_score=78, ...),
        "000001": TrendScore(code="000001", total_score=45, ...),
    }
    ranker = TrendRanker()
    result = ranker.rank(stocks, scores, positions, watchlist)
    # 绿的谐波: position+watchlist → Tier1
    # 昊志机电: watchlist only → Tier2
    # 平安银行: score=45 < 50 → excluded
    assert result.tier1[0].code == "688017"
    assert result.tier2[0].code == "300503"
    assert len(result.tier3) == 0

def test_tier3_deepseek_integration():
    # Mock DeepSeek response with top5 JSON
    ...

def test_fallback_to_rule_ranking():
    # Simulate DeepSeek failure → should return rule-ranked top5
    ...

def test_cap_at_8_stocks():
    # 3 tier1 + 4 tier2 + 10 tier3 candidates → result should have max 8
    ...

def test_skip_deepseek_when_tier12_full():
    # tier1+tier2 already ≥ 8 → no DeepSeek call
    ...
```
2. Run → fail
3. Implement `news/trend_ranker.py`:
   - `TrendRanker.__init__(deepseek_client, top_n_candidates=10, top_n_final=5, max_stocks=8, min_score=50)`
   - `TrendRanker.rank(stocks, scores, positions, watchlist) → RankedResult`
   - `RankedResult` dataclass: `tier1: list[SectorStock]`, `tier2: list[SectorStock]`, `tier3: list[SectorStock]`, `all: list[SectorStock]`
   - Tier classification: check code in positions→T1, else code in watchlist→T2, else→T3 candidate
   - T3 rule filter: total_score >= min_score
   - T3 candidates sorted by score desc, take top_n_candidates
   - DeepSeek prompt: send structured JSON of candidates → parse `{"top5": [...]}`
   - Merge: T1 + T2 + DeepSeek_top5, cap at max_stocks
   - Fallback: if DeepSeek fails, use T3 rule-ranked by score desc
   - Skip DeepSeek if len(T1+T2) >= max_stocks
4. Run → pass
5. Commit: `feat: TrendRanker with rule pre-filter + DeepSeek top5 selection`

**Produces**: `RankedResult` consumed by T10 (CardBuilder)

**Files**: `news/trend_ranker.py`, `tests/test_trend_ranker.py`

---

## Batch 5: Push + Scheduling

**Depends on**: Batch 3 (classifier), Batch 4 (sector enrichment)

### T10 — NewsCardBuilder (sector + tiered quotes) [MODIFIED]

**Consumes**: T16 (RankedResult), T14 (sector names)

1. Write failing test:
```python
def test_enriched_card_structure():
    news = NewsItem(title="机器人异动", ...)
    tiered = RankedResult(
        tier1=[SectorStock(code="688017", name="绿的谐波", price=494.66, change_pct=19.76, high=495.66, low=416.20, open=426.00, ...)],
        tier2=[],
        tier3=[SectorStock(code="300403", name="汉宇集团", price=11.61, change_pct=17.04, high=11.83, low=9.96, open=10.07, ...)],
    )
    card = build_news_card(news, sector="机器人执行器", tiered=tiered)
    # Verify JSON structure
    assert card["header"]["title"]["content"] == "📰 机器人执行器 异动"
    assert any("📌 你的持仓" in str(e) for e in card["elements"])
    assert any("绿的谐波" in str(e) for e in card["elements"])
    assert any("494.66" in str(e) for e in card["elements"])

def test_basic_card_fallback():
    # No sector matched → basic card
    card = build_news_card(news, sector=None, tiered=None)
    assert "📰 重大新闻" in card["header"]["title"]["content"]

def test_stock_cap_8():
    # Generate 10 tier3 stocks → card should show max 8
    ...

def test_card_schema_valid():
    import jsonschema
    card = build_news_card(...)
    jsonschema.validate(card, feishu_card_schema)  # validate against Feishu schema
```
2. Run → fail
3. Modify `news/card_builder.py`:
   - `build_news_card(news: NewsItem, sector: str | None, tiered: RankedResult | None) → dict`
   - When sector and tiered present: enriched card with header + sector overview row + tiered stock rows
   - Stock row format: `{name} {code} {change_pct:+.2f}% ¥{price:.2f} H:{high:.2f} L:{low:.2f} O:{open:.2f}`
   - When no sector: fall back to basic news card (original NP-S01 format)
   - Enforce max 8 stocks across all tiers
   - Rate cap 10 pushes/min + overflow queue
   - Per-sector 30-min push dedup
4. Run → pass
5. Commit: `feat: NewsCardBuilder with sector overview and tiered stock quote display`

**Files**: `news/card_builder.py`, `tests/test_news_card_builder.py`

---

### T11 — ScheduledPush

**Depends on**: Batch 3 (classifier)

1. Write failing test: mock clock, verify each push type fires at correct time
2. Run → fail
3. Implement `scheduled_push.py` with 5 push types (09:25/15:05/16:15/09:00/20:00), configurable times, missed-push skip
4. Run → pass
5. Commit: `feat: ScheduledPush with 5 configurable push types`

**Files**: `scheduled_push.py`, `tests/test_scheduled_push.py`

---

## Batch 6: Integration

**Depends on**: Batch 5

### T12 — 集成到主程序 + 端到端测试 [MODIFIED]

**Consumes**: All Batch 1-5 outputs

1. Write e2e smoke test in `tests/test_news_e2e.py`:
```python
@pytest.mark.asyncio
async def test_news_pipeline_integration():
    # Start app with test config
    # Inject mock news → verify card JSON produced
    # Verify sector matcher + trend ranker integration
    ...
```
2. Implement integration in `app.py`:
   - Wire `NewsAggregator` + `NewsClassifier` + `SectorMatcher` + `TrendScorer` + `TrendRanker` + `NewsCardBuilder`
   - Start `ScheduledPush` in async loop
   - Config hot-reload for `news:` section
3. Update `config.yaml` with full `news:` defaults (sector_keywords, trend_scoring)
4. Update `config.yaml.example` with full `news:` defaults
5. Run e2e test → verify pipeline flows
6. Commit: `feat: integrate news pipeline with sector enrichment into app`

**Files**: `app.py`, `config.yaml`, `config.yaml.example`, `tests/test_news_e2e.py`

---

## Estimated Effort

| Batch | Tasks | Est. Hours |
|-------|-------|-----------|
| B1: Foundation | T01–T02 | 1h |
| B2: Fetchers | T03–T07 | 3h |
| B3: Aggregation+Classify | T08–T09 | 3h |
| B4: Sector Enrichment | T13–T16 | 4h |
| B5: Push+Scheduling | T10–T11 | 2.5h |
| B6: Integration | T12 | 1.5h |
| **Total** | **16 tasks** | **~15h** |
