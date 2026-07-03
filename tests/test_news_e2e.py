"""End-to-end smoke test for news pipeline integration.

Verifies that all pipeline components can be wired together
without import errors or type mismatches.
"""

import pytest


class TestNewsPipelineIntegration:
    """T12: Verify news pipeline components integrate correctly."""

    def test_all_imports_resolve(self):
        """All news modules should be importable."""
        from stock_watcher.news import (
            BaseNewsFetcher,
            NewsAggregator,
            NewsClassifier,
            NewsLevel,
            ClassifyResult,
            build_news_card,
            build_enriched_card,
        )
        # New modules
        from stock_watcher.news.eastmoney_stock import EastMoneyStockNewsFetcher
        from stock_watcher.news.eastmoney_flash import EastMoneyFlashNewsFetcher
        from stock_watcher.news.sina_flash import SinaFlashNewsFetcher
        from stock_watcher.news.cls_telegraph import CLSTelegraphFetcher
        from stock_watcher.news.yahoo_news import YahooNewsFetcher
        from stock_watcher.news.sector_matcher import SectorMatcher
        from stock_watcher.news.trend_scorer import TrendScorer
        from stock_watcher.news.trend_ranker import TrendRanker, RankedResult
        from stock_watcher.scheduled_push import ScheduledPush

    def test_models_roundtrip(self):
        """NewsItem, SectorStock, TrendScore should be constructable."""
        from stock_watcher.models import NewsItem, SectorStock, TrendScore

        item = NewsItem(id="test", title="Hello")
        assert item.id == "test"

        stock = SectorStock(code="000001", name="Test", sector="Bank")
        assert stock.code == "000001"

        ts = TrendScore(code="000001", total_score=50)
        assert ts.total_score == 50

    def test_config_loads_news_section(self):
        """NewsConfig should parse from dict with sector/enrichment fields."""
        from stock_watcher.config import NewsConfig

        d = {
            "sources": {"eastmoney_stock": True},
            "sector_keywords": {"AI": ["人工智能"]},
            "trend_scoring": {"min_score_threshold": 60},
        }
        cfg = NewsConfig.from_dict(d)
        assert cfg.sources["eastmoney_stock"] is True
        assert cfg.sector_keywords.keywords["AI"] == ["人工智能"]
        assert cfg.trend_scoring.min_score_threshold == 60

    def test_aggregator_with_new_fetchers(self):
        """NewsAggregator accepts the 5 new fetcher types."""
        from stock_watcher.news.aggregator import NewsAggregator
        from stock_watcher.news.eastmoney_stock import EastMoneyStockNewsFetcher
        from stock_watcher.news.sina_flash import SinaFlashNewsFetcher

        fetchers = [
            EastMoneyStockNewsFetcher(enabled=False),
            SinaFlashNewsFetcher(enabled=False),
        ]
        agg = NewsAggregator(fetchers, seen_path="/tmp/test_seen.jsonl")
        assert agg is not None

    def test_enriched_card_pipeline_e2e(self):
        """Full enriched pipeline: matcher + scorer + ranker → card."""
        from stock_watcher.news.sector_matcher import SectorMatcher
        from stock_watcher.news.trend_scorer import TrendScorer
        from stock_watcher.news.trend_ranker import TrendRanker, RankedResult
        from stock_watcher.news.card_builder import build_enriched_card
        from stock_watcher.models import NewsItem, SectorStock, TrendScore

        # Simulate news → sector match
        matcher = SectorMatcher(
            sector_keywords={"机器人": ["机器人执行器"]},
            sector_list=["机器人执行器"],
        )
        news = NewsItem(id="e2e", title="机器人概念大涨")
        sectors = matcher.match(news)
        assert "机器人执行器" in sectors

        # Simulate sector → stocks + scores → ranking
        stocks = [
            SectorStock(code="000001", name="StockA", sector="机器人执行器",
                        price=10.0, change_pct=5.0, high=11.0, low=9.0, open=9.5,
                        prev_close=9.5, volume=1000, turnover=5.0, pe=20.0, pb=2.0),
            SectorStock(code="000002", name="StockB", sector="机器人执行器",
                        price=20.0, change_pct=3.0, high=21.0, low=19.0, open=19.5,
                        prev_close=19.5, volume=2000, turnover=4.0, pe=25.0, pb=3.0),
        ]
        scores = {
            "000001": TrendScore(code="000001", total_score=85),
            "000002": TrendScore(code="000002", total_score=60),
        }
        ranker = TrendRanker(deepseek_cfg=None, min_score=50)
        result = ranker.rank(stocks, scores, set(), set())
        assert len(result.tier3) == 2  # both above min_score, no positions/watchlist

        # Build card
        card = build_enriched_card(news=news, sector="机器人执行器", tiered=result)
        assert card["msg_type"] == "interactive"
        assert "机器人执行器" in card["card"]["header"]["title"]["content"]
