"""Tests for SinaFlashNewsFetcher."""

import pytest
from unittest.mock import MagicMock, patch

from stock_watcher.news.sina_flash import SinaFlashNewsFetcher
from stock_watcher.models import NewsItem


class TestSinaFlashNewsFetcher:
    """T05: SinaFlashNewsFetcher — Sina flash news via akshare."""

    def test_source_name_and_priority(self):
        fetcher = SinaFlashNewsFetcher()
        assert fetcher.source_name == "sina_flash"
        assert fetcher.source_priority == 2

    def test_fetch_returns_news_items(self):
        fetcher = SinaFlashNewsFetcher()
        mock_df = MagicMock()
        mock_df.iterrows.return_value = iter([
            (0, {"title": "新浪快讯", "content": "内容", "time": "2025-07-01 10:30"}),
        ])

        with patch("akshare.stock_info_global_sina", return_value=mock_df):
            import asyncio
            items = asyncio.run(fetcher.fetch([]))

        assert len(items) == 1
        assert items[0].source == "sina_flash"

    def test_handles_error_gracefully(self):
        fetcher = SinaFlashNewsFetcher()
        with patch("akshare.stock_info_global_sina", side_effect=RuntimeError("fail")):
            import asyncio
            items = asyncio.run(fetcher.fetch([]))
        assert items == []
