"""新浪财经滚动新闻 (feed.mix.sina.com.cn)."""

from __future__ import annotations

import logging
from typing import Any

import httpx

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

_SINA_ROLL_URL = "https://feed.mix.sina.com.cn/api/roll/get"
_TIMEOUT = 15

# 与股市最相关的 LID（经验精选）
_DEFAULT_LIDS = [
    2509,  # 全球财经（主力，含 A 股/港股资讯）
    2517,  # A 股
    2516,  # 港股
    2515,  # 美股
]

# A 股常见公司名 → 代码映射（用于从标题提取相关代码）
_NAME_TO_CODE: dict[str, str] = {}


class SinaRollNewsFetcher(BaseNewsFetcher):
    """新浪财经滚动新闻 — 全球财经快讯."""

    source_name = "sina_roll"
    source_priority = 2

    def __init__(
        self,
        enabled: bool = True,
        lids: list[int] | None = None,
        fetch_per_lid: int = 10,
    ) -> None:
        super().__init__(enabled=enabled)
        self._lids = lids or _DEFAULT_LIDS
        self._fetch_per_lid = fetch_per_lid

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        items: list[NewsItem] = []
        seen_urls: set[str] = set()

        async with httpx.AsyncClient(timeout=_TIMEOUT) as client:
            for lid in self._lids:
                try:
                    batch = await self._fetch_lid(client, lid, watchlist_codes)
                    for item in batch:
                        if item.url not in seen_urls:
                            seen_urls.add(item.url)
                            items.append(item)
                except Exception as exc:
                    logger.warning("sina_roll lid=%d failed: %s", lid, exc)
                    self._record_failure()
                else:
                    self._record_success()

        return items

    async def _fetch_lid(
        self,
        client: httpx.AsyncClient,
        lid: int,
        watchlist_codes: list[str],
    ) -> list[NewsItem]:
        resp = await client.get(
            _SINA_ROLL_URL,
            params={
                "pageid": 153,
                "lid": lid,
                "num": self._fetch_per_lid,
                "page": 1,
            },
            headers={"Referer": "https://finance.sina.com.cn/"},
        )
        resp.raise_for_status()
        data: dict[str, Any] = resp.json()
        raw_items: list[dict[str, Any]] = (
            data.get("result", {}).get("data", []) or []
        )

        results: list[NewsItem] = []
        for raw in raw_items:
            title = str(raw.get("title", "")).strip()
            if not title:
                continue
            url = str(raw.get("url", "")).strip()
            ctime = str(raw.get("ctime", "")).strip()  # Unix timestamp string
            intro = str(raw.get("intro", "")).strip()

            # 时间戳转换
            published_at = ""
            try:
                ts = int(ctime)
                from datetime import datetime, timezone, timedelta
                published_at = datetime.fromtimestamp(
                    ts, tz=timezone(timedelta(hours=8))
                ).isoformat()
            except (ValueError, OSError):
                published_at = ctime

            # 提取相关股票代码
            related = self._extract_codes(title, watchlist_codes)

            item_id = self._make_id("sina_roll", url or title)
            results.append(NewsItem(
                id=item_id,
                source=self.source_name,
                title=title,
                summary=intro[:200] if intro else title[:200],
                url=url,
                published_at=published_at,
                related_codes=related,
                source_priority=self.source_priority,
            ))

        return results

    def _extract_codes(
        self, title: str, watchlist_codes: list[str]
    ) -> list[str]:
        """从标题提取自选股相关代码 — 基于已知公司名映射."""
        if not _NAME_TO_CODE:
            return []
        matched: list[str] = []
        for name, code in _NAME_TO_CODE.items():
            if name in title and code in watchlist_codes:
                matched.append(code)
        return matched

    @classmethod
    def set_name_mapping(cls, mapping: dict[str, str]) -> None:
        """设置公司名→代码映射（各 Fetcher 共享）."""
        _NAME_TO_CODE.clear()
        _NAME_TO_CODE.update(mapping)
