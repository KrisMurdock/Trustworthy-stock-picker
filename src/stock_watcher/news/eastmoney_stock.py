"""东财个股新闻抓取器 — akshare.stock_news_em()."""

from __future__ import annotations

import logging
import re
from typing import TYPE_CHECKING

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

if TYPE_CHECKING:
    pass

logger = logging.getLogger(__name__)

_SH_PREFIX = re.compile(r"^sh", re.IGNORECASE)
_SZ_PREFIX = re.compile(r"^sz", re.IGNORECASE)


class EastMoneyStockNewsFetcher(BaseNewsFetcher):
    """Per-watchlist stock news from East Money via akshare."""

    source_name: str = "eastmoney_stock"
    source_priority: int = 2

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        if not self.enabled:
            return []

        items: list[NewsItem] = []
        seen_urls: set[str] = set()

        for code in watchlist_codes:
            if not code.startswith(("sh", "sz")):
                continue  # skip HK/US codes — EM only covers A-shares

            try:
                raw_code = _SH_PREFIX.sub("", _SZ_PREFIX.sub("", code))
                import akshare
                df = await self._fetch_with_retry(akshare.stock_news_em, symbol=raw_code)
            except Exception:
                logger.warning("%s: fetch failed for %s", self.source_name, code, )
                self._record_failure()
                continue

            try:
                for _idx, row in df.iterrows():
                    url = str(row.get("新闻链接", "")).strip()
                    if not url:
                        continue
                    if url in seen_urls:
                        continue
                    seen_urls.add(url)

                    title = str(row.get("新闻标题", "")).strip()
                    summary = str(row.get("内容", "")).strip()[:200]
                    published = str(row.get("发布时间", "")).strip()

                    items.append(NewsItem(
                        id=self._make_id(self.source_name, url),
                        source=self.source_name,
                        title=title,
                        summary=summary,
                        url=url,
                        published_at=published,
                        related_codes=[code],
                        source_priority=self.source_priority,
                    ))
                self._record_success()
            except Exception:
                logger.warning(
                    "%s: parse failed for %s", self.source_name, code, 
                )
                self._record_failure()
                continue

        return items
