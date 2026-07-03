"""Tests for EastMoneyFlashNewsFetcher."""

import pytest
from unittest.mock import MagicMock, patch

from stock_watcher.news.eastmoney_flash import EastMoneyFlashNewsFetcher
from stock_watcher.models import NewsItem


class TestEastMoneyFlashNewsFetcher:
    """T04: EastMoneyFlashNewsFetcher — global flash news via akshare."""

    def test_source_name_and_priority(self):
        fetcher = EastMoneyFlashNewsFetcher()
        assert fetcher.source_name == "eastmoney_flash"
        assert fetcher.source_priority == 2

    def test_fetch_returns_news_items(self):
        fetcher = EastMoneyFlashNewsFetcher()
        mock_df = MagicMock()
        mock_df.iterrows.return_value = iter([
            (0, {"标题": "央行降准0.5个百分点", "内容": "央行宣布...",
                 "发布时间": "2025-07-01 10:00", "链接": "http://x.com/1"}),
        ])

        with patch("akshare.stock_info_global_em", return_value=mock_df):
            import asyncio
            items = asyncio.run(fetcher.fetch([]))

        assert len(items) == 1
        assert items[0].title == "央行降准0.5个百分点"
        assert items[0].source == "eastmoney_flash"

    def test_extracts_stock_codes_from_headline(self):
        """Regex extraction: find stock codes in headline text."""
        fetcher = EastMoneyFlashNewsFetcher()
        mock_df = MagicMock()
        mock_df.iterrows.return_value = iter([
            (0, {"标题": "贵州茅台(600519)大涨", "内容": "",
                 "发布时间": "2025-07-01", "链接": "http://x.com/1"}),
            (1, {"标题": "平安银行sz000001涨停", "内容": "",
                 "发布时间": "2025-07-01", "链接": "http://x.com/2"}),
            (2, {"标题": "市场整体走强", "内容": "",
                 "发布时间": "2025-07-01", "链接": "http://x.com/3"}),
        ])

        with patch("akshare.stock_info_global_em", return_value=mock_df):
            import asyncio
            items = asyncio.run(fetcher.fetch([]))

        # Item with code extraction
        codes_item1 = items[0].related_codes
        codes_item2 = items[1].related_codes
        codes_item3 = items[2].related_codes
        assert len(codes_item1) >= 1  # 600519 extracted
        assert len(codes_item2) >= 1  # sz000001 extracted
        assert codes_item3 == []      # no code in headline

    def test_handles_error_gracefully(self):
        fetcher = EastMoneyFlashNewsFetcher()
        with patch("akshare.stock_info_global_em", side_effect=RuntimeError("fail")):
            import asyncio
            items = asyncio.run(fetcher.fetch([]))
        assert items == []
