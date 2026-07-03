"""Unit tests for TrendScorer."""

import pytest
import pandas as pd
import numpy as np
from unittest.mock import patch, MagicMock

from stock_watcher.news.trend_scorer import TrendScorer
from stock_watcher.models import TrendScore


def _make_rising_data(days=60, start_price=100.0, step=1.2):
    """Generate synthetic OHLC data with upward trend and realistic pullbacks."""
    dates = pd.date_range("2025-04-01", periods=days, freq="B")
    np.random.seed(42)
    closes = [start_price]
    for i in range(1, days):
        # Upward bias with occasional small pullbacks
        change = step + np.random.normal(0, step * 0.6)
        closes.append(closes[-1] + max(change, -step * 0.3))
    closes_arr = np.array(closes)
    df = pd.DataFrame({
        "date": dates,
        "open": closes_arr - np.abs(np.random.normal(0, 0.5, days)),
        "close": closes_arr,
        "high": closes_arr + np.abs(np.random.normal(0, 1.0, days)),
        "low": closes_arr - np.abs(np.random.normal(0, 1.0, days)),
        "amount": np.linspace(1e6, 3e6, days) + np.abs(np.random.normal(0, 5e4, days)),
    })
    return df


def _make_falling_data(days=60):
    """Generate synthetic OHLC data with steady downward trend."""
    dates = pd.date_range("2025-04-01", periods=days, freq="B")
    closes = [100.0 - i * 0.8 for i in range(days)]
    df = pd.DataFrame({
        "date": dates,
        "open": [c + 0.3 for c in closes],
        "close": closes,
        "high": [c + 0.5 for c in closes],
        "low": [c - 0.5 for c in closes],
        "amount": [1000000 - i * 5000 for i in range(days)],
    })
    return df


class TestTrendScorer:
    """T15: TrendScorer — 100-point technical indicator scoring."""

    def test_bullish_score_high(self):
        """Strong uptrend → total_score ≥ 70."""
        df = _make_rising_data(60, start_price=100.0, step=1.2)
        scorer = TrendScorer()
        score = scorer.score("test_code", df)
        assert score.total_score >= 70, f"Expected ≥70, got {score.total_score}"
        assert score.ma_score == 30  # MA5 > MA10 > MA20 in strong uptrend
        assert score.consecutive_up_days >= 3

    def test_bearish_score_low(self):
        """Strong downtrend → total_score ≤ 25."""
        df = _make_falling_data(60)
        scorer = TrendScorer()
        score = scorer.score("test_code", df)
        assert score.total_score <= 25, f"Expected ≤25, got {score.total_score}"

    def test_insufficient_data(self):
        """Less than 20 rows → score = 0, reason = 'insufficient_data'."""
        df = _make_rising_data(10)
        scorer = TrendScorer()
        score = scorer.score("test_code", df)
        assert score.total_score == 0
        assert score.reason == "insufficient_data"

    def test_score_cache(self):
        """Second call with same code+data returns same result from cache."""
        df = _make_rising_data(60)
        scorer = TrendScorer()
        score1 = scorer.score("cached_code", df)
        score2 = scorer.score("cached_code", df)
        assert score1.total_score == score2.total_score
        assert score1 is score2  # same object from cache

    def test_score_fields_populated(self):
        """All sub-scores should be populated."""
        df = _make_rising_data(60)
        scorer = TrendScorer()
        score = scorer.score("populated", df)
        assert isinstance(score, TrendScore)
        assert score.ma_score >= 0
        assert score.macd_score >= 0
        assert score.rsi_score >= 0
        assert score.consecutive_up >= 0
        assert score.volume_ratio_score >= 0
        assert score.ma5 > 0
        assert score.rsi14 > 0

    def test_weighted_total(self):
        """Total score = sum of individual scores."""
        df = _make_rising_data(60)
        scorer = TrendScorer()
        score = scorer.score("weighted", df)
        assert score.total_score == (
            score.ma_score + score.macd_score + score.rsi_score +
            score.consecutive_up + score.volume_ratio_score
        )
