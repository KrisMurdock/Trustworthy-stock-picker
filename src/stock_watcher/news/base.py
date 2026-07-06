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
        # In YELLOW, a probe failure keeps us in YELLOW — reset the probe timer
        # and bump the last failure timestamp so failures don't decay out of
        # the window while probes are still failing.  Use a timestamp just
        # past the cooldown boundary so we stay YELLOW, not RED.
        if self.state == "yellow":
            self._last_probe_time = now
            if self._failure_history:
                self._failure_history[-1] = now - self._cooldown_period
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

    # ------------------------------------------------------------------
    # Proxy + retry (Phase 2)
    # ------------------------------------------------------------------

    _proxy_pool = None  # type: ignore  # injected by aggregator

    async def _run_blocking_with_proxy(
        self, func, *args, proxy: str, timeout: float | None = None, **kwargs
    ) -> Any:
        """在线程中运行阻塞函数，通过环境变量注入代理。

        akshare → requests → 自动读取 HTTP_PROXY / HTTPS_PROXY。
        在 _runner 闭包内 save/restore 环境变量，避免线程间污染。
        """
        _to = timeout if timeout is not None else self._fetch_timeout
        loop = asyncio.get_running_loop()

        # 解析代理地址
        proxy_url = proxy
        if "://" in proxy:
            proxy_url = proxy.split("://", 1)[1]

        def _runner():
            import os as _os
            # Save original
            _orig_http = _os.environ.get("HTTP_PROXY")
            _orig_https = _os.environ.get("HTTPS_PROXY")
            try:
                _os.environ["HTTP_PROXY"] = f"http://{proxy_url}"
                _os.environ["HTTPS_PROXY"] = f"http://{proxy_url}"
                return func(*args, **kwargs)
            finally:
                # Restore
                if _orig_http is not None:
                    _os.environ["HTTP_PROXY"] = _orig_http
                else:
                    _os.environ.pop("HTTP_PROXY", None)
                if _orig_https is not None:
                    _os.environ["HTTPS_PROXY"] = _orig_https
                else:
                    _os.environ.pop("HTTPS_PROXY", None)

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

    async def _fetch_with_retry(
        self, func, *args, timeout: float | None = None, **kwargs
    ) -> Any:
        """混合策略：直连优先 → 代理 fallback。

        1. 直连尝试（_run_blocking）
        2. 瞬时错误 → 最多尝试 3 个不同代理
        3. 永久错误（SSL等）→ 直接 raise
        4. 全部失败 → raise last error
        """
        _to = timeout if timeout is not None else self._fetch_timeout
        last_error: Exception | None = None

        # Step 1: 直连
        try:
            return await self._run_blocking(func, *args, timeout=_to, **kwargs)
        except Exception as e:
            if not self._is_retryable(type(e)):
                raise
            last_error = e

        # Step 2: 代理 fallback
        pool = getattr(self, '_proxy_pool', None)
        if pool is not None and pool.available_count > 0:
            for proxy in pool.get_proxies(3):
                try:
                    return await self._run_blocking_with_proxy(
                        func, *args, proxy=proxy,
                        timeout=max(_to, 20.0), **kwargs
                    )
                except Exception as e:
                    pool.mark_dead(proxy)
                    last_error = e
                    continue

        if last_error is not None:
            raise last_error
        raise RuntimeError("_fetch_with_retry: no error captured")

    @staticmethod
    def _is_retryable(error_type: type) -> bool:
        """判断错误是否应重试（瞬时错误）vs 快速失败（永久错误）。"""
        import ssl

        # Built-in retryable
        if issubclass(error_type, (TimeoutError, ConnectionError)):
            return True

        # SSL errors are NOT retryable — TCP-level block
        if issubclass(error_type, ssl.SSLError):
            return False

        # String-based matching for httpx errors (avoid hard import dependency)
        name = error_type.__name__ if hasattr(error_type, '__name__') else str(error_type)
        if name in ("TimeoutException", "ConnectError", "RemoteProtocolError",
                     "ReadTimeout", "WriteTimeout", "PoolTimeout"):
            return True
        if name in ("HTTPStatusError",):
            return False

        # Default: do not retry unknown errors
        return False

    @staticmethod
    def _make_id(source: str, identifier: str) -> str:
        """生成确定性 news ID: sha1(source + identifier)[:16]."""
        h = hashlib.sha1(f"{source}:{identifier}".encode()).hexdigest()
        return h[:16]
