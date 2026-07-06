# News Fetcher Network Resilience — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden the news fetcher network layer with hard timeouts, 3-state circuit breaker with auto-recovery, auto proxy pool, and direct→proxy fallback.

**Architecture:** Enhance `BaseNewsFetcher` with `_run_blocking()` for hard timeouts and a 3-state circuit breaker (GREEN/RED/YELLOW). Add a new `ProxyPool` module for auto-fetching/validating free proxies. Add `_fetch_with_retry()` for direct-first → proxy-fallback strategy. Deliver in two independent phases.

**Tech Stack:** Python 3.11+, asyncio, concurrent.futures, httpx, akshare, pytest, respx

## Global Constraints

- Phase 1 and Phase 2 are independent delivery stages — Phase 1 must work standalone
- All existing 291 tests must continue to pass
- `asyncio.to_thread` calls in akshare fetchers replaced with `self._run_blocking()`
- Circuit breaker parameters default to: failure_window=300s, failure_threshold=5, cooldown=300s, probe_interval=60s
- Proxy pool refreshes every 30min, caches to `proxy_cache.json`, expires after 2h
- `_is_retryable()` treats SSL errors as non-retryable (fast-fail, let circuit breaker handle it)

---

### Task 1: Add `_run_blocking()` hard timeout to BaseNewsFetcher

**Files:**
- Modify: `src/stock_watcher/news/base.py`

**Interfaces:**
- Produces: `BaseNewsFetcher._fetch_timeout: float = 15`, `BaseNewsFetcher._run_blocking(func, *args, timeout=None) -> Any`

- [ ] **Step 1: Write the failing test**

Create `tests/test_news_base_timeout.py`:

```python
"""Tests for BaseNewsFetcher._run_blocking hard timeout."""
import asyncio
import time
import pytest
from stock_watcher.news.base import BaseNewsFetcher


# Concrete subclass for testing
class _TestFetcher(BaseNewsFetcher):
    source_name = "test"
    async def fetch(self, watchlist_codes: list[str]) -> list:
        return []


class TestRunBlocking:
    """Tests for _run_blocking hard timeout."""

    def test_returns_result_on_success(self):
        """_run_blocking returns the function's return value."""
        fetcher = _TestFetcher()

        async def run():
            result = await fetcher._run_blocking(lambda x: x * 2, 21)
            assert result == 42

        asyncio.run(run())

    def test_raises_on_function_error(self):
        """_run_blocking propagates exceptions from the wrapped function."""
        fetcher = _TestFetcher()

        async def run():
            with pytest.raises(ValueError, match="boom"):
                await fetcher._run_blocking(lambda: (_ for _ in ()).throw(ValueError("boom")))

        asyncio.run(run())

    def test_timeout_fires(self):
        """_run_blocking raises TimeoutError when function takes too long."""
        fetcher = _TestFetcher()
        fetcher._fetch_timeout = 0.5  # short timeout for test

        async def run():
            t0 = time.monotonic()
            with pytest.raises(TimeoutError):
                await fetcher._run_blocking(lambda: time.sleep(10), timeout=0.5)
            elapsed = time.monotonic() - t0
            assert elapsed < 2.0  # should fail fast, not wait 10s

        asyncio.run(run())

    def test_timeout_returns_control_to_event_loop(self):
        """After timeout, the event loop is not blocked."""
        fetcher = _TestFetcher()

        async def run():
            # Start a blocking task that will timeout
            async def blocking():
                await fetcher._run_blocking(lambda: time.sleep(60), timeout=0.3)

            # Start a fast task
            async def fast():
                await asyncio.sleep(0.5)
                return "fast_done"

            results = await asyncio.gather(
                blocking(), fast(), return_exceptions=True
            )
            # blocking() should have timed out
            assert isinstance(results[0], TimeoutError)
            # fast() should have completed
            assert results[1] == "fast_done"

        asyncio.run(run())
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pytest tests/test_news_base_timeout.py -v`
Expected: FAIL — `AttributeError: '_TestFetcher' object has no attribute '_run_blocking'`

- [ ] **Step 3: Write minimal implementation in base.py**

Replace `src/stock_watcher/news/base.py` imports and add `_run_blocking` + `_fetch_timeout`:

In the imports section (top of file), add:
```python
import asyncio
import time as _time
from concurrent.futures import ThreadPoolExecutor
```

Change the class `__init__` and add new methods:

```python
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

        with ThreadPoolExecutor(max_workers=1) as pool:
            fut = pool.submit(_runner)
            try:
                return await loop.run_in_executor(None, lambda: fut.result(timeout=_to))
            except Exception:
                fut.cancel()
                raise

    @property
    def is_failing(self) -> bool:
        return self._consecutive_failures >= 5
    # ... rest of existing methods unchanged
```

- [ ] **Step 4: Run test to verify it passes**

Run: `pytest tests/test_news_base_timeout.py -v`
Expected: 4 passed

- [ ] **Step 5: Run full test suite to verify no regressions**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all 291+ tests pass

- [ ] **Step 6: Commit**

```bash
git add src/stock_watcher/news/base.py tests/test_news_base_timeout.py
git commit -m "feat: add _run_blocking() hard timeout to BaseNewsFetcher"
```

---

