"""Tests for EastMoneyStockNewsFetcher."""

import pytest
from unittest.mock import AsyncMock, patch, MagicMock

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.news.eastmoney_stock import EastMoneyStockNewsFetcher
from stock_watcher.models import NewsItem


class TestEastMoneyStockNewsFetcher:
    """T03: EastMoneyStockNewsFetcher — per-watchlist stock news via akshare."""

    def test_source_name_and_priority(self):
        fetcher = EastMoneyStockNewsFetcher()
        assert fetcher.source_name == "eastmoney_stock"
        assert fetcher.source_priority == 2

    def test_fetch_returns_news_items(self):
        """fetch() should convert akshare output to list[NewsItem]."""
        fetcher = EastMoneyStockNewsFetcher()
        # Mock akshare.stock_news_em to return a DataFrame
        mock_df = MagicMock()
        mock_df.iterrows.return_value = iter([
            (0, {
                "关键词": "", "内容": "摘要内容",
                "发布时间": "2025-07-01 10:30:00",
                "新闻标题": "贵州茅台发布年报",
                "新闻链接": "http://example.com/1",
                "个股代码": "600519",
            }),
            (1, {
                "关键词": "", "内容": "摘要2",
                "发布时间": "2025-07-01 11:00:00",
                "新闻标题": "茅台分红公告",
                "新闻链接": "http://example.com/2",
                "个股代码": "600519",
            }),
        ])

        with patch("akshare.stock_news_em", return_value=mock_df):
            import asyncio
            items = asyncio.run(fetcher.fetch(["sh600519"]))

        assert len(items) == 2
        assert all(isinstance(item, NewsItem) for item in items)
        assert items[0].title == "贵州茅台发布年报"
        assert items[0].source == "eastmoney_stock"
        assert items[0].source_priority == 2
        assert items[0].url == "http://example.com/1"
        assert items[0].related_codes == ["sh600519"]

    def test_strips_prefix_before_akshare_call(self):
        """sh600519 → 600519 when calling akshare."""
        fetcher = EastMoneyStockNewsFetcher()
        mock_df = MagicMock()
        mock_df.iterrows.return_value = iter([])

        with patch("akshare.stock_news_em", return_value=mock_df) as mock_ak:
            import asyncio
            asyncio.run(fetcher.fetch(["sh600519", "sz000001"]))

        called_codes = {
            c.kwargs.get("symbol") for c in mock_ak.call_args_list
        }
        assert "600519" in called_codes
        assert "000001" in called_codes

    def test_handles_akshare_error_gracefully(self):
        """Should return [] on akshare failure, not crash."""
        fetcher = EastMoneyStockNewsFetcher()

        with patch("akshare.stock_news_em", side_effect=RuntimeError("API down")):
            import asyncio
            items = asyncio.run(fetcher.fetch(["sh600519"]))

        assert items == []
        assert len(fetcher._failure_history) == 1

    def test_dedup_by_url_within_source(self):
        """Duplicate URLs from akshare should produce single NewsItem."""
        fetcher = EastMoneyStockNewsFetcher()
        mock_df = MagicMock()
        mock_df.iterrows.return_value = iter([
            (0, {"新闻标题": "Same", "新闻链接": "http://x.com/same",
                 "发布时间": "2025-07-01", "个股代码": "600519",
                 "内容": "x", "关键词": ""}),
            (1, {"新闻标题": "Same2", "新闻链接": "http://x.com/same",
                 "发布时间": "2025-07-01", "个股代码": "600519",
                 "内容": "x", "关键词": ""}),
        ])

        with patch("akshare.stock_news_em", return_value=mock_df):
            import asyncio
            items = asyncio.run(fetcher.fetch(["sh600519"]))

        assert len(items) == 1  # deduped

    def test_skips_disabled_fetcher(self):
        fetcher = EastMoneyStockNewsFetcher(enabled=False)
        import asyncio
        items = asyncio.run(fetcher.fetch(["sh600519"]))
        assert items == []
