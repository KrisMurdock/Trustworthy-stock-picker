"""Polling scheduler with per-stock queue, exponential backoff, and trading calendar."""

from __future__ import annotations

import datetime as dt
import random
from typing import Optional

from stock_watcher.config import BackoffConfig
from stock_watcher.models import Market

# ---------------------------------------------------------------------------
# Trading schedules (all times in CST / UTC+8)
# ---------------------------------------------------------------------------

TRADING_SCHEDULES = {
    "a_share": [
        (dt.time(9, 15), dt.time(11, 30)),   # pre-auction + morning session
        (dt.time(13, 0), dt.time(15, 0)),     # afternoon session
    ],
    "hk": [
        (dt.time(9, 15), dt.time(12, 0)),     # pre-auction + morning session
        (dt.time(13, 0), dt.time(16, 10)),    # afternoon + closing auction
    ],
    # US trading, Beijing time — spans midnight; handled specially
    # Extended hours: pre-market 4:00 + regular 9:30 + after-hours → 20:00 ET
    "us_edt": (dt.time(16, 0), dt.time(8, 0)),    # summer: 16:00–08:00
    "us_est": (dt.time(17, 0), dt.time(9, 0)),     # winter: 17:00–09:00
}

CST = dt.timezone(dt.timedelta(hours=8))  # China Standard Time (UTC+8)


def _now_cst() -> dt.datetime:
    """Return current time in China Standard Time."""
    return dt.datetime.now(CST)


def _is_weekday(t: dt.datetime) -> bool:
    """Return True if t is Mon-Fri."""
    return t.weekday() < 5  # 0=Mon … 4=Fri


def _is_in_any_session(t: dt.datetime, sessions: list[tuple[dt.time, dt.time]]) -> bool:
    """Return True if t falls within any of the given (start, end) time windows."""
    current_time = t.time()
    for start, end in sessions:
        if start <= current_time < end:
            return True
    return False


def _is_us_dst(t: dt.datetime) -> bool:
    """Return True if US Daylight Saving Time is in effect at datetime *t* (CST).

    US DST starts on the 2nd Sunday of March and ends on the 1st Sunday of
    November.  We check by constructing the transition dates for *t*'s year.
    """
    year = t.year

    def _nth_sunday(month: int, n: int) -> dt.date:
        """Return the date of the n-th Sunday in *month* of *year*."""
        first = dt.date(year, month, 1)
        # weekday(): Mon=0 … Sun=6
        days_until_sunday = (6 - first.weekday()) % 7
        return first + dt.timedelta(days=days_until_sunday + 7 * (n - 1))

    start_dst = _nth_sunday(3, 2)  # 2nd Sunday in March
    end_dst = _nth_sunday(11, 1)   # 1st Sunday in November

    today = t.date()
    return start_dst <= today < end_dst


def _is_in_us_session(t: dt.datetime) -> bool:
    """Return True if *t* (CST) falls within US regular trading hours."""
    key = "us_edt" if _is_us_dst(t) else "us_est"
    open_time, close_time = TRADING_SCHEDULES[key]
    current = t.time()
    # US session crosses midnight in CST: e.g. 21:30 ≤ t  OR  t < 04:00
    return current >= open_time or current < close_time


def is_a_share_trading_time(t: Optional[dt.datetime] = None) -> bool:
    """Return True if now (or *t*) is during A-share trading hours."""
    if t is None:
        t = _now_cst()
    return _is_weekday(t) and _is_in_any_session(t, TRADING_SCHEDULES["a_share"])


def is_hk_trading_time(t: Optional[dt.datetime] = None) -> bool:
    """Return True if now (or *t*) is during HK trading hours."""
    if t is None:
        t = _now_cst()
    return _is_weekday(t) and _is_in_any_session(t, TRADING_SCHEDULES["hk"])


def is_us_trading_time(t: Optional[dt.datetime] = None) -> bool:
    """Return True if now (or *t*) is during US trading hours (CST)."""
    if t is None:
        t = _now_cst()
    return _is_weekday(t) and _is_in_us_session(t)


def is_any_market_open(t: Optional[dt.datetime] = None) -> bool:
    """Return True if any monitored market is currently open."""
    return is_a_share_trading_time(t) or is_hk_trading_time(t) or is_us_trading_time(t)


# ---------------------------------------------------------------------------
# TradingCalendar
# ---------------------------------------------------------------------------


class TradingCalendar:
    """Tells whether a given stock code is in a trading window right now."""

    def is_trading(self, code: str, at: Optional[dt.datetime] = None) -> bool:
        """Return True if the market for *code* is currently trading."""
        if at is None:
            at = _now_cst()
        try:
            market = Market.from_code(code)
        except ValueError:
            return False

        if market in (Market.SHANGHAI, Market.SHENZHEN):
            return is_a_share_trading_time(at)
        elif market == Market.HONGKONG:
            return is_hk_trading_time(at)
        elif market == Market.USA:
            return is_us_trading_time(at)
        return False

    def status_string(self, at: Optional[dt.datetime] = None) -> str:
        """Return a human-readable status for the current time."""
        if at is None:
            at = _now_cst()

        a_open = is_a_share_trading_time(at)
        hk_open = is_hk_trading_time(at)
        us_open = is_us_trading_time(at)

        open_parts: list[str] = []
        if a_open:
            open_parts.append("A股")
        if hk_open:
            open_parts.append("港股")
        if us_open:
            open_parts.append("美股")

        if open_parts:
            return "、".join(open_parts) + " 交易中"
        return "休市"