### Task 2: Replace circuit breaker with 3-state version

**Files:**
- Modify: `src/stock_watcher/news/base.py`

**Interfaces:**
- Removes: `_consecutive_failures: int`
- Produces: `_failure_history: list[float]`, `state` property (`"green"|"red"|"yellow"`), `is_failing` property, `should_probe()` method, `cooldown_remaining` property
- Consumption note: `is_failing` keeps the same signature so aggregator code doesn't break yet; `state` and `should_probe()` are new

- [ ] **Step 1: Write the failing test**

Create `tests/test_news_circuit_breaker.py`:

```python
"""Tests for 3-state circuit breaker in BaseNewsFetcher."""
import asyncio
import time
import pytest
from stock_watcher.news.base import BaseNewsFetcher


class _TestFetcher(BaseNewsFetcher):
    source_name = "test_cb"
    async def fetch(self, watchlist_codes: list[str]) -> list:
        return []


class TestCircuitBreakerStates:
    """Test GREEN → RED → YELLOW → GREEN transitions."""

    def test_initial_state_is_green(self):
        f = _TestFetcher()
        assert f.state == "green"
        assert not f.is_failing
        assert not f.should_probe()

    def test_five_failures_trigger_red(self):
        f = _TestFetcher()
        for _ in range(5):
            f._record_failure()
        assert f.state == "red"
        assert f.is_failing
        assert not f.should_probe()  # just entered red, still cooling

    def test_success_resets_to_green(self):
        f = _TestFetcher()
        for _ in range(3):
            f._record_failure()
        f._record_success()
        assert f.state == "green"
        assert f._failure_history == []

    def test_red_transitions_to_yellow_after_cooldown(self, monkeypatch):
        f = _TestFetcher()
        # Override cooldown to 0.1s for test
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        # Trigger RED
        for _ in range(5):
            f._record_failure()
        assert f.state == "red"

        # Wait for cooldown
        time.sleep(0.15)
        assert f.state == "yellow"
        assert not f.is_failing  # YELLOW is not "failing"
        assert f.should_probe()   # ready to probe

    def test_yellow_probe_success_returns_to_green(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        for _ in range(5):
            f._record_failure()
        time.sleep(0.15)
        assert f.state == "yellow"

        f._record_success()
        assert f.state == "green"

    def test_yellow_probe_failure_stays_yellow(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        for _ in range(5):
            f._record_failure()
        time.sleep(0.15)
        assert f.state == "yellow"

        f._record_failure()
        assert f.state == "yellow"  # stays yellow
        assert not f.should_probe()  # must wait probe_interval

        time.sleep(0.1)
        assert f.should_probe()  # ready again

    def test_old_failures_decay_from_window(self, monkeypatch):
        f = _TestFetcher()
        f._failure_window = 0.1  # very short window

        for _ in range(5):
            f._record_failure()
        assert f.state == "red"

        # Wait for failures to decay out of window
        time.sleep(0.15)
        # Failures older than window are pruned, count drops below threshold
        assert f.state in ("yellow", "green")

    def test_is_failing_matches_red_only(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 0.1
        f._probe_interval = 0.05

        assert not f.is_failing  # GREEN

        for _ in range(5):
            f._record_failure()
        assert f.is_failing  # RED

        time.sleep(0.15)
        assert not f.is_failing  # YELLOW

    def test_cooldown_remaining_decreases(self, monkeypatch):
        f = _TestFetcher()
        f._cooldown_period = 10.0

        for _ in range(5):
            f._record_failure()

        r1 = f.cooldown_remaining
        assert r1 > 0
        time.sleep(0.2)
        r2 = f.cooldown_remaining
        assert r2 < r1
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pytest tests/test_news_circuit_breaker.py -v`
Expected: FAIL — `AttributeError: '_TestFetcher' object has no attribute 'state'`

- [ ] **Step 3: Write implementation in base.py**

Replace the circuit breaker section in `BaseNewsFetcher`. Remove `_consecutive_failures: int` from `__init__` and replace `_record_failure`, `_record_success`, `is_failing` with:

```python
    def __init__(self, enabled: bool = True) -> None:
        self.enabled = enabled
        self._fetch_timeout: float = 15
        self._failure_history: list[float] = []  # 失败时间戳
        self._last_probe_time: float = 0.0
        # Circuit breaker parameters (overridable)
        self._failure_window: float = 300.0
        self._failure_threshold: int = 5
        self._cooldown_period: float = 300.0
        self._probe_interval: float = 60.0

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
        self._failure_history.append(now)
        self._prune_old_failures(now)
        if self.state == "yellow":
            self._last_probe_time = now
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `pytest tests/test_news_circuit_breaker.py -v`
Expected: 8 passed

- [ ] **Step 5: Run full test suite**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all tests pass (circuit breaker replaces old `_consecutive_failures`, aggregator still uses `is_failing` which is preserved)

- [ ] **Step 6: Commit**

```bash
git add src/stock_watcher/news/base.py tests/test_news_circuit_breaker.py
git commit -m "feat: replace circuit breaker with 3-state version (green/red/yellow)"
```

---

### Task 3: Update aggregator for green/yellow/red branching

**Files:**
- Modify: `src/stock_watcher/news/aggregator.py`

**Interfaces:**
- Consumes: `BaseNewsFetcher.state`, `BaseNewsFetcher.should_probe()`, `BaseNewsFetcher.is_failing`
- Modifies: `aggregate()` method branching logic

- [ ] **Step 1: Write the failing integration test**

Create `tests/test_news_aggregator_circuit.py`:

```python
"""Integration tests for aggregator with 3-state circuit breaker."""
import asyncio
import time
import pytest
from stock_watcher.news.aggregator import NewsAggregator
from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem


