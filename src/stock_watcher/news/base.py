"""新闻抓取器抽象基类."""

from __future__ import annotations

import hashlib
import logging
from abc import ABC, abstractmethod
from dataclasses import dataclass

from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)


class BaseNewsFetcher(ABC):
    """抓取器的抽象基类 — 每源一个子类."""

    source_name: str = ""
    source_priority: int = 2  # 1=高, 2=中, 3=低

    def __init__(self, enabled: bool = True) -> None:
        self.enabled = enabled
        self._consecutive_failures: int = 0

    @abstractmethod
    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        """抓取最新新闻。失败时返回 [] 并记录日志."""
        ...

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
