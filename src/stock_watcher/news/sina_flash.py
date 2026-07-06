"""新浪全球快讯抓取器 — akshare.stock_info_global_sina()."""

from __future__ import annotations

import logging
import re

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

_CODE_RE = re.compile(r"(?:sh|sz|SH|SZ)?(\d{6})")


class SinaFlashNewsFetcher(BaseNewsFetcher):
    """Global financial flash news from Sina via akshare."""

    source_name: str = "sina_flash"
    source_priority: int = 2

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        if not self.enabled:
            return []

        try:
            import akshare, asyncio as _asyncio
            df = await _asyncio.to_thread(akshare.stock_info_global_sina)
        except Exception:
            logger.warning("%s: fetch failed", self.source_name, )
            self._record_failure()
            return []

        items: list[NewsItem] = []
        seen_urls: set[str] = set()

        try:
            for _idx, row in df.iterrows():
                title = str(row.get("title", "")).strip()
                content = str(row.get("content", "")).strip()
                published = str(row.get("time", "")).strip()

                # Use title as dedup key (Sina may not provide URLs)
                key = f"{title}:{published}"
                if key in seen_urls or not title:
                    continue
                seen_urls.add(key)

                codes: list[str] = []
                text = f"{title} {content}"
                for m in _CODE_RE.finditer(text):
                    raw = m.group(1)
                    if len(raw) == 6:
                        codes.append(f"sz{raw}" if raw.startswith(("0", "3")) else f"sh{raw}")

                items.append(NewsItem(
                    id=self._make_id(self.source_name, key),
                    source=self.source_name,
                    title=title,
                    summary=content[:200] if content else title[:200],
                    url="",
                    published_at=published,
                    related_codes=list(set(codes)),
                    source_priority=self.source_priority,
                ))
            self._record_success()
        except Exception:
            logger.warning("%s: parse failed", self.source_name, )
            self._record_failure()

        return items
