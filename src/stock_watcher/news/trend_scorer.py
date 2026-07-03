"""趋势打分器 — 基于技术指标给个股做 100 分制打分."""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

import numpy as np
import pandas as pd

from stock_watcher.models import TrendScore

if TYPE_CHECKING:
    pass

logger = logging.getLogger(__name__)


class TrendScorer:
    """Compute 100-point technical indicator score for a stock.

    Rubric (configurable via weights):
        MA 多头排列:  30 points (MA5 > MA10 > MA20)
        MACD 方向:    25 points (DIF > DEA, histogram)
        RSI 强度:     20 points (60 ≤ RSI ≤ 80 = optimal)
        连涨天数:     15 points (≥3 days)
        量比放大:     10 points (today_vol / 5d_avg_vol)
    """

    def __init__(
        self,
        ma_weight: int = 30,
        macd_weight: int = 25,
        rsi_weight: int = 20,
        consecutive_up_weight: int = 15,
        volume_ratio_weight: int = 10,
    ) -> None:
        self._ma_w = ma_weight
        self._macd_w = macd_weight
        self._rsi_w = rsi_weight
        self._consec_w = consecutive_up_weight
        self._vol_w = volume_ratio_weight
        self._cache: dict[str, TrendScore] = {}

    def score(self, code: str, df: pd.DataFrame) -> TrendScore:
        """Compute trend score from daily OHLCV DataFrame.

        Args:
            code: Stock code (for cache key).
            df: DataFrame with columns [date, open, close, high, low, amount].
                Uses amount as volume proxy (Tencent source lacks volume).

        Returns:
            TrendScore with all indicator sub-scores populated.
        """
        # Check cache
        if code in self._cache:
            return self._cache[code]

        if len(df) < 20:
            ts = TrendScore(code=code, total_score=0, reason="insufficient_data")
            self._cache[code] = ts
            return ts

        closes = df["close"].values.astype(float)
        amounts = df["amount"].values.astype(float)

        # --- MA alignment ---
        ma5_arr = _sma(closes, 5)
        ma10_arr = _sma(closes, 10)
        ma20_arr = _sma(closes, 20)
        ma5 = float(ma5_arr[-1])
        ma10 = float(ma10_arr[-1])
        ma20 = float(ma20_arr[-1])

        if ma5 > ma10 > ma20:
            ma_score = self._ma_w
        elif ma5 > ma10:
            ma_score = self._ma_w // 2
        else:
            ma_score = 0

        # --- MACD ---
        dif, dea, macd_hist = _macd(closes)
        dif_last = float(dif[-1])
        dea_last = float(dea[-1])
        hist_last = float(macd_hist[-1])
        hist_prev = float(macd_hist[-2]) if len(macd_hist) >= 2 else 0.0

        if dif_last > dea_last and hist_last > hist_prev:
            macd_score = self._macd_w
        elif dif_last > dea_last:
            macd_score = int(self._macd_w * 0.6)
        else:
            macd_score = 0

        # --- RSI(14) ---
        rsi14 = float(_rsi(closes, 14)[-1])
        if 60 <= rsi14 <= 80:
            rsi_score = self._rsi_w
        elif 50 <= rsi14 < 60:
            rsi_score = int(self._rsi_w * 0.6)
        elif 40 <= rsi14 < 50:
            rsi_score = int(self._rsi_w * 0.25)
        else:
            rsi_score = 0

        # --- Consecutive up days ---
        up_days = 0
        for i in range(len(closes) - 1, 0, -1):
            if closes[i] > closes[i - 1]:
                up_days += 1
            else:
                break

        if up_days >= 3:
            consec_score = self._consec_w
        elif up_days == 2:
            consec_score = int(self._consec_w * 0.67)
        elif up_days == 1:
            consec_score = int(self._consec_w * 0.33)
        else:
            consec_score = 0

        # --- Volume ratio ---
        if len(amounts) >= 6:
            today_vol = amounts[-1]
            avg_5d = np.mean(amounts[-6:-1])
            vol_ratio = float(today_vol / avg_5d) if avg_5d > 0 else 1.0
        else:
            vol_ratio = 1.0

        if vol_ratio >= 1.5:
            vol_score = self._vol_w
        elif vol_ratio >= 1.0:
            vol_score = self._vol_w // 2
        else:
            vol_score = 0

        total = ma_score + macd_score + rsi_score + consec_score + vol_score

        ts = TrendScore(
            code=code,
            total_score=total,
            ma_score=ma_score,
            macd_score=macd_score,
            rsi_score=rsi_score,
            consecutive_up=consec_score,
            volume_ratio_score=vol_score,
            ma5=ma5,
            ma10=ma10,
            ma20=ma20,
            rsi14=rsi14,
            consecutive_up_days=up_days,
        )
        self._cache[code] = ts
        return ts


# ---------------------------------------------------------------------------
# Technical indicator helpers
# ---------------------------------------------------------------------------


def _sma(data: np.ndarray, period: int) -> np.ndarray:
    """Simple moving average."""
    if len(data) < period:
        return np.full_like(data, np.nan, dtype=float)
    result = np.full_like(data, np.nan, dtype=float)
    for i in range(period - 1, len(data)):
        result[i] = float(np.mean(data[i - period + 1:i + 1]))
    return result


def _ema(data: np.ndarray, period: int) -> np.ndarray:
    """Exponential moving average."""
    result = np.full_like(data, np.nan, dtype=float)
    if len(data) < period:
        return result
    result[period - 1] = float(np.mean(data[:period]))
    multiplier = 2.0 / (period + 1)
    for i in range(period, len(data)):
        result[i] = (data[i] - result[i - 1]) * multiplier + result[i - 1]
    return result


def _macd(closes: np.ndarray, fast: int = 12, slow: int = 26, signal: int = 9):
    """MACD: returns (DIF, DEA, histogram)."""
    ema_fast = _ema(closes, fast)
    ema_slow = _ema(closes, slow)
    dif = ema_fast - ema_slow
    dea = _ema(dif[~np.isnan(dif)], signal) if np.any(~np.isnan(dif)) else np.full_like(dif, np.nan)
    # Pad DEA back to original length
    if len(dea) < len(dif):
        pad = len(dif) - len(dea)
        dea = np.concatenate([np.full(pad, np.nan), dea])
    hist = (dif - dea) * 2
    return dif, dea, hist


def _rsi(closes: np.ndarray, period: int = 14) -> np.ndarray:
    """Relative Strength Index."""
    if len(closes) < period + 1:
        return np.full_like(closes, 50.0, dtype=float)
    deltas = np.diff(closes)
    gains = np.where(deltas > 0, deltas, 0.0)
    losses = np.where(deltas < 0, -deltas, 0.0)

    result = np.full_like(closes, np.nan, dtype=float)
    # Initial average
    avg_gain = float(np.mean(gains[:period]))
    avg_loss = float(np.mean(losses[:period]))

    for i in range(period, len(closes)):
        if avg_loss == 0:
            result[i] = 100.0
        else:
            rs = avg_gain / avg_loss
            result[i] = 100.0 - (100.0 / (1.0 + rs))
        # Wilder smoothing
        avg_gain = (avg_gain * (period - 1) + gains[i - 1]) / period
        avg_loss = (avg_loss * (period - 1) + losses[i - 1]) / period

    return result
