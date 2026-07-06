"""行情轮询控制器 — 从 app.py 提取 _poll_loop + 告警判断逻辑."""

from __future__ import annotations

import asyncio
import logging
import time
from typing import Callable

from stock_watcher.config import RequestConfig, BackoffConfig
from stock_watcher.fetcher import get_fetcher, FetchError
from stock_watcher.models import StockQuote, AlertRule, Market
from stock_watcher.scheduler import PollQueue, MarketBackoffManager
from stock_watcher.controllers.alert_controller import AlertController

logger = logging.getLogger(__name__)


class PollController:
    """行情轮询 + 告警判断。通过回调与 TUI 层通信。"""

    def __init__(
        self,
        watchlist: list[str],
        request_cfg: RequestConfig,
        alert_ctrl: AlertController,
        backoff: MarketBackoffManager,
        poll_interval: float = 2.5,
        on_quote: Callable[[StockQuote], None] | None = None,
        on_alert: Callable[[AlertRule, StockQuote], None] | None = None,
    ) -> None:
        self._queue = PollQueue(watchlist)
        self._request_cfg = request_cfg
        self._alert_ctrl = alert_ctrl
        self._backoff = backoff
        self._poll_interval = poll_interval
        self._on_quote = on_quote
        self._on_alert = on_alert
        self._latest_quotes: dict[str, StockQuote] = {}

    async def run(self, stop_event: asyncio.Event) -> None:
        """主轮询循环，直到 stop_event 被设置."""
        logger.info("PollController started")
        while not stop_event.is_set():
            code = self._queue.next()
            if code is None:
                await asyncio.sleep(1)
                continue

            from stock_watcher.scheduler import TradingCalendar
            calendar = TradingCalendar()
            if not calendar.is_trading(code):
                await asyncio.sleep(0.1)
                continue

            b = self._backoff.get(code)
            if b.is_backed_off:
                await asyncio.sleep(b.get_delay())

            await self._fetch_one(code)
            await asyncio.sleep(self._poll_interval)

    async def _fetch_one(self, code: str) -> None:
        t0 = time.monotonic()
        b = self._backoff.get(code)
        try:
            fetcher = get_fetcher(code, self._request_cfg)
            quote = await fetcher.fetch(code)
        except FetchError as e:
            logger.warning("fetch %s failed: %s", code, e)
            b.record_failure()
            return
        except Exception:
            logger.warning("fetch %s unexpected error", code, exc_info=True)
            b.record_failure()
            return

        elapsed = (time.monotonic() - t0) * 1000
        logger.debug("fetched %s in %.0fms", code, elapsed)

        if not quote.is_valid:
            b.record_failure()
            return

        b.record_success()
        self._latest_quotes[code] = quote

        if self._on_quote:
            self._on_quote(quote)

        # Check alerts
        for alert in self._alert_ctrl.rules:
            if alert.code == code and alert.check(quote):
                if self._on_alert:
                    self._on_alert(alert, quote)

    @property
    def latest_quotes(self) -> dict[str, StockQuote]:
        return dict(self._latest_quotes)

    def update_watchlist(self, codes: list[str]) -> None:
        for code in codes:
            self._queue.add(code)
