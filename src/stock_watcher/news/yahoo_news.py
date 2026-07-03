"""雅虎财经新闻抓取器 — yfinance.Ticker(ticker).news (HK stocks)."""

from __future__ import annotations

import logging
import re
from datetime import datetime, timezone

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

_HK_RE = re.compile(r"^hk", re.IGNORECASE)


class YahooNewsFetcher(BaseNewsFetcher):
    """HK stock news via yfinance.  Not for A-shares (poor coverage)."""

    source_name: str = "yahoo_news"
    source_priority: int = 3

    @staticmethod
    def _to_yahoo_ticker(code: str) -> str:
        """Convert stock_watcher code to Yahoo Finance ticker format."""
        code = code.lower()
        if code.startswith("hk"):
            return f"{code[2:]}.HK"
        elif code.startswith("sz"):
            return f"{code[2:]}.SZ"
        elif code.startswith("sh"):
            return f"{code[2:]}.SS"
        return code

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        if not self.enabled:
            return []

        items: list[NewsItem] = []
        seen_urls: set[str] = set()

        for code in watchlist_codes:
            # Only process HK codes; warn on A-share codes
            if not _HK_RE.match(code):
                continue

            try:
                ticker = self._to_yahoo_ticker(code)
                import yfinance
                t = yfinance.Ticker(ticker)
                news_list = t.news or []
            except Exception:
                logger.warning("%s: fetch failed for %s", self.source_name, code, exc_info=True)
                self._record_failure()
                continue

            try:
                for entry in news_list:
                    url = str(entry.get("link", "")).strip()
                    title = str(entry.get("title", "")).strip()
                    if not title:
                        continue

                    # Use title as dedup key when URL is missing
                    key = url or title
                    if key in seen_urls:
                        continue
                    seen_urls.add(key)

                    # Convert epoch timestamp to ISO
                    ts = entry.get("providerPublishTime", 0)
                    published = datetime.fromtimestamp(ts, tz=timezone.utc).isoformat() if ts else ""

                    items.append(NewsItem(
                        id=self._make_id(self.source_name, key),
                        source=self.source_name,
                        title=title,
                        summary="",
                        url=url,
                        published_at=published,
                        related_codes=[code],
                        source_priority=self.source_priority,
                    ))
                self._record_success()
            except Exception:
                logger.warning("%s: parse failed for %s", self.source_name, code, exc_info=True)
                self._record_failure()
                continue

        return items
