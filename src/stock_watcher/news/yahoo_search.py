"""雅虎财经搜索新闻 — 通过 query2 API 按公司名搜索."""

from __future__ import annotations

import logging
from typing import Any

import httpx

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

_YAHOO_SEARCH_URL = "https://query2.finance.yahoo.com/v1/finance/search"
_TIMEOUT = 15


class YahooSearchNewsFetcher(BaseNewsFetcher):
    """雅虎财经搜索 — 按公司名搜索新闻（港股为主）."""

    source_name = "yahoo_search"
    source_priority = 2

    # 港股代码 → 英文公司名映射
    _HK_NAME_MAP: dict[str, str] = {
        "hk00700": "Tencent",
        "hk00939": "China Construction Bank",
        "hk09988": "Alibaba",
        "hk09999": "NetEase",
        "hk01810": "Xiaomi",
        "hk09618": "JD.com",
        "hk09888": "Baidu",
        "hk03690": "Meituan",
        "hk00388": "HKEX",
        "hk00941": "China Mobile",
        "hk01299": "AIA",
        "hk00005": "HSBC",
        "hk02318": "Ping An",
        "hk03968": "CMB",
        "hk01398": "ICBC",
        "hk03988": "BOC",
    }

    # 美股代码 → 公司名
    _US_NAME_MAP: dict[str, str] = {
        "ustsla": "Tesla",
        "usgoog": "Google",
        "usaapl": "Apple",
        "usmsft": "Microsoft",
        "usnvda": "NVIDIA",
        "usbaba": "Alibaba",
        "usjd": "JD.com",
        "usnflx": "Netflix",
    }

    def __init__(
        self,
        enabled: bool = True,
        news_per_search: int = 5,
    ) -> None:
        super().__init__(enabled=enabled)
        self._news_per_search = news_per_search

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        """按自选股中的港股+美股代码搜索新闻."""
        # 港股 + 美股
        target_codes = [c for c in watchlist_codes if c.startswith(("hk", "us"))]
        if not target_codes:
            return []

        items: list[NewsItem] = []
        seen: set[str] = set()

        async with httpx.AsyncClient(timeout=_TIMEOUT) as client:
            for code in target_codes:
                try:
                    batch = await self._search_code(client, code)
                    for item in batch:
                        if item.id not in seen:
                            seen.add(item.id)
                            items.append(item)
                except Exception as exc:
                    logger.warning("yahoo_search %s failed: %s", code, exc)
                    self._record_failure()
                else:
                    self._record_success()

        return items

    async def _search_code(
        self, client: httpx.AsyncClient, code: str
    ) -> list[NewsItem]:
        """搜索单只股票新闻 — 优先用公司名，其次用代码."""
        # 港股用 HK 映射，美股用 US 映射
        query = self._HK_NAME_MAP.get(code) or self._US_NAME_MAP.get(code) or self._to_yahoo(code)
        resp = await client.get(
            _YAHOO_SEARCH_URL,
            params={
                "q": query,
                "newsCount": self._news_per_search,
                "quotesCount": 0,
            },
            headers={"User-Agent": "Mozilla/5.0"},
        )
        resp.raise_for_status()
        data: dict[str, Any] = resp.json()
        raw_news: list[dict[str, Any]] = data.get("news") or []

        results: list[NewsItem] = []
        for raw in raw_news:
            title = str(raw.get("title", "")).strip()
            if not title:
                continue
            link = str(raw.get("link", "")).strip()
            publisher = str(raw.get("publisher", "")).strip()
            pub_ts = raw.get("providerPublishTime", 0)

            published_at = ""
            try:
                from datetime import datetime, timezone, timedelta
                published_at = datetime.fromtimestamp(
                    pub_ts, tz=timezone(timedelta(hours=8))
                ).isoformat()
            except (ValueError, OSError, TypeError):
                pass

            item_id = self._make_id("yahoo_search", link or title)
            results.append(NewsItem(
                id=item_id,
                source=self.source_name,
                title=title,
                summary=f"{publisher}: {title}"[:200],
                url=link,
                published_at=published_at,
                related_codes=[code],
                source_priority=self.source_priority,
            ))

        return results

    @staticmethod
    def _to_yahoo(code: str) -> str:
        """hk00700 → 0700.HK."""
        if code.startswith("hk"):
            return f"{code[2:]}.HK"
        return code
