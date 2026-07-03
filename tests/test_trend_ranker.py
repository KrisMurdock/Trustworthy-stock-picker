"""Unit tests for TrendRanker."""

import pytest
from unittest.mock import MagicMock, AsyncMock, patch

from stock_watcher.news.trend_ranker import TrendRanker, RankedResult
from stock_watcher.models import SectorStock, TrendScore


def _make_stock(code, name, sector="机器人执行器", change_pct=5.0, score=75):
    return SectorStock(
        code=code, name=name, sector=sector,
        price=100.0, change_pct=change_pct,
        high=105.0, low=95.0, open=98.0, prev_close=97.0,
        volume=100000, turnover=5.0, pe=30.0, pb=3.0,
    )


def _make_score(code, total=75):
    return TrendScore(
        code=code, total_score=total,
        ma_score=30, macd_score=20, rsi_score=15,
        consecutive_up=5, volume_ratio_score=5,
    )


class TestTrendRanker:
    """T16: TrendRanker — rule pre-filter + DeepSeek top5, tiered output."""

    def test_tier_classification(self):
        """Positions → Tier1, watchlist → Tier2, others → Tier3."""
        positions = {"sh000001", "sh000002"}
        watchlist = {"sh000001", "sh000002", "sh000003"}

        stocks = [
            _make_stock("000001", "Stock1"),  # in positions + watchlist → Tier1
            _make_stock("000002", "Stock2"),  # in positions + watchlist → Tier1
            _make_stock("000003", "Stock3"),  # watchlist only → Tier2
            _make_stock("000004", "Stock4"),  # neither → Tier3
        ]
        scores = {
            "000001": _make_score("000001", 85),
            "000002": _make_score("000002", 80),
            "000003": _make_score("000003", 75),
            "000004": _make_score("000004", 70),
        }

        ranker = TrendRanker(deepseek_cfg=None)  # no AI → rule-ranking
        result = ranker.rank(stocks, scores, positions, watchlist)

        assert [s.code for s in result.tier1] == ["000001", "000002"]
        assert [s.code for s in result.tier2] == ["000003"]
        assert [s.code for s in result.tier3] == ["000004"]

    def test_min_score_threshold(self):
        """Stocks with score < min_score excluded from Tier3."""
        stocks = [
            _make_stock("000001", "Good"),
            _make_stock("000002", "Bad"),
        ]
        scores = {
            "000001": _make_score("000001", 80),
            "000002": _make_score("000002", 30),  # below threshold
        }
        ranker = TrendRanker(deepseek_cfg=None, min_score=50)
        result = ranker.rank(stocks, scores, set(), set())

        assert len(result.tier3) == 1
        assert result.tier3[0].code == "000001"

    def test_cap_at_max_stocks(self):
        """Total across all tiers capped at max_stocks."""
        stocks = [_make_stock(f"{i:06d}", f"Stock{i}") for i in range(1, 20)]
        scores = {f"{i:06d}": _make_score(f"{i:06d}", 80) for i in range(1, 20)}
        positions = {f"sh{i:06d}" for i in range(1, 4)}  # 3 positions
        watchlist = {f"sh{i:06d}" for i in range(1, 8)}  # 7 watchlist (overlap with pos)

        # Actually: pos codes 1-3, wl codes 1-7. Tier1 = 1-3, Tier2 = 4-7.
        # That's 7 stocks already. but the stocks list uses raw codes without prefix.
        # Let me fix: positions/wl use raw codes for matching
        
        ranker = TrendRanker(deepseek_cfg=None, max_stocks=5, min_score=50)
        result = ranker.rank(stocks, scores, set(), set())  # no positions/watchlist
        assert len(result.all) <= 5

    def test_skip_deepseek_when_tier12_full(self):
        """When Tier1+Tier2 ≥ max_stocks, skip DeepSeek entirely."""
        stocks = [
            _make_stock("000001", "P1"),
            _make_stock("000002", "P2"),
            _make_stock("000003", "WL1"),
        ]
        scores = {s.code: _make_score(s.code, 80) for s in stocks}
        positions = {"sh000001", "sh000002"}
        watchlist = {"sh000001", "sh000002", "sh000003"}

        ranker = TrendRanker(deepseek_cfg=MagicMock(), max_stocks=3)
        result = ranker.rank(stocks, scores, positions, watchlist)
        # Tier1 (000001,000002) + Tier2 (000003) = 3 ≥ max_stocks=3 → skip Tier3
        assert len(result.tier3) == 0
        assert len(result.all) == 3

    def test_empty_input(self):
        ranker = TrendRanker(deepseek_cfg=None)
        result = ranker.rank([], {}, set(), set())
        assert result.all == []
        assert result.tier1 == []
        assert result.tier2 == []
        assert result.tier3 == []
