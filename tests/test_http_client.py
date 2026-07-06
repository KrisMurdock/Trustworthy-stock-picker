"""Tests for shared httpx.AsyncClient singleton."""

import pytest
import asyncio
from stock_watcher.http_client import get_shared_client


class TestHttpClient:
    """T02: Shared httpx.AsyncClient singleton."""

    def test_singleton_returns_same_instance(self):
        c1 = get_shared_client()
        c2 = get_shared_client()
        assert c1 is c2

    @pytest.mark.asyncio
    async def test_client_is_functional(self):
        """Shared client creates functional httpx client."""
        import httpx
        client = get_shared_client()
        assert isinstance(client, httpx.AsyncClient)
