"""新闻抓取器抽象基类."""

from __future__ import annotations

import asyncio
import hashlib
import logging
import time as _time
from abc import ABC, abstractmethod
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Any

from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)


class BaseNewsFetcher(ABC):
    """抓取器的抽象基类 — 每源一个子类."""

    source_name: str = ""
    source_priority: int = 2  # 1=高, 2=中, 3=低

    def __init__(self, enabled: bool = True) -> None:
        self.enabled = enabled
        self._consecutive_failures: int = 0
        self._fetch_timeout: float = 15  # 可被子类覆盖

    @abstractmethod
    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        """抓取最新新闻。失败时返回 [] 并记录日志."""
        ...

    async def _run_blocking(self, func, *args, timeout: float | None = None) -> Any:
        """在线程池中运行阻塞函数，带硬超时。

        使用 ThreadPoolExecutor.submit().result(timeout=N) 实现真正的超时中断。
        超时时线程被丢弃（最多泄漏 1 个线程/次），但保证控制权回到事件循环。
        """
        _to = timeout if timeout is not None else self._fetch_timeout
        loop = asyncio.get_running_loop()

        def _runner():
            return func(*args)

        fut = None
        pool = ThreadPoolExecutor(max_workers=1)
        try:
            fut = pool.submit(_runner)
            return await loop.run_in_executor(None, lambda: fut.result(timeout=_to))
        except Exception:
            if fut is not None:
                fut.cancel()
            raise
        finally:
            pool.shutdown(wait=False)

    @property
    def is_failing(self) -> bool:
        return self._consecutive_failures >= 5

    def _record_failure(self) -> None:
        self._consecutive_failures += 1
        if self._consecutive_failures >= 5:
            logger.warning(
                "%s: %d consecutive failures, consider disabling",
                self.source_name,
                self._consecutive_failures,
            )

    def _record_success(self) -> None:
        self._consecutive_failures = 0

    @staticmethod
    def _make_id(source: str, identifier: str) -> str:
        """生成确定性 news ID: sha1(source + identifier)[:16]."""
        h = hashlib.sha1(f"{source}:{identifier}".encode()).hexdigest()
        return h[:16]
