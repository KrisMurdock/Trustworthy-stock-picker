"""财联社电报抓取器 — akshare.stock_telegraph_cls()."""

from __future__ import annotations

import logging

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)


class CLSTelegraphFetcher(BaseNewsFetcher):
    """CLS breaking news telegraph — short items, high priority."""

    source_name: str = "cls_telegraph"
    source_priority: int = 1  # CLS is primary breaking news source

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        if not self.enabled:
            return []

        try:
            import akshare
            df = await self._fetch_with_retry(akshare.stock_info_global_cls)
        except Exception:
            logger.warning("%s: fetch failed", self.source_name, )
            self._record_failure()
            return []

        items: list[NewsItem] = []
        seen: set[str] = set()

        try:
            for _idx, row in df.iterrows():
                content = str(row.get("content", "")).strip()
                ctime = str(row.get("ctime", "")).strip()
                if not content:
                    continue

                key = f"{content[:80]}:{ctime}"
                if key in seen:
                    continue
                seen.add(key)

                items.append(NewsItem(
                    id=self._make_id(self.source_name, key),
                    source=self.source_name,
                    title=content,
                    summary=content[:200],
                    url="",
                    published_at=ctime,
                    related_codes=[],
                    source_priority=self.source_priority,
                ))
            self._record_success()
        except Exception:
            logger.warning("%s: parse failed", self.source_name, )
            self._record_failure()

        return items
