"""板块关键词匹配器 — 新闻标题关键词→概念板块模糊匹配."""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)


class SectorMatcher:
    """Match news headlines to concept sectors via config-driven keyword mapping."""

    def __init__(
        self,
        sector_keywords: dict[str, list[str]],
        sector_list: list[str] | None = None,
    ) -> None:
        """
        Args:
            sector_keywords: {keyword: [sector_name, ...]} mapping from config.
            sector_list: Valid sector names (from live akshare data).
                         Sectors not in this list are filtered out.
        """
        # Normalize to lowercase for case-insensitive matching
        self._keywords: dict[str, list[str]] = {
            k.lower(): v for k, v in sector_keywords.items()
        }
        self._sector_set: set[str] = {s for s in (sector_list or [])}

    def match(self, news: NewsItem) -> list[str]:
        """Extract sector names from news title and summary.

        Returns deduplicated list of sector names that:
        1. Match a keyword from the mapping table
        2. Exist in the live sector list (if provided)
        """
        text = f"{news.title} {news.summary}".lower()
        if not text.strip():
            return []

        matched: set[str] = set()
        for keyword, sectors in self._keywords.items():
            if keyword in text:
                for sector in sectors:
                    if not self._sector_set or sector in self._sector_set:
                        matched.add(sector)

        return list(matched)