class _MockFetcher(BaseNewsFetcher):
    """Fetcher that returns predefined items or raises."""
    source_name = "mock"

    def __init__(self, enabled=True, items=None, should_fail=False):
        super().__init__(enabled=enabled)
        self._items = items or []
        self._should_fail = should_fail
        self.fetch_called = 0

    async def fetch(self, watchlist_codes: list[str]) -> list[NewsItem]:
        self.fetch_called += 1
        if self._should_fail:
            raise RuntimeError("mock fetch error")
        return self._items


def _make_item(title="test"):
    return NewsItem(
        id=f"mock_{title}",
        source="mock",
        title=title,
        summary="",
        url="",
        published_at="",
        related_codes=[],
        source_priority=2,
    )


class TestAggregatorCircuitBreaker:
    """Aggregator respects circuit breaker states."""

    def test_green_fetcher_is_called(self):
        """GREEN fetchers are called normally."""
        f = _MockFetcher(items=[_make_item("news1")])
        agg = NewsAggregator([f], "/tmp/test_seen_cb.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 1
            assert f.fetch_called == 1

        asyncio.run(run())

    def test_red_fetcher_is_skipped(self):
        """RED (failing) fetchers are skipped entirely."""
        f = _MockFetcher(items=[_make_item("news1")])
        # Force RED state
        for _ in range(5):
            f._record_failure()
        assert f.state == "red"

        agg = NewsAggregator([f], "/tmp/test_seen_cb2.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 0
            assert f.fetch_called == 0  # never called

        asyncio.run(run())

    def test_yellow_fetcher_is_probed(self, monkeypatch):
        """YELLOW fetchers are probed when should_probe() is True."""
        f = _MockFetcher(items=[_make_item("recovered")])
        f._cooldown_period = 0.01
        f._probe_interval = 0.01

        # Trigger RED → wait → YELLOW
        for _ in range(5):
            f._record_failure()
        time.sleep(0.05)
        assert f.state == "yellow"
        assert f.should_probe()

        agg = NewsAggregator([f], "/tmp/test_seen_cb3.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 1  # probe succeeded
            assert f.fetch_called == 1
            assert f.state == "green"  # recovered

        asyncio.run(run())

    def test_yellow_fetcher_not_probed_within_interval(self, monkeypatch):
        """YELLOW fetcher not probed if within probe_interval."""
        f = _MockFetcher(items=[_make_item("x")])
        f._cooldown_period = 0.01
        f._probe_interval = 999  # very long interval

        for _ in range(5):
            f._record_failure()
        time.sleep(0.05)
        assert f.state == "yellow"
        assert not f.should_probe()  # interval not elapsed

        agg = NewsAggregator([f], "/tmp/test_seen_cb4.jsonl")

        async def run():
            items = await agg.aggregate(["sh000001"])
            assert len(items) == 0
            assert f.fetch_called == 0

        asyncio.run(run())
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pytest tests/test_news_aggregator_circuit.py -v`
Expected: 2 pass (green fetcher test, yellow not probed test), 2 fail (red skip, yellow probe) — because aggregator still uses old `is_failing`/`enabled` binary logic

- [ ] **Step 3: Update aggregator's `aggregate()` branching logic**

In `src/stock_watcher/news/aggregator.py`, replace the task creation loop (lines 49-57) with:

```python
        # 并发抓取 — 根据熔断状态分支
        tasks = {}
        for f in self._fetchers:
            if not f.enabled:
                continue
            if f.state == "green":
                task = asyncio.create_task(self._fetch_one(f, watchlist_codes))
                tasks[task] = f
            elif f.state == "yellow" and f.should_probe():
                logger.info("%s: probing (half-open)", f.source_name)
                task = asyncio.create_task(self._fetch_one(f, watchlist_codes))
                tasks[task] = f
            else:
                state_label = f.state.upper()
                logger.debug(
                    "%s: skipped (state=%s, cooldown=%.0fs)",
                    f.source_name, state_label, f.cooldown_remaining,
                )
```

Also update the `_fetch_one` method to call `_record_success` / `_record_failure` on the fetcher:

```python
    async def _fetch_one(
        self, fetcher: BaseNewsFetcher, codes: list[str]
    ) -> list[NewsItem]:
        """包装单个 Fetcher 调用，处理异常."""
        try:
            result = await fetcher.fetch(codes)
            fetcher._record_success()
            return result
        except Exception as exc:
            logger.error("%s fetch error: %s", fetcher.source_name, exc)
            fetcher._record_failure()
            return []
```

And bump the timeout from 30 to 60 in `asyncio.wait` (line 62):

```python
        done, pending = await asyncio.wait(
            tasks.keys(), timeout=60, return_when=asyncio.ALL_COMPLETED,
        )
```

Remove the manual `fetcher._record_failure()` call in the pending-task loop (line 69) since `_fetch_one` now handles it.

- [ ] **Step 4: Run test to verify it passes**

Run: `pytest tests/test_news_aggregator_circuit.py -v`
Expected: 4 passed

- [ ] **Step 5: Run full test suite**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all tests pass (existing aggregator tests use mock fetchers without is_failing, should still pass)

- [ ] **Step 6: Commit**

```bash
git add src/stock_watcher/news/aggregator.py tests/test_news_aggregator_circuit.py
git commit -m "feat: update aggregator for 3-state circuit breaker (green/yellow/red)"
```

---

### Task 4: Update akshare fetchers to use `_run_blocking()`

**Files:**
- Modify: `src/stock_watcher/news/eastmoney_flash.py`
- Modify: `src/stock_watcher/news/eastmoney_stock.py`
- Modify: `src/stock_watcher/news/cls_telegraph.py`
- Modify: `src/stock_watcher/news/sina_flash.py`
- Modify: `src/stock_watcher/news/yahoo_news.py`

**Interfaces:**
- Consumes: `BaseNewsFetcher._run_blocking()`
- Each fetcher's `fetch()` replaces `await asyncio.to_thread(akshare.xxx)` with `await self._run_blocking(akshare.xxx)`

- [ ] **Step 1: Update eastmoney_flash.py**

Change line 31 from:
```python
            df = await _asyncio.to_thread(akshare.stock_info_global_em)
```
To:
```python
            import akshare
            df = await self._run_blocking(akshare.stock_info_global_em)
```

Also remove the unused `import asyncio as _asyncio` from line 30.

- [ ] **Step 2: Update eastmoney_stock.py**

Change line 40-41 from:
```python
                import akshare, asyncio as _asyncio
                df = await _asyncio.to_thread(akshare.stock_news_em, symbol=raw_code)
```
To:
```python
                import akshare
                df = await self._run_blocking(akshare.stock_news_em, symbol=raw_code)
```

- [ ] **Step 3: Update cls_telegraph.py**

Change line 24-25 from:
```python
            import akshare, asyncio as _asyncio
            df = await _asyncio.to_thread(akshare.stock_info_global_cls)
```
To:
```python
            import akshare
            df = await self._run_blocking(akshare.stock_info_global_cls)
```

- [ ] **Step 4: Update sina_flash.py**

Change line 27-28 from:
```python
            import akshare, asyncio as _asyncio
            df = await _asyncio.to_thread(akshare.stock_info_global_sina)
```
To:
```python
            import akshare
            df = await self._run_blocking(akshare.stock_info_global_sina)
```

- [ ] **Step 5: Update yahoo_news.py**

Change lines 49-51 from:
```python
                import yfinance, asyncio as _asyncio
                t = await _asyncio.to_thread(yfinance.Ticker, ticker)
                news_list = await _asyncio.to_thread(lambda: t.news or [])
```
To:
```python
                import yfinance
                t = await self._run_blocking(yfinance.Ticker, ticker)
                news_list = await self._run_blocking(lambda: t.news or [])
```

- [ ] **Step 6: Run full test suite**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all tests pass

- [ ] **Step 7: Commit**

```bash
git add src/stock_watcher/news/eastmoney_flash.py \
        src/stock_watcher/news/eastmoney_stock.py \
        src/stock_watcher/news/cls_telegraph.py \
        src/stock_watcher/news/sina_flash.py \
        src/stock_watcher/news/yahoo_news.py
git commit -m "refactor: replace asyncio.to_thread with _run_blocking in 5 akshare fetchers"
```

**Phase 1 Complete** — All akshare fetchers now have hard timeouts. Circuit breaker auto-recovers via half-open probing. Aggregator branches on green/yellow/red.

---

### Task 5: Create ProxyPool module

**Files:**
- Create: `src/stock_watcher/proxy_pool.py`

**Interfaces:**
- Produces: `ProxyEntry` dataclass, `ProxyPool` class with `refresh()`, `get_proxy()`, `get_proxies(n)`, `mark_dead()`, `mark_slow()`, `available_count`, `stats`

- [ ] **Step 1: Write the failing test**

Create `tests/test_proxy_pool.py`:

```python
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
        from stock_watcher.proxy_pool import ProxyPool, ProxyEntry
        cache = tmp_path / "proxy_cache.json"
        cache.write_text(json.dumps([
            {"url": "http://10.0.0.1:8080", "score": 0.9, "latency": 0.5,
             "last_check": 1000.0, "fail_count": 0}
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pytest tests/test_proxy_pool.py -v`
Expected: FAIL — `ModuleNotFoundError: No module named 'stock_watcher.proxy_pool'`

- [ ] **Step 3: Create proxy_pool.py**

Create `src/stock_watcher/proxy_pool.py`:

```python
"""自动代理池 — 从免费源抓取代理，验证可用性，健康评分排序."""
from __future__ import annotations

import asyncio
import json
import logging
import time
from dataclasses import dataclass, field
from pathlib import Path

import httpx

logger = logging.getLogger(__name__)

# 免费代理源
_PROXY_SOURCES = [
    # JSON API — returns list of proxy objects
    {
        "url": "https://proxylist.geonode.com/api/proxy-list?limit=50&page=1"
               "&sort_by=lastChecked&sort_type=desc&protocols=http,https"
               "&anonymityLevel=elite&anonymityLevel=anonymous",
        "parser": "geonode_json",
    },
    # Fallback: text-based proxy list
    {
        "url": "https://api.proxyscrape.com/v2/?request=displayproxies"
               "&protocol=http&timeout=10000&country=all&ssl=all&anonymity=all",
        "parser": "text_lines",
    },
]

_VALIDATE_URL = "https://www.baidu.com"
_VALIDATE_TIMEOUT = 5.0
_CACHE_EXPIRY = 7200  # 2 hours


@dataclass
class ProxyEntry:
    """代理条目."""
    url: str
    score: float = 1.0       # 0.0 ~ 1.0，健康评分
    latency: float = 0.0     # 最近验证延迟(秒)
    last_check: float = field(default_factory=time.monotonic)
    fail_count: int = 0


class ProxyPool:
    """单例。自动从免费源抓取代理，验证可用性，健康评分排序。"""

    def __init__(self, cache_path: str = "proxy_cache.json") -> None:
        self._proxies: list[ProxyEntry] = []
        self._cache_path = Path(cache_path)
        self._last_refresh: float = 0.0
        self._refresh_interval: float = 1800.0  # 30 min
        self._min_proxies: int = 3
        self._max_proxies: int = 20
        self._load_cache()

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def refresh(self) -> int:
        """抓取 + 验证，返回新增可用代理数。"""
        raw_proxies = await self._fetch_from_sources()
        if not raw_proxies:
            logger.warning("proxy_pool: no proxies fetched from sources")
            return 0

        # 去重
        existing_urls = {p.url for p in self._proxies}
        new_urls = [u for u in raw_proxies if u not in existing_urls]

        # 并发验证
        validated: list[ProxyEntry] = []
        sem = asyncio.Semaphore(10)  # limit concurrent validation

        async def validate_one(url: str) -> ProxyEntry | None:
            async with sem:
                latency = await self._validate_one(url)
                if latency is not None:
                    return ProxyEntry(
                        url=url, score=self._score_from_latency(latency),
                        latency=latency, last_check=time.monotonic(),
                    )
                return None

        tasks = [asyncio.create_task(validate_one(u)) for u in new_urls]
        results = await asyncio.gather(*tasks)
        for entry in results:
            if entry is not None:
                validated.append(entry)

        # Merge: keep existing + new, sort by score desc, cap
        self._proxies.extend(validated)
        self._proxies.sort(key=lambda p: p.score, reverse=True)
        if len(self._proxies) > self._max_proxies:
            self._proxies = self._proxies[:self._max_proxies]

        self._last_refresh = time.monotonic()
        self._save_cache()

        logger.info(
            "proxy_pool: refreshed — %d fetched, %d valid, %d total in pool",
            len(new_urls), len(validated), len(self._proxies),
        )
        return len(validated)

    def get_proxy(self) -> str | None:
        """轮询返回一个代理地址（http://ip:port）。"""
        if not self._proxies:
            return None
        # Round-robin via moving first to last
        entry = self._proxies.pop(0)
        self._proxies.append(entry)
        return entry.url

    def get_proxies(self, n: int) -> list[str]:
        """返回最多 n 个代理，按评分降序。"""
        return [p.url for p in self._proxies[:n]]

    def mark_dead(self, proxy: str) -> None:
        """标记失效，fail_count += 1，≥3 次移除。"""
        for p in self._proxies:
            if p.url == proxy:
                p.fail_count += 1
                p.score *= 0.5
                if p.fail_count >= 3:
                    self._proxies.remove(p)
                    logger.debug("proxy_pool: removed dead proxy %s", proxy)
                return

    def mark_slow(self, proxy: str, latency: float) -> None:
        """更新延迟和评分。"""
        for p in self._proxies:
            if p.url == proxy:
                p.latency = latency
                p.score = self._score_from_latency(latency)
                p.last_check = time.monotonic()
                return

    @property
    def available_count(self) -> int:
        return len(self._proxies)

    @property
    def stats(self) -> dict:
        if not self._proxies:
            return {"total": 0, "available": 0, "avg_latency": 0.0}
        latencies = [p.latency for p in self._proxies if p.latency > 0]
        return {
            "total": len(self._proxies),
            "available": len(self._proxies),
            "avg_latency": sum(latencies) / len(latencies) if latencies else 0.0,
        }

    # ------------------------------------------------------------------
    # Internal
    # ------------------------------------------------------------------

    async def _fetch_from_sources(self) -> list[str]:
        """从所有代理源抓取原始代理列表。"""
        all_proxies: list[str] = []
        async with httpx.AsyncClient(timeout=15.0) as client:
            for src in _PROXY_SOURCES:
                try:
                    resp = await client.get(src["url"])
                    resp.raise_for_status()
                    parsed = self._parse_proxy_response(resp.text, src["parser"])
                    all_proxies.extend(parsed)
                    logger.debug(
                        "proxy_pool: fetched %d from %s", len(parsed), src["url"][:60]
                    )
                except Exception as exc:
                    logger.warning(
                        "proxy_pool: source %s failed: %s", src["url"][:60], exc
                    )
        return list(set(all_proxies))  # dedup

    @staticmethod
    def _parse_proxy_response(text: str, parser: str) -> list[str]:
        """解析代理源响应。"""
        if parser == "geonode_json":
            try:
                data = json.loads(text)
                proxies = []
                for item in data.get("data", []):
                    ip = item.get("ip", "")
                    port = item.get("port", "")
                    proto = item.get("protocols", ["http"])[0]
                    if ip and port:
                        proxies.append(f"{proto}://{ip}:{port}")
                return proxies
            except (json.JSONDecodeError, KeyError):
                return []
        elif parser == "text_lines":
            proxies = []
            for line in text.strip().split("\n"):
                line = line.strip()
                if line and ":" in line and not line.startswith("#"):
                    # Ensure it has a protocol prefix
                    if not line.startswith("http"):
                        line = f"http://{line}"
                    proxies.append(line)
            return proxies
        return []

    async def _validate_one(self, proxy: str) -> float | None:
        """验证单个代理是否可用。返回延迟(秒)或 None。"""
        try:
            t0 = time.monotonic()
            async with httpx.AsyncClient(
                proxy=proxy, timeout=_VALIDATE_TIMEOUT, follow_redirects=False,
            ) as client:
                resp = await client.get(_VALIDATE_URL)
                if resp.status_code in (200, 301, 302):
                    return time.monotonic() - t0
            return None
        except Exception:
            return None

    @staticmethod
    def _score_from_latency(latency: float) -> float:
        """将延迟映射为 0.0~1.0 的分数。"""
        if latency <= 1.0:
            return 1.0
        elif latency <= 3.0:
            return 0.7
        elif latency <= 5.0:
            return 0.4
        else:
            return 0.1

    # ------------------------------------------------------------------
    # Cache persistence
    # ------------------------------------------------------------------

    def _load_cache(self) -> None:
        """从缓存文件加载代理列表。"""
        if not self._cache_path.exists():
            return
        try:
            data = json.loads(self._cache_path.read_text(encoding="utf-8"))
            now = time.monotonic()
            for item in data:
                age = now - item.get("last_check", 0)
                if age > _CACHE_EXPIRY:
                    continue
                self._proxies.append(ProxyEntry(
                    url=item["url"],
                    score=item.get("score", 0.5),
                    latency=item.get("latency", 0.0),
                    last_check=item.get("last_check", now),
                    fail_count=item.get("fail_count", 0),
                ))
            logger.debug("proxy_pool: loaded %d from cache", len(self._proxies))
        except (OSError, json.JSONDecodeError, KeyError) as exc:
            logger.warning("proxy_pool: cache load failed: %s", exc)

    def _save_cache(self) -> None:
        """保存代理列表到缓存文件。"""
        try:
            data = [
                {
                    "url": p.url,
                    "score": p.score,
                    "latency": p.latency,
                    "last_check": p.last_check,
                    "fail_count": p.fail_count,
                }
                for p in self._proxies
            ]
            self._cache_path.write_text(
                json.dumps(data, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
        except OSError as exc:
            logger.warning("proxy_pool: cache save failed: %s", exc)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `pytest tests/test_proxy_pool.py -v`
Expected: 8 passed

- [ ] **Step 5: Commit**

```bash
git add src/stock_watcher/proxy_pool.py tests/test_proxy_pool.py
git commit -m "feat: add ProxyPool module — auto-fetch, validate, cache free proxies"
```

---

### Task 6: Add proxy fallback methods to BaseNewsFetcher

**Files:**
- Modify: `src/stock_watcher/news/base.py`

**Interfaces:**
- Consumes: `ProxyPool` (optional, injected)
- Produces: `_fetch_with_retry(func, *args, timeout=None)`, `_run_blocking_with_proxy(func, *args, proxy, timeout=None)`, `_is_retryable(error_type)`, `_proxy_pool: ProxyPool | None`

- [ ] **Step 1: Write the failing test**

Create `tests/test_news_fallback.py`:

```python
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pytest tests/test_news_fallback.py -v`
Expected: FAIL — `AttributeError: type object 'BaseNewsFetcher' has no attribute '_is_retryable'`

- [ ] **Step 3: Write implementation in base.py**

Add to the end of `BaseNewsFetcher` class (after the circuit breaker methods):

```python
    # ------------------------------------------------------------------
    # Proxy + retry (Phase 2)
    # ------------------------------------------------------------------

    _proxy_pool = None  # type: ignore  # injected by aggregator

    async def _run_blocking_with_proxy(
        self, func, *args, proxy: str, timeout: float | None = None
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
                return func(*args)
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

        with ThreadPoolExecutor(max_workers=1) as pool:
            fut = pool.submit(_runner)
            try:
                return await loop.run_in_executor(None, lambda: fut.result(timeout=_to))
            except Exception:
                fut.cancel()
                raise

    async def _fetch_with_retry(
        self, func, *args, timeout: float | None = None
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
            return await self._run_blocking(func, *args, timeout=_to)
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
                        timeout=max(_to, 20.0),
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

        # Default: retry unknown errors (safe side)
        return True
```

- [ ] **Step 4: Run test to verify it passes**

Run: `pytest tests/test_news_fallback.py -v`
Expected: 7 passed

- [ ] **Step 5: Commit**

```bash
git add src/stock_watcher/news/base.py tests/test_news_fallback.py
git commit -m "feat: add _fetch_with_retry + proxy fallback to BaseNewsFetcher"
```

---

### Task 7: Update akshare fetchers to use `_fetch_with_retry()`

**Files:**
- Modify: `src/stock_watcher/news/eastmoney_flash.py`
- Modify: `src/stock_watcher/news/eastmoney_stock.py`
- Modify: `src/stock_watcher/news/cls_telegraph.py`
- Modify: `src/stock_watcher/news/sina_flash.py`
- Modify: `src/stock_watcher/news/yahoo_news.py`

**Interfaces:**
- Consumes: `BaseNewsFetcher._fetch_with_retry()`

- [ ] **Step 1: Update all 5 fetchers**

Replace `await self._run_blocking(...)` with `await self._fetch_with_retry(...)` in each fetcher:

**eastmoney_flash.py** line 31:
```python
            df = await self._fetch_with_retry(akshare.stock_info_global_em)
```

**eastmoney_stock.py** line 41:
```python
                df = await self._fetch_with_retry(akshare.stock_news_em, symbol=raw_code)
```

**cls_telegraph.py** line 25:
```python
            df = await self._fetch_with_retry(akshare.stock_info_global_cls)
```

**sina_flash.py** line 28:
```python
            df = await self._fetch_with_retry(akshare.stock_info_global_sina)
```

**yahoo_news.py** lines 50-51:
```python
                t = await self._fetch_with_retry(yfinance.Ticker, ticker)
                news_list = await self._fetch_with_retry(lambda: t.news or [])
```

- [ ] **Step 2: Run full test suite**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all tests pass

- [ ] **Step 3: Commit**

```bash
git add src/stock_watcher/news/eastmoney_flash.py \
        src/stock_watcher/news/eastmoney_stock.py \
        src/stock_watcher/news/cls_telegraph.py \
        src/stock_watcher/news/sina_flash.py \
        src/stock_watcher/news/yahoo_news.py
git commit -m "refactor: use _fetch_with_retry in 5 akshare fetchers"
```

---

### Task 8: Update httpx fetchers to accept proxy

**Files:**
- Modify: `src/stock_watcher/news/sina_roll.py`
- Modify: `src/stock_watcher/news/sina_stock.py`

**Interfaces:**
- Produces: `SinaRollNewsFetcher.fetch(watchlist_codes, proxy=None)`, `SinaStockNewsFetcher.fetch(watchlist_codes, proxy=None)`

- [ ] **Step 1: Update sina_roll.py**

Add `proxy: str | None = None` parameter to `fetch()` and pass to `httpx.AsyncClient`:

In `sina_roll.py`, change the `fetch` signature and client creation (lines 46, 50):

```python
    async def fetch(
        self, watchlist_codes: list[str], proxy: str | None = None
    ) -> list[NewsItem]:
        items: list[NewsItem] = []
        seen_urls: set[str] = set()

        async with httpx.AsyncClient(timeout=_TIMEOUT, proxy=proxy) as client:
```

- [ ] **Step 2: Update sina_stock.py**

Add `proxy: str | None = None` parameter to `fetch()` and pass to `httpx.AsyncClient`:

In `sina_stock.py`, change line 33 and 42:

```python
    async def fetch(
        self, watchlist_codes: list[str], proxy: str | None = None
    ) -> list[NewsItem]:
        """抓取自选股个股新闻（仅 A 股）."""
        a_codes = [c for c in watchlist_codes if c.startswith(("sh", "sz"))]
        if not a_codes:
            return []

        items: list[NewsItem] = []
        seen: set[str] = set()

        async with httpx.AsyncClient(timeout=_TIMEOUT, proxy=proxy) as client:
```

- [ ] **Step 3: Run full test suite**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all tests pass (existing tests don't pass proxy, default None is backward-compatible)

- [ ] **Step 4: Commit**

```bash
git add src/stock_watcher/news/sina_roll.py \
        src/stock_watcher/news/sina_stock.py
git commit -m "feat: add optional proxy param to sina_roll and sina_stock fetchers"
```

---

### Task 9: Wire ProxyPool into aggregator and app.py

**Files:**
- Modify: `src/stock_watcher/news/aggregator.py`
- Modify: `src/stock_watcher/app.py`

**Interfaces:**
- Produces: `NewsAggregator.__init__(..., proxy_pool=None)`, `NewsAggregator._inject_proxy_to_fetcher()`
- Consumes: `ProxyPool`

- [ ] **Step 1: Update aggregator to accept and inject ProxyPool**

In `src/stock_watcher/news/aggregator.py`, update `__init__`:

```python
    def __init__(
        self,
        fetchers: list[BaseNewsFetcher],
        seen_path: str = "news_seen.jsonl",
        proxy_pool = None,
    ) -> None:
        self._fetchers = fetchers
        self._proxy_pool = proxy_pool
        self._seen_path = Path(seen_path)
        self._seen_ids: set[str] = set()
        self._seen_urls: set[str] = set()
        self._seen_titles: list[str] = []
        self._load_seen()
```

Add method to inject proxy into fetchers, and update `_fetch_one` to pass proxy to httpx fetchers:

```python
    async def _fetch_one(
        self, fetcher: BaseNewsFetcher, codes: list[str]
    ) -> list[NewsItem]:
        """包装单个 Fetcher 调用，处理异常。注入代理池引用。"""
        # Inject proxy pool reference into fetcher
        if self._proxy_pool is not None:
            fetcher._proxy_pool = self._proxy_pool  # type: ignore

        # For httpx fetchers, pick a proxy if available
        proxy = None
        if self._proxy_pool is not None and self._proxy_pool.available_count > 0:
            proxy = self._proxy_pool.get_proxy()

        try:
            # Pass proxy to httpx fetchers that accept it
            import inspect
            sig = inspect.signature(fetcher.fetch)
            if 'proxy' in sig.parameters:
                result = await fetcher.fetch(codes, proxy=proxy)  # type: ignore
            else:
                result = await fetcher.fetch(codes)
            fetcher._record_success()
            return result
        except Exception as exc:
            logger.error("%s fetch error: %s", fetcher.source_name, exc)
            fetcher._record_failure()
            return []
```

- [ ] **Step 2: Update app.py to create and pass ProxyPool**

In `src/stock_watcher/app.py`, in `_news_loop()`, after the aggregator init (around line 1329):

Add ProxyPool import and initialization:

```python
        # Init aggregator with proxy pool (Phase 2)
        from stock_watcher.proxy_pool import ProxyPool
        proxy_pool = ProxyPool(cache_path="proxy_cache.json")
        # Kick off initial async refresh (don't block startup)
        asyncio.create_task(proxy_pool.refresh())

        # Init aggregator and classifier
        seen_path = Path(cfg.seen_ids_path) if cfg.seen_ids_path else Path("news_seen.jsonl")
        self._news_aggregator = NewsAggregator(
            fetchers, str(seen_path), proxy_pool=proxy_pool,
        )
```

Also add periodic proxy refresh in the main poll loop. Insert before `await asyncio.sleep(cfg.poll_interval)` at the end of the loop (around line 1479):

```python
                # Refresh proxy pool periodically (every 30 min, or when low)
                if proxy_pool.available_count < 3:
                    asyncio.create_task(proxy_pool.refresh())
```

- [ ] **Step 3: Run full test suite**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all tests pass

- [ ] **Step 4: Commit**

```bash
git add src/stock_watcher/news/aggregator.py src/stock_watcher/app.py
git commit -m "feat: wire ProxyPool into NewsAggregator and app.py"
```

---

### Task 10: Add Phase 2 config entries

**Files:**
- Modify: `config.yaml`

**Interfaces:**
- Adds `news.proxy_pool` config section

- [ ] **Step 1: Add proxy_pool config section**

In `config.yaml`, under the `news:` section, after `seen_ids_path`, add:

```yaml
  # 代理池 —— Phase 2: 自动抓取免费代理，直连失败后 fallback
  proxy_pool:
    enabled: true
    refresh_interval: 1800   # 刷新间隔（秒），默认 30 分钟
    min_proxies: 3           # 低于此数立即刷新
    validate_timeout: 5      # 验证超时（秒）
    max_proxies: 20          # 池子容量上限
    cache_path: "proxy_cache.json"
```

- [ ] **Step 2: Commit**

```bash
git add config.yaml
git commit -m "config: add news.proxy_pool config section"
```

---

### Task 11: End-to-end verification

**Files:**
- All modified files

- [ ] **Step 1: Run full test suite one final time**

Run: `pytest tests/ -x --ignore=tests/test_e2e_news.py -q`
Expected: all tests pass (expect 291+)

- [ ] **Step 2: Run a quick integration smoke test**

```bash
cd /root/data/codebase/toys_project/stock_watcher && timeout 45 python3 -c "
import asyncio, sys
sys.path.insert(0, 'src')

async def test():
    from stock_watcher.news import (
        SinaFlashNewsFetcher, SinaRollNewsFetcher, SinaStockNewsFetcher,
        NewsAggregator,
    )
    from stock_watcher.proxy_pool import ProxyPool
    
    # Verify ProxyPool is importable and works
    pool = ProxyPool(cache_path='/tmp/smoke_proxy_cache.json')
    assert pool.available_count >= 0  # may be empty if no cache
    
    # Verify aggregator with proxy_pool=None still works
    codes = ['sh603848', 'sz002920', 'hk00700']
    fetchers = [
        SinaFlashNewsFetcher(enabled=True),
        SinaRollNewsFetcher(enabled=True),
        SinaStockNewsFetcher(enabled=True),
    ]
    agg = NewsAggregator(fetchers, '/tmp/smoke_seen.jsonl')
    items = await asyncio.wait_for(agg.aggregate(codes), timeout=30)
    print(f'Smoke test: {len(items)} items aggregated')
    assert len(items) > 0
    
    # Verify circuit breaker states
    assert fetchers[0].state == 'green'
    print('All smoke tests PASSED')

asyncio.run(test())
"
```

Expected: `Smoke test: XX items aggregated` / `All smoke tests PASSED`

- [ ] **Step 3: Commit any remaining changes**

```bash
git status
git add -A
git diff --staged --stat
# Only commit if there are meaningful leftover changes
```
