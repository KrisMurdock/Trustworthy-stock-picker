"""Tests for direct→proxy fallback in BaseNewsFetcher."""
import asyncio
import os
import time
import pytest
from unittest.mock import AsyncMock, patch, MagicMock
from stock_watcher.news.base import BaseNewsFetcher


class _TestFetcher(BaseNewsFetcher):
    source_name = "test_fallback"
    async def fetch(self, watchlist_codes: list[str]) -> list:
        return []


class TestIsRetryable:
    """_is_retryable classifies errors correctly."""

    def test_timeout_is_retryable(self):
        assert BaseNewsFetcher._is_retryable(TimeoutError)

    def test_connection_error_is_retryable(self):
        assert BaseNewsFetcher._is_retryable(ConnectionError)

    def test_ssl_error_is_not_retryable(self):
        import ssl
        assert not BaseNewsFetcher._is_retryable(ssl.SSLError)

    def test_value_error_is_not_retryable(self):
        assert not BaseNewsFetcher._is_retryable(ValueError)


class TestFetchWithRetry:
    """_fetch_with_retry: direct → proxy fallback."""

    def test_direct_success_returns_result(self):
        """Direct connection succeeds → return result, no proxy used."""
        f = _TestFetcher()

        async def run():
            result = await f._fetch_with_retry(lambda x: x * 2, 21)
            assert result == 42

        asyncio.run(run())

    def test_direct_retryable_error_falls_back_to_proxy(self):
        """Direct fails with TimeoutError → proxy succeeds."""
        f = _TestFetcher()

        # Mock proxy pool
        mock_pool = MagicMock()
        mock_pool.available_count = 2
        mock_pool.get_proxies.return_value = ["http://proxy1:8080", "http://proxy2:8080"]
        f._proxy_pool = mock_pool

        call_count = [0]

        def unreliable_func():
            call_count[0] += 1
            if call_count[0] == 1:
                raise TimeoutError("direct timeout")
            return "proxy_result"

        async def run():
            result = await f._fetch_with_retry(unreliable_func)
            assert result == "proxy_result"
            assert call_count[0] == 2  # direct failed, proxy succeeded

        asyncio.run(run())

    def test_all_proxies_fail_raises_last_error(self):
        """All proxies fail → raises last error."""
        f = _TestFetcher()

        mock_pool = MagicMock()
        mock_pool.available_count = 2
        mock_pool.get_proxies.return_value = ["http://p1:8080", "http://p2:8080"]
        f._proxy_pool = mock_pool

        def always_fail():
            raise TimeoutError("timeout")

        async def run():
            with pytest.raises(TimeoutError, match="timeout"):
                await f._fetch_with_retry(always_fail)
            # Both proxies marked dead
            assert mock_pool.mark_dead.call_count == 2

        asyncio.run(run())

    def test_non_retryable_error_skips_proxy(self):
        """SSL errors skip proxy fallback → raise immediately."""
        f = _TestFetcher()
        import ssl

        mock_pool = MagicMock()
        mock_pool.available_count = 5
        f._proxy_pool = mock_pool

        def ssl_error_func():
            raise ssl.SSLError("connection reset")

        async def run():
            with pytest.raises(ssl.SSLError):
                await f._fetch_with_retry(ssl_error_func)
            # Proxy was never used
            mock_pool.get_proxies.assert_not_called()
            mock_pool.mark_dead.assert_not_called()

        asyncio.run(run())

    def test_no_proxy_pool_still_tries_direct(self):
        """Without proxy pool, only direct is attempted."""
        f = _TestFetcher()

        async def run():
            result = await f._fetch_with_retry(lambda: "ok")
            assert result == "ok"

        asyncio.run(run())

    def test_run_blocking_with_proxy_sets_env_vars(self):
        """_run_blocking_with_proxy sets HTTP_PROXY/HTTPS_PROXY in thread."""
        f = _TestFetcher()

        def check_env():
            return {
                "HTTP_PROXY": os.environ.get("HTTP_PROXY", ""),
                "HTTPS_PROXY": os.environ.get("HTTPS_PROXY", ""),
            }

        async def run():
            result = await f._run_blocking_with_proxy(
                check_env, proxy="http://test:9999", timeout=5
            )
            assert result["HTTP_PROXY"] == "http://test:9999"
            assert result["HTTPS_PROXY"] == "http://test:9999"

        asyncio.run(run())

        # Verify env vars are NOT leaked after call
        assert os.environ.get("HTTP_PROXY") != "http://test:9999"
