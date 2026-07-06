"""Integration tests for aggregator with 3-state circuit breaker."""
import asyncio
import time
import pytest
from stock_watcher.news.aggregator import NewsAggregator
from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem


class _MockFetcher(BaseNewsFetcher):
    """Fetcher that returns predefined items or raises."""
    source_name = "mock"

    def __init__(self, enabled=True, items=None, should_fail=False):
        super().__init__(enabled=enabled)
        self._items = items or []
        self._should_fail = should_fail
        self.fetch_called = 0

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        self.fetch_called += 1
        if self._should_fail:
            raise RuntimeError("mock fetch error")
        return self._items


def _make_item(title="test"):
    return NewsItem(
        id=f"mock_{title}",
        source="mock",
        title=title,
        summary="",
        url="",
        published_at="",
        related_codes=[],
        source_priority=2,
    )


class TestAggregatorCircuitBreaker:
    """Aggregator respects circuit breaker states."""

    def test_green_fetcher_is_called(self):
        """GREEN fetchers are called normally."""
        f = _MockFetcher(items=[_make_item("news1")])
        agg = NewsAggregator([f], "/tmp/test_seen_cb.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 1
            assert f.fetch_called == 1

        asyncio.run(run())

    def test_red_fetcher_is_skipped(self):
        """RED (failing) fetchers are skipped entirely."""
        f = _MockFetcher(items=[_make_item("news1")])
        # Force RED state
        for _ in range(5):
            f._record_failure()
        assert f.state == "red"

        agg = NewsAggregator([f], "/tmp/test_seen_cb2.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 0
            assert f.fetch_called == 0  # never called

        asyncio.run(run())

    def test_yellow_fetcher_is_probed(self, monkeypatch):
        """YELLOW fetchers are probed when should_probe() is True."""
        f = _MockFetcher(items=[_make_item("recovered")])
        f._cooldown_period = 0.01
        f._probe_interval = 0.01

        # Trigger RED → wait → YELLOW
        for _ in range(5):
            f._record_failure()
        time.sleep(0.05)
        assert f.state == "yellow"
        assert f.should_probe()

        agg = NewsAggregator([f], "/tmp/test_seen_cb3.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 1  # probe succeeded
            assert f.fetch_called == 1
            assert f.state == "green"  # recovered

        asyncio.run(run())

    def test_yellow_fetcher_not_probed_within_interval(self, monkeypatch):
        """YELLOW fetcher not probed if within probe_interval."""
        f = _MockFetcher(items=[_make_item("x")])
        f._cooldown_period = 0.01
        f._probe_interval = 999  # very long interval

        for _ in range(5):
            f._record_failure()
        time.sleep(0.05)
        assert f.state == "yellow"
        # Reset probe timer so the interval hasn't elapsed
        f._last_probe_time = time.monotonic()
        assert not f.should_probe()  # interval not elapsed

        agg = NewsAggregator([f], "/tmp/test_seen_cb4.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 0
            assert f.fetch_called == 0

        asyncio.run(run())
