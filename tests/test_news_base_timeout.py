"""Tests for BaseNewsFetcher._run_blocking hard timeout."""
import asyncio
import time
import pytest
from stock_watcher.news.base import BaseNewsFetcher


# Concrete subclass for testing
class _TestFetcher(BaseNewsFetcher):
    source_name = "test"
    async def fetch(self, watchlist_codes: list[str]) -> list:
        return []


class TestRunBlocking:
    """Tests for _run_blocking hard timeout."""

    def test_returns_result_on_success(self):
        """_run_blocking returns the function's return value."""
        fetcher = _TestFetcher()

        async def run():
            result = await fetcher._run_blocking(lambda x: x * 2, 21)
            assert result == 42

        asyncio.run(run())

    def test_raises_on_function_error(self):
        """_run_blocking propagates exceptions from the wrapped function."""
        fetcher = _TestFetcher()

        async def run():
            with pytest.raises(ValueError, match="boom"):
                await fetcher._run_blocking(lambda: (_ for _ in ()).throw(ValueError("boom")))

        asyncio.run(run())

    def test_timeout_fires(self):
        """_run_blocking raises TimeoutError when function takes too long."""
        fetcher = _TestFetcher()
        fetcher._fetch_timeout = 0.5  # short timeout for test

        async def run():
            t0 = time.monotonic()
            with pytest.raises(TimeoutError):
                await fetcher._run_blocking(lambda: time.sleep(10), timeout=0.5)
            elapsed = time.monotonic() - t0
            assert elapsed < 2.0  # should fail fast, not wait 10s

        asyncio.run(run())

    def test_uses_fetch_timeout_default(self):
        """When timeout=None, self._fetch_timeout is used as the timeout."""
        fetcher = _TestFetcher()
        fetcher._fetch_timeout = 0.3

        async def run():
            t0 = time.monotonic()
            with pytest.raises(TimeoutError):
                await fetcher._run_blocking(lambda: time.sleep(10))
            elapsed = time.monotonic() - t0
            assert elapsed < 2.0  # should fail fast, not wait 10s

        asyncio.run(run())

    def test_timeout_returns_control_to_event_loop(self):
        """After timeout, the event loop is not blocked."""
        fetcher = _TestFetcher()

        async def run():
            # Start a blocking task that will timeout
            async def blocking():
                await fetcher._run_blocking(lambda: time.sleep(60), timeout=0.3)

            # Start a fast task
            async def fast():
                await asyncio.sleep(0.5)
                return "fast_done"

            results = await asyncio.gather(
                blocking(), fast(), return_exceptions=True
            )
            # blocking() should have timed out
            assert isinstance(results[0], TimeoutError)
            # fast() should have completed
            assert results[1] == "fast_done"

        asyncio.run(run())
