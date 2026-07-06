"""Shared httpx.AsyncClient singleton — connection pooling for all HTTP requests."""

from __future__ import annotations

import asyncio
import logging

import httpx

logger = logging.getLogger(__name__)

_client: httpx.AsyncClient | None = None
_loop_id: int | None = None


def get_shared_client() -> httpx.AsyncClient:
    """Return a module-level singleton AsyncClient, recreated per event loop."""
    global _client, _loop_id
    try:
        loop = asyncio.get_running_loop()
        current_id = id(loop)
    except RuntimeError:
        current_id = 0

    if _client is None or _loop_id != current_id:
        _client = httpx.AsyncClient(timeout=15.0)
        _loop_id = current_id
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
