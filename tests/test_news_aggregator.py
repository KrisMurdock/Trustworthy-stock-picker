"""Integration tests for NewsAggregator."""

import pytest
from unittest.mock import MagicMock, AsyncMock, patch

from stock_watcher.news.aggregator import NewsAggregator
from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem


class _MockFetcher(BaseNewsFetcher):
    """Concrete mock fetcher for testing aggregator."""
    source_name = "mock"
    source_priority = 2

    def __init__(self, items=None, enabled=True, should_fail=False):
        super().__init__(enabled=enabled)
        self._items = items or []
        self._should_fail = should_fail

    async def fetch(self, watchlist_codes):
        if self._should_fail:
            raise RuntimeError("mock failure")
        return self._items


class TestNewsAggregator:
    """T08: NewsAggregator — concurrent fetch, dedup, filter, persistence."""

    def _make_item(self, id_suffix="", title="Test", url="", codes=None):
        return NewsItem(
            id=f"mock_{id_suffix}" if id_suffix else self._make_id("mock", title),
            source="mock", title=title, url=url,
            related_codes=codes or [],
            source_priority=2,
        )

    @staticmethod
    def _make_id(source, identifier):
        import hashlib
        return hashlib.sha1(f"{source}:{identifier}".encode()).hexdigest()[:16]

    def test_aggregate_empty_fetchers(self):
        import asyncio
        agg = NewsAggregator([])
        items = asyncio.run(agg.aggregate(["sh600519"]))
        assert items == []

    def test_aggregate_single_fetcher(self):
        import asyncio
        item = NewsItem(id="test1", source="mock", title="News", related_codes=["sh600519"])
        fetcher = _MockFetcher([item])
        agg = NewsAggregator([fetcher])
        items = asyncio.run(agg.aggregate(["sh600519"]))
        assert len(items) == 1
        assert items[0].id == "test1"

    def test_dedup_by_id(self):
        """Items with same id should deduplicate across fetchers."""
        import asyncio
        item1 = NewsItem(id="same_id", source="mock1", title="A")
        item2 = NewsItem(id="same_id", source="mock2", title="B")
        agg = NewsAggregator([
            _MockFetcher([item1]),
            _MockFetcher([item2]),
        ])
        items = asyncio.run(agg.aggregate(["sh600519"]))
        assert len(items) == 1

    def test_watchlist_filter(self):
        """Items with no related_codes pass through; items with non-matching codes are dropped."""
        import asyncio
        market_wide = NewsItem(id="mw", source="mock", title="Market", related_codes=[])
        matching = NewsItem(id="m", source="mock", title="Match", related_codes=["sh600519"])
        non_matching = NewsItem(id="nm", source="mock", title="NoMatch", related_codes=["sz000001"])
        agg = NewsAggregator([_MockFetcher([market_wide, matching, non_matching])])
        items = asyncio.run(agg.aggregate(["sh600519"]))
        ids = {i.id for i in items}
        assert "mw" in ids      # market-wide passes
        assert "m" in ids       # matches watchlist
        assert "nm" not in ids  # doesn't match

    def test_error_isolation(self):
        """One fetcher failing should not block others."""
        import asyncio
        good_item = NewsItem(id="good", source="good", title="Good")
        agg = NewsAggregator([
            _MockFetcher([good_item]),
            _MockFetcher(should_fail=True),
        ])
        items = asyncio.run(agg.aggregate(["sh600519"]))
        assert len(items) == 1
        assert items[0].id == "good"

    def test_persist_and_load_seen(self, tmp_path):
        """Seen IDs should persist across aggregator instances."""
        import asyncio
        seen_file = tmp_path / "test_seen.jsonl"

        # First aggregator processes an item
        item = NewsItem(id="persist_test", source="mock", title="News")
        agg1 = NewsAggregator([_MockFetcher([item])], seen_path=str(seen_file))
        items1 = asyncio.run(agg1.aggregate(["sh600519"]))
        assert len(items1) == 1
        agg1.persist_seen()

        # Second aggregator should skip the already-seen item
        item2 = NewsItem(id="persist_test", source="mock", title="News")  # same id
        agg2 = NewsAggregator([_MockFetcher([item2])], seen_path=str(seen_file))
        items2 = asyncio.run(agg2.aggregate(["sh600519"]))
        assert len(items2) == 0  # deduped by persistent seen
