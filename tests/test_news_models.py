"""Tests for NewsItem, SectorStock, TrendScore models and NewsConfig."""

import pytest
from stock_watcher.models import NewsItem, SectorStock, TrendScore
from stock_watcher.config import NewsConfig, SectorKeywordsConfig, TrendScoringConfig


class TestNewsItem:
    def test_creation_with_all_fields(self):
        item = NewsItem(
            id="abc123",
            source="eastmoney_stock",
            title="贵州茅台发布年报",
            summary="营收增长15%",
            url="http://example.com",
            published_at="2025-07-01T10:30:00Z",
            related_codes=["sh600519"],
            source_priority=1,
        )
        assert item.id == "abc123"
        assert item.source == "eastmoney_stock"
        assert item.title == "贵州茅台发布年报"
        assert item.summary == "营收增长15%"
        assert item.url == "http://example.com"
        assert item.published_at == "2025-07-01T10:30:00Z"
        assert item.related_codes == ["sh600519"]
        assert item.source_priority == 1

    def test_equality_by_id(self):
        a = NewsItem(id="same", title="A")
        b = NewsItem(id="same", title="B")
        assert a == b

    def test_empty_defaults(self):
        item = NewsItem(id="x")
        assert item.source == ""
        assert item.title == ""
        assert item.related_codes == []
        assert item.source_priority == 2


class TestSectorStock:
    def test_creation_with_all_fields(self):
        s = SectorStock(
            code="688017",
            name="绿的谐波",
            sector="机器人执行器",
            price=494.66,
            change_pct=19.76,
            high=495.66,
            low=416.20,
            open=426.00,
            prev_close=413.05,
            volume=259204,
            turnover=14.14,
            pe=694.72,
            pb=25.41,
        )
        assert s.code == "688017"
        assert s.name == "绿的谐波"
        assert s.sector == "机器人执行器"
        assert s.price == 494.66
        assert s.change_pct == 19.76
        assert s.high == 495.66
        assert s.low == 416.20
        assert s.open == 426.00
        assert s.prev_close == 413.05
        assert s.volume == 259204
        assert s.turnover == 14.14
        assert s.pe == 694.72
        assert s.pb == 25.41


class TestTrendScore:
    def test_creation_with_all_fields(self):
        ts = TrendScore(
            code="688017",
            total_score=85,
            ma_score=30,
            macd_score=25,
            rsi_score=20,
            consecutive_up=3,
            volume_ratio_score=10,
            ma5=480.0,
            ma10=460.0,
            ma20=430.0,
            rsi14=65.0,
            consecutive_up_days=3,
            reason="",
        )
        assert ts.code == "688017"
        assert ts.total_score == 85
        assert ts.ma_score == 30
        assert ts.macd_score == 25
        assert ts.rsi_score == 20
        assert ts.consecutive_up == 3
        assert ts.volume_ratio_score == 10
        assert ts.ma5 == 480.0
        assert ts.ma10 == 460.0
        assert ts.ma20 == 430.0
        assert ts.rsi14 == 65.0
        assert ts.consecutive_up_days == 3
        assert ts.reason == ""

    def test_score_bounds(self):
        """total_score should be between 0 and 100."""
        for score in [0, 50, 100]:
            ts = TrendScore(code="x", total_score=score, ma_score=0, macd_score=0,
                            rsi_score=0, consecutive_up=0, volume_ratio_score=0)
            assert 0 <= ts.total_score <= 100

    def test_insufficient_data_reason(self):
        ts = TrendScore(
            code="new_ipo", total_score=0, ma_score=0, macd_score=0,
            rsi_score=0, consecutive_up=0, volume_ratio_score=0,
            reason="insufficient_data",
        )
        assert ts.total_score == 0
        assert ts.reason == "insufficient_data"


class TestSectorKeywordsConfig:
    def test_from_dict(self):
        d = {
            "sector_keywords": {
                "机器人": ["机器人执行器", "人形机器人"],
                "AI": ["人工智能", "ChatGPT"],
            }
        }
        cfg = SectorKeywordsConfig.from_dict(d.get("sector_keywords", {}))
        assert cfg.keywords["机器人"] == ["机器人执行器", "人形机器人"]
        assert cfg.keywords["AI"] == ["人工智能", "ChatGPT"]

    def test_default_empty(self):
        cfg = SectorKeywordsConfig()
        assert cfg.keywords == {}


class TestTrendScoringConfig:
    def test_from_dict(self):
        d = {
            "ma_weight": 30,
            "macd_weight": 25,
            "rsi_weight": 20,
            "consecutive_up_weight": 15,
            "volume_ratio_weight": 10,
            "lookback_days": 60,
            "min_score_threshold": 50,
            "top_n_candidates": 10,
            "top_n_final": 5,
            "max_stocks_per_card": 8,
        }
        cfg = TrendScoringConfig.from_dict(d)
        assert cfg.ma_weight == 30
        assert cfg.macd_weight == 25
        assert cfg.rsi_weight == 20
        assert cfg.consecutive_up_weight == 15
        assert cfg.volume_ratio_weight == 10
        assert cfg.lookback_days == 60
        assert cfg.min_score_threshold == 50
        assert cfg.top_n_candidates == 10
        assert cfg.top_n_final == 5
        assert cfg.max_stocks_per_card == 8

    def test_defaults(self):
        cfg = TrendScoringConfig()
        assert cfg.ma_weight == 30
        assert cfg.lookback_days == 60


class TestNewsConfigExtended:
    def test_sector_keywords_in_config(self):
        d = {
            "sector_keywords": {
                "机器人": ["机器人执行器", "人形机器人"],
            },
        }
        cfg = NewsConfig.from_dict(d)
        assert cfg.sector_keywords is not None
        assert cfg.sector_keywords.keywords["机器人"] == ["机器人执行器", "人形机器人"]

    def test_trend_scoring_in_config(self):
        d = {
            "trend_scoring": {
                "ma_weight": 25,
                "lookback_days": 30,
            },
        }
        cfg = NewsConfig.from_dict(d)
        assert cfg.trend_scoring is not None
        assert cfg.trend_scoring.ma_weight == 25
        assert cfg.trend_scoring.lookback_days == 30

    def test_news_config_source_list(self):
        cfg = NewsConfig()
        expected_sources = [
            "eastmoney_stock", "eastmoney_flash", "sina_flash",
            "cls_telegraph", "yahoo_news",
        ]
        for s in expected_sources:
            assert s in cfg.sources, f"Missing source: {s}"
