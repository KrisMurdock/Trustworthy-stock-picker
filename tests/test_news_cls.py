"""Tests for CLSTelegraphFetcher."""

import pytest
from unittest.mock import MagicMock, patch

from stock_watcher.news.cls_telegraph import CLSTelegraphFetcher
from stock_watcher.models import NewsItem


class TestCLSTelegraphFetcher:
    """T06: CLSTelegraphFetcher — CLS breaking news via akshare."""

    def test_source_name_and_priority(self):
        fetcher = CLSTelegraphFetcher()
        assert fetcher.source_name == "cls_telegraph"
        assert fetcher.source_priority == 1  # CLS is primary breaking news

    def test_fetch_uses_title_as_summary(self):
        """CLS items are short; title doubles as summary."""
        fetcher = CLSTelegraphFetcher()
        mock_df = MagicMock()
        mock_df.iterrows.return_value = iter([
            (0, {"content": "【快讯】央行降准0.5个百分点", "ctime": "2025-07-01 10:30"}),
        ])

        with patch("akshare.stock_info_global_cls", return_value=mock_df):
            import asyncio
            items = asyncio.run(fetcher.fetch([]))

        assert len(items) == 1
        assert items[0].title == "【快讯】央行降准0.5个百分点"
        assert items[0].summary == items[0].title
        assert items[0].source_priority == 1

    def test_handles_error_gracefully(self):
        fetcher = CLSTelegraphFetcher()
        with patch("akshare.stock_info_global_cls", side_effect=RuntimeError("fail")):
            import asyncio
            items = asyncio.run(fetcher.fetch([]))
        assert items == []
