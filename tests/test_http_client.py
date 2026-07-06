"""Tests for shared httpx.AsyncClient singleton."""

import pytest
import asyncio
from stock_watcher.http_client import get_shared_client, close_shared_client


class TestHttpClient:
    """T02: Shared httpx.AsyncClient singleton."""

    def teardown_method(self):
        close_shared_client()

    def test_singleton_returns_same_instance(self):
        c1 = get_shared_client()
        c2 = get_shared_client()
        assert c1 is c2

    def test_close_and_recreate(self):
        c1 = get_shared_client()
        close_shared_client()
        c2 = get_shared_client()
        assert c1 is not c2  # new instance after reset

    @pytest.mark.asyncio
    async def test_client_is_functional(self):
        """Shared client can make real HTTP requests."""
        client = get_shared_client()
        # Use a well-known public endpoint for a quick smoke test
        resp = await client.get("https://httpbin.org/status/200")
        assert resp.status_code == 200
