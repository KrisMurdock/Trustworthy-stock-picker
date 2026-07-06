"""Tests for ProxyPool automatic proxy fetching and validation."""
import asyncio
import json
import os
import pytest
from unittest.mock import AsyncMock, patch, MagicMock
from pathlib import Path


# We'll test the module after it's created
class TestProxyEntry:
    """ProxyEntry dataclass."""

    def test_create_entry(self):
        from stock_watcher.proxy_pool import ProxyEntry
        entry = ProxyEntry(url="http://1.2.3.4:8080", score=0.8, latency=1.2)
        assert entry.url == "http://1.2.3.4:8080"
        assert entry.score == 0.8
        assert entry.latency == 1.2
        assert entry.fail_count == 0


class TestProxyPool:
    """ProxyPool lifecycle: refresh, validate, cache, mark dead."""

    def test_init_loads_cache(self, tmp_path):
        """ProxyPool loads proxies from cache file on init."""
        import time
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry
        cache = tmp_path / "proxy_cache.json"
        cache.write_text(json.dumps([
            {"url": "http://10.0.0.1:8080", "score": 0.9, "latency": 0.5,
             "last_check": time.monotonic(), "fail_count": 0}
        ]))

        pool = ProxyPool(cache_path=str(cache))
        assert pool.available_count == 1
        proxy = pool.get_proxy()
        assert proxy == "http://10.0.0.1:8080"

    def test_get_proxy_returns_none_when_empty(self):
        """get_proxy returns None when pool is empty."""
        from stock_watcher.proxy_pool import ProxyPool
        pool = ProxyPool(cache_path="/tmp/nonexistent_cache.json")
        assert pool.get_proxy() is None

    def test_get_proxies_returns_at_most_n(self, tmp_path):
        """get_proxies(n) returns at most n proxies, sorted by score."""
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry
        pool = ProxyPool(cache_path=str(tmp_path / "cache.json"))
        pool._proxies = [
            ProxyEntry(url=f"http://p{i}:8080", score=0.1 * i, latency=1.0)
            for i in range(1, 6)
        ]
        proxies = pool.get_proxies(3)
        assert len(proxies) == 3
        # Highest score first
        assert proxies[0] == "http://p5:8080"

    def test_mark_dead_removes_after_three_failures(self, tmp_path):
        """mark_dead removes proxy after 3 consecutive failures."""
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry
        pool = ProxyPool(cache_path=str(tmp_path / "cache.json"))
        pool._proxies = [ProxyEntry(url="http://bad:8080", score=1.0, latency=0.5)]
        assert pool.available_count == 1

        pool.mark_dead("http://bad:8080")
        assert pool.available_count == 1  # still there after 1

        pool.mark_dead("http://bad:8080")
        assert pool.available_count == 1  # still there after 2

        pool.mark_dead("http://bad:8080")
        assert pool.available_count == 0  # removed after 3

    def test_mark_slow_updates_latency_and_score(self, tmp_path):
        """mark_slow updates the proxy's latency and degrades score."""
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry
        pool = ProxyPool(cache_path=str(tmp_path / "cache.json"))
        pool._proxies = [ProxyEntry(url="http://slow:8080", score=1.0, latency=0.5)]
        pool.mark_slow("http://slow:8080", latency=5.0)
        entry = pool._proxies[0]
        assert entry.latency == 5.0
        assert entry.score < 1.0  # degraded

    def test_stats_returns_dict(self, tmp_path):
        """stats returns a dict with pool metrics."""
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry
        pool = ProxyPool(cache_path=str(tmp_path / "cache.json"))
        pool._proxies = [
            ProxyEntry(url=f"http://p{i}:8080", score=0.5, latency=1.0)
            for i in range(3)
        ]
        s = pool.stats
        assert s["total"] == 3
        assert s["available"] == 3

    @pytest.mark.asyncio
    async def test_refresh_with_mock_sources(self, tmp_path):
        """refresh() fetches from proxy sources, validates, and updates pool."""
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry

        pool = ProxyPool(cache_path=str(tmp_path / "cache.json"))

        # Mock the HTTP calls: source returns proxy list, validation succeeds
        with patch.object(pool, "_fetch_from_sources", new_callable=AsyncMock) as mock_fetch, \
             patch.object(pool, "_validate_one", new_callable=AsyncMock) as mock_validate:

            mock_fetch.return_value = [
                "http://a:8080", "http://b:8080", "http://c:8080"
            ]
            # First two valid, third times out
            async def validate_side_effect(proxy):
                if "c" in proxy:
                    return None  # invalid
                return 1.0 + hash(proxy) % 100 / 100  # some latency

            mock_validate.side_effect = validate_side_effect

            added = await pool.refresh()
            assert added == 2  # two valid
            assert pool.available_count == 2

    def test_save_cache_writes_json(self, tmp_path):
        """ProxyPool saves valid proxies to cache file."""
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry
        cache = tmp_path / "cache.json"
        pool = ProxyPool(cache_path=str(cache))
        pool._proxies = [
            ProxyEntry(url="http://x:8080", score=0.9, latency=0.3)
        ]
        pool._save_cache()
        assert cache.exists()
        data = json.loads(cache.read_text())
        assert len(data) == 1
        assert data[0]["url"] == "http://x:8080"
