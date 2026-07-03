"""新浪个股新闻 — HTML 页面解析."""

from __future__ import annotations

import logging
import re

import httpx
from bs4 import BeautifulSoup

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

_SINA_STOCK_URL = (
    "https://vip.stock.finance.sina.com.cn/corp/go.php/"
    "vCB_AllNewsStock/symbol/{prefix}{code}.phtml"
)
_TIMEOUT = 15


class SinaStockNewsFetcher(BaseNewsFetcher):
    """新浪个股新闻 — 解析 HTML 页面提取新闻列表."""

    source_name = "sina_stock"
    source_priority = 1  # 个股新闻可信度高

    def __init__(self, enabled: bool = True, max_per_stock: int = 10) -> None:
        super().__init__(enabled=enabled)
        self._max_per_stock = max_per_stock

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        """抓取自选股个股新闻（仅 A 股）."""
        a_codes = [c for c in watchlist_codes if c.startswith(("sh", "sz"))]
        if not a_codes:
            return []

        items: list[NewsItem] = []
        seen: set[str] = set()

        async with httpx.AsyncClient(timeout=_TIMEOUT) as client:
            for code in a_codes:
                try:
                    batch = await self._fetch_stock(client, code)
                    for item in batch:
                        if item.id not in seen:
                            seen.add(item.id)
                            items.append(item)
                except Exception as exc:
                    logger.warning("sina_stock %s failed: %s", code, exc)
                    self._record_failure()
                else:
                    self._record_success()

        return items

    async def _fetch_stock(
        self, client: httpx.AsyncClient, code: str
    ) -> list[NewsItem]:
        """抓取单只个股新闻."""
        prefix, raw_code = code[:2], code[2:]
        url = _SINA_STOCK_URL.format(prefix=prefix, code=raw_code)

        resp = await client.get(
            url,
            headers={"Referer": "https://finance.sina.com.cn/"},
        )
        resp.raise_for_status()

        soup = BeautifulSoup(resp.text, "html.parser")
        results: list[NewsItem] = []

        # 新浪个股新闻页面结构: datelist > ul > (date text + <a> + <br/>)*
        datelist = soup.find("div", class_="datelist")
        if not datelist:
            return results

        # 直接提取所有 <a> 标签
        for a_tag in datelist.find_all("a"):
            title = a_tag.get_text(strip=True)
            href = a_tag.get("href", "").strip()
            if not title or len(title) < 4:
                continue

            # 从前面的文本节点提取日期
            date_str = ""
            prev = a_tag.previous_sibling
            if prev and hasattr(prev, "string") is False:
                prev = prev.previous_sibling
            if prev:
                text = str(prev).strip()
                # 匹配日期格式 "2026-07-02 17:20"
                import re as _re
                m = _re.search(r"(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2})", text)
                if m:
                    date_str = m.group(1)

            # 补全相对 URL
            if href and not href.startswith("http"):
                href = f"https://vip.stock.finance.sina.com.cn{href}"

            item_id = self._make_id("sina_stock", href or f"{code}:{title}")

            published_at = ""
            if date_str:
                published_at = self._parse_date(date_str)

            results.append(NewsItem(
                id=item_id,
                source=self.source_name,
                title=title,
                summary=title[:200],
                url=href,
                published_at=published_at,
                related_codes=[code],
                source_priority=self.source_priority,
            ))

            if len(results) >= self._max_per_stock:
                break

        return results

    @staticmethod
    def _parse_date(date_str: str) -> str:
        """解析日期字符串为 ISO 8601."""
        from datetime import datetime

        date_str = date_str.strip()

        # "2025-07-03 15:30"
        try:
            dt = datetime.strptime(date_str, "%Y-%m-%d %H:%M")
            return dt.isoformat()
        except ValueError:
            pass

        # "07-03 15:30" → 当前年份
        try:
            dt = datetime.strptime(date_str, "%m-%d %H:%M")
            return dt.replace(year=datetime.now().year).isoformat()
        except ValueError:
            pass

        # "2025-07-03"
        try:
            dt = datetime.strptime(date_str, "%Y-%m-%d")
            return dt.isoformat()
        except ValueError:
            pass

        return date_str
