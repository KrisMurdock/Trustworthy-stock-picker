"""东财全球快讯抓取器 — akshare.stock_info_global_em()."""

from __future__ import annotations

import logging
import re

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

# Stock code patterns in Chinese headlines
_CODE_RE = re.compile(
    r"(?:sh|sz|SH|SZ)?(\d{6})",
)


class EastMoneyFlashNewsFetcher(BaseNewsFetcher):
    """Global financial flash news from East Money via akshare."""

    source_name: str = "eastmoney_flash"
    source_priority: int = 2

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        if not self.enabled:
            return []

        try:
            import akshare, asyncio as _asyncio
            df = await _asyncio.to_thread(akshare.stock_info_global_em)
        except Exception:
            logger.warning("%s: fetch failed", self.source_name, exc_info=True)
            self._record_failure()
            return []

        items: list[NewsItem] = []
        seen_urls: set[str] = set()

        try:
            for _idx, row in df.iterrows():
                url = str(row.get("链接", "")).strip()
                if not url:
                    continue
                if url in seen_urls:
                    continue
                seen_urls.add(url)

                title = str(row.get("标题", "")).strip()
                content = str(row.get("内容", "")).strip()
                summary = (content[:200] if content else title[:200])
                published = str(row.get("发布时间", "")).strip()

                # Extract stock codes from title
                codes: list[str] = []
                for m in _CODE_RE.finditer(title):
                    raw = m.group(1)
                    if len(raw) == 6:
                        codes.append(f"sz{raw}" if raw.startswith(("0", "3")) else f"sh{raw}")

                items.append(NewsItem(
                    id=self._make_id(self.source_name, url),
                    source=self.source_name,
                    title=title,
                    summary=summary,
                    url=url,
                    published_at=published,
                    related_codes=list(set(codes)),
                    source_priority=self.source_priority,
                ))
            self._record_success()
        except Exception:
            logger.warning("%s: parse failed", self.source_name, exc_info=True)
            self._record_failure()

        return items