# ---------------------------------------------------------------------------
# BackoffController
# ---------------------------------------------------------------------------


class BackoffController:
    """Exponential backoff with jitter for failed requests."""

    def __init__(
        self, base: float = 5.0, max_delay: float = 120.0, multiplier: float = 2.0
    ) -> None:
        self._base = base
        self._max = max_delay
        self._multiplier = multiplier
        self._failures: int = 0

    @classmethod
    def from_config(cls, config: "BackoffConfig") -> "BackoffController":
        """Create a BackoffController from a BackoffConfig."""
        return cls(
            base=config.base,
            max_delay=config.max_delay,
            multiplier=config.multiplier,
        )

    @property
    def consecutive_failures(self) -> int:
        return self._failures

    @property
    def current_delay(self) -> float:
        """The raw (unjittered) current backoff delay."""
        return min(self._base * (self._multiplier ** self._failures), self._max)

    @property
    def is_backed_off(self) -> bool:
        return self._failures > 0

    def backoff(self) -> None:
        """Increment the failure counter, increasing the delay."""
        self._failures += 1

    def reset(self) -> None:
        """Reset the failure counter to zero (e.g. after a successful request)."""
        self._failures = 0

    def get_delay(self) -> float:
        """Return the current backoff delay with ±25% jitter applied."""
        raw = self.current_delay
        jitter = raw * 0.25 * (random.random() * 2 - 1)  # ±25%
        return raw + jitter


# ---------------------------------------------------------------------------
# PollQueue
# ---------------------------------------------------------------------------


class PollQueue:
    """Round-robin queue that cycles through stock codes for polling."""

    def __init__(self, codes: list[str]) -> None:
        self._codes: list[str] = list(codes)
        self._index: int = 0

    @property
    def size(self) -> int:
        return len(self._codes)

    @property
    def all_codes(self) -> list[str]:
        """Return a copy of all codes currently in the queue."""
        return list(self._codes)

    def next(self) -> Optional[str]:
        """Return the next stock code to poll, or None if the queue is empty."""
        if not self._codes:
            return None
        code = self._codes[self._index]
        self._index = (self._index + 1) % len(self._codes)
        return code

    def add(self, code: str) -> None:
        """Add a stock code to the queue (no-op if already present)."""
        if code not in self._codes:
            self._codes.append(code)

    def remove(self, code: str) -> None:
        """Remove a stock code from the queue."""
        if code in self._codes:
            idx = self._codes.index(code)
            self._codes.remove(code)
            # Adjust index so we don't skip the next element
            if self._codes and idx < self._index:
                self._index -= 1
            elif self._codes:
                self._index = self._index % len(self._codes)

    def has(self, code: str) -> bool:
        """Return True if the code is in the queue."""
        return code in self._codes


# ---------------------------------------------------------------------------
# MarketBackoffManager — per-market backoff isolation
# ---------------------------------------------------------------------------


class MarketBackoffManager:
    """Manages separate BackoffController instances per market.

    Failures in one market (e.g. A-share) do not delay requests for
    another market (e.g. HK).
    """

    def __init__(self, config: "BackoffConfig") -> None:
        self._config = config
        self._controllers: dict[str, BackoffController] = {}

    def _market_key(self, code: str) -> str:
        """Return a market key for the given stock code.

        Uses the two-letter prefix (sh/sz/hk) as the key, collapsing
        sh + sz into 'a_share' so both mainland exchanges share one
        controller.
        """
        try:
            market = Market.from_code(code)
        except ValueError:
            return "unknown"
        if market in (Market.SHANGHAI, Market.SHENZHEN):
            return "a_share"
        return market.value  # "hk"

    def get(self, code: str) -> BackoffController:
        """Return (or create) the BackoffController for *code*'s market."""
        key = self._market_key(code)
        if key not in self._controllers:
            self._controllers[key] = BackoffController.from_config(self._config)
        return self._controllers[key]

    def reset_all(self) -> None:
        """Reset all controllers (e.g. after config reload)."""
        self._controllers.clear()

    @property
    def any_backed_off(self) -> bool:
        """True if any market is currently in backoff."""
        return any(c.is_backed_off for c in self._controllers.values())

    @property
    def max_delay(self) -> float:
        """The largest current delay across all backed-off markets (0 if none)."""
        delays = [c.current_delay for c in self._controllers.values() if c.is_backed_off]
        return max(delays) if delays else 0.0
