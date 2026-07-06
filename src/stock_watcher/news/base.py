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
        self._fetch_timeout: float = 15  # 可被子类覆盖
        self._failure_history: list[float] = []  # 失败时间戳
        self._last_probe_time: float = 0.0
        # Circuit breaker parameters (overridable)
        self._failure_window: float = 300.0
        self._failure_threshold: int = 5
        self._cooldown_period: float = 300.0
        self._probe_interval: float = 60.0

    @abstractmethod
    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        """抓取最新新闻。失败时返回 [] 并记录日志."""
        ...

    async def _run_blocking(self, func, *args, timeout: float | None = None, **kwargs) -> Any:
        """在线程池中运行阻塞函数，带硬超时。

        使用 ThreadPoolExecutor.submit().result(timeout=N) 实现真正的超时中断。
        超时时线程被丢弃（最多泄漏 1 个线程/次），但保证控制权回到事件循环。
        """
        _to = timeout if timeout is not None else self._fetch_timeout
        loop = asyncio.get_running_loop()

        def _runner():
            return func(*args, **kwargs)

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

    # ------------------------------------------------------------------
    # Circuit breaker (3-state: green / red / yellow)
    # ------------------------------------------------------------------

    @property
    def state(self) -> str:
        """返回当前熔断状态: "green" | "red" | "yellow"."""
        now = _time.monotonic()
        self._prune_old_failures(now)
        recent = len(self._failure_history)

        if recent < self._failure_threshold:
            return "green"

        # latest failure time
        last_failure = self._failure_history[-1]
        if now - last_failure < self._cooldown_period:
            return "red"
        return "yellow"

    @property
    def is_failing(self) -> bool:
        """True = 熔断中（RED），聚合器完全跳过."""
        return self.state == "red"

    def should_probe(self) -> bool:
        """True = 半开状态（YELLOW）且距上次探测超过 probe_interval."""
        if self.state != "yellow":
            return False
        now = _time.monotonic()
        return (now - self._last_probe_time) >= self._probe_interval

    @property
    def cooldown_remaining(self) -> float:
        """剩余冷却秒数（仅在 RED 状态有意义）."""
        if self.state != "red":
            return 0.0
        now = _time.monotonic()
        if not self._failure_history:
            return 0.0
        last_failure = self._failure_history[-1]
        remaining = self._cooldown_period - (now - last_failure)
        return max(0.0, remaining)

    def _record_failure(self) -> None:
        """记录一次失败."""
        now = _time.monotonic()
        # In YELLOW, a probe failure keeps us in YELLOW — just reset the probe timer.
        if self.state == "yellow":
            self._last_probe_time = now
            return
        self._failure_history.append(now)
        self._prune_old_failures(now)
        recent = len(self._failure_history)
        if recent >= self._failure_threshold and self.state == "red":
            logger.warning(
                "%s: circuit breaker OPEN (%d failures in %.0fs, cooldown %.0fs)",
                self.source_name, recent, self._failure_window, self.cooldown_remaining,
            )

    def _record_success(self) -> None:
        """记录一次成功 — 重置熔断器."""
        self._failure_history.clear()
        self._last_probe_time = 0.0

    def _prune_old_failures(self, now: float) -> None:
        """移除窗口外的旧失败记录."""
        cutoff = now - self._failure_window
        while self._failure_history and self._failure_history[0] < cutoff:
            self._failure_history.pop(0)

    @staticmethod
    def _make_id(source: str, identifier: str) -> str:
        """生成确定性 news ID: sha1(source + identifier)[:16]."""
        h = hashlib.sha1(f"{source}:{identifier}".encode()).hexdigest()
        return h[:16]
