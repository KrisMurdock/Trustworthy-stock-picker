"""Shared httpx.AsyncClient singleton — connection pooling for all HTTP requests."""

from __future__ import annotations

import asyncio
import logging

import httpx

logger = logging.getLogger(__name__)

_client: httpx.AsyncClient | None = None


def get_shared_client() -> httpx.AsyncClient:
    """Return a module-level singleton AsyncClient.

    Creates the client on first call; subsequent calls return the same instance.
    Use ``close_shared_client()`` to reset (mainly for test cleanup).
    """
    global _client
    if _client is None:
        _client = httpx.AsyncClient(timeout=15.0)
    return _client


def close_shared_client() -> None:
    """Close and reset the shared client.

    Schedules ``aclose()`` on the running event loop.
    Safe to call in tests or at application shutdown.
    """
    global _client
    if _client is not None:
        try:
            loop = asyncio.get_running_loop()
            loop.create_task(_client.aclose())
        except RuntimeError:
            # No running event loop — close synchronously
            pass
        _client = None
