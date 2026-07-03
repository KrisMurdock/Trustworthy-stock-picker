"""Tests for YahooNewsFetcher."""

import pytest
from unittest.mock import MagicMock, patch

from stock_watcher.news.yahoo_news import YahooNewsFetcher
from stock_watcher.models import NewsItem


class TestYahooNewsFetcher:
    """T07: YahooNewsFetcher — HK stock news via yfinance."""

    def test_source_name_and_priority(self):
        fetcher = YahooNewsFetcher()
        assert fetcher.source_name == "yahoo_news"
        assert fetcher.source_priority == 3

    def test_converts_hk_code_to_yahoo_format(self):
        """hk00700 → 00700.HK."""
        fetcher = YahooNewsFetcher()
        result = fetcher._to_yahoo_ticker("hk00700")
        assert result == "00700.HK"

    def test_converts_sz_code_to_yahoo_format(self):
        """sz000001 → 000001.SZ."""
        fetcher = YahooNewsFetcher()
        result = fetcher._to_yahoo_ticker("sz000001")
        assert result == "000001.SZ"

    def test_converts_sh_code_to_yahoo_format(self):
        """sh600519 → 600519.SS."""
        fetcher = YahooNewsFetcher()
        result = fetcher._to_yahoo_ticker("sh600519")
        assert result == "600519.SS"

    def test_fetch_returns_news_items(self):
        fetcher = YahooNewsFetcher()
        mock_ticker = MagicMock()
        mock_ticker.news = [
            {"title": "Tencent earnings beat", "link": "http://x.com/1",
             "publisher": "Reuters", "providerPublishTime": 1719792000},
        ]

        with patch("yfinance.Ticker", return_value=mock_ticker):
            import asyncio
            items = asyncio.run(fetcher.fetch(["hk00700"]))

        assert len(items) == 1
        assert items[0].title == "Tencent earnings beat"
        assert items[0].source == "yahoo_news"
        assert items[0].related_codes == ["hk00700"]

    def test_skips_non_hk_us_codes(self):
        """Only HK codes through yfinance; warn on A-share codes."""
        fetcher = YahooNewsFetcher()
        import asyncio
        items = asyncio.run(fetcher.fetch(["sh600519"]))
        assert items == []  # silently skip A-share codes

    def test_handles_error_gracefully(self):
        fetcher = YahooNewsFetcher()
        with patch("yfinance.Ticker", side_effect=RuntimeError("fail")):
            import asyncio
            items = asyncio.run(fetcher.fetch(["hk00700"]))
        assert items == []
