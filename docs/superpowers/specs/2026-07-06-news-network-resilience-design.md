# News Fetcher Network Resilience — Design Spec

**Date:** 2026-07-06  
**Status:** draft  
**Author:** reasonix  

## Overview

Stock Watcher 的新闻推送依赖于 7 个外部新闻源。2026-07-06 诊断发现 4 个源因网络封锁完全不可用（East Money ×2、Yahoo、CLS），1 个源因代码 bug 静默返回 0 条（Sina Flash 列名不匹配）。剩余 2 个可用的 Sina 源（SinaRoll、SinaStock）未被接入主循环。

紧急修复（已完成）恢复了 Sina 三源的推送能力（~70 条/轮），但底层网络韧性仍有系统性缺陷。本设计对网络层做全面加固。

## Motivation

### 当前弱点

| 层面 | 问题 | 影响 |
|------|------|------|
| **超时** | akshare 抓取器用 `asyncio.to_thread()` 调用阻塞函数，线程无法被 asyncio 取消 | 一个源挂起拖慢整轮聚合 |
| **熔断** | 5 次连续失败后永久跳过，无时间衰减、无自动恢复 | 源一旦被熔断就永远不会恢复 |
| **代理** | `proxies` 配置仅 cninfo 使用，新闻抓取全走直连 | 被墙的源没有绕过手段 |
| **重试** | 无区分瞬时错误（timeout/429）和永久错误（403/SSL），无 fallback | 瞬时波动直接导致推送缺失 |

### 目标

1. **超时可控** — 每个 akshare 调用都有硬超时，不阻塞事件循环
2. **熔断可恢复** — 时间衰减 + 半开探测，被墙的源有机会自动恢复
3. **代理可绕过** — 自动代理池 + 直连优先/代理 fallback 混合策略
4. **重试可区分** — 瞬时错误重试（含代理 fallback），永久错误快速失败

## Design

### Phase 1: Timeout + Circuit Breaker (基础韧性)

#### 1.1 `BaseNewsFetcher._run_blocking()` — 统一硬超时

在 base class 新增方法，替代各抓取器中的 `asyncio.to_thread()`：

```python
async def _run_blocking(self, func, *args, timeout: float | None = None) -> Any:
    """在线程池中运行阻塞函数，带硬超时。

    使用 ThreadPoolExecutor.submit().result(timeout=N) 实现真正的超时中断。
    超时时线程被丢弃（最多泄漏 1 个线程/次），但保证控制权回到事件循环。
    """
    timeout = timeout or self._fetch_timeout
    loop = asyncio.get_running_loop()

    def _runner():
        return func(*args)

    with ThreadPoolExecutor(max_workers=1) as pool:
        fut = pool.submit(_runner)
        try:
            return await loop.run_in_executor(None, lambda: fut.result(timeout=timeout))
        except Exception:
            fut.cancel()
            raise
```

**配置**：`BaseNewsFetcher._fetch_timeout: float = 15`（可被子类覆盖）

**影响的抓取器**（5 个 akshare 调用点）：
- `EastMoneyStockNewsFetcher.fetch()` — 每个 `akshare.stock_news_em()` 调用
- `EastMoneyFlashNewsFetcher.fetch()` — `akshare.stock_info_global_em()`
- `SinaFlashNewsFetcher.fetch()` — `akshare.stock_info_global_sina()`
- `CLSTelegraphFetcher.fetch()` — `akshare.stock_info_global_cls()`
- `YahooNewsFetcher.fetch()` — `yfinance.Ticker().news`

每个调用点从 `await asyncio.to_thread(akshare.xxx)` 改为 `await self._run_blocking(akshare.xxx)`。

#### 1.2 Circuit Breaker — 时间衰减 + 半开探测

将 `_consecutive_failures: int` 改为 `_failure_history: list[float]`（失败时间戳列表），增加三种状态：

```
       正常                     熔断                       半开
  ┌──────────┐  5次失败/5min   ┌──────────┐  冷却 5min   ┌──────────┐
  │  GREEN   │ ───────────────→│   RED    │ ────────────→│  YELLOW  │
  │ 正常抓取  │                 │ 完全跳过  │              │ 探测1条   │
  └──────────┘                 └──────────┘              └──────────┘
       ↑                         ↑      ↑                      │
       │                         │      │ 每次探测仍失败         │
       │    探测成功               │      └──────────────────────┘
       └─────────────────────────┘             重设冷却计时器
```

**参数**（硬编码默认值，可后续配置化）：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `failure_window` | 300s | 失败计数窗口 |
| `failure_threshold` | 5 | 触发熔断的失败次数 |
| `cooldown_period` | 300s | 熔断 → 半开的冷却时间 |
| `probe_interval` | 60s | 半开状态探测间隔 |

**BaseNewsFetcher 属性变化**：

```python
_failure_history: list[float]  # 失败时间戳

@property
def is_failing(self) -> bool:
    """True = 熔断中，聚合器完全跳过"""
    ...

@property
def state(self) -> str:
    """返回 "green" / "red" / "yellow" """
    ...

def should_probe(self) -> bool:
    """True = 半开状态且距上次探测超过 probe_interval"""
    ...
```

**聚合器 `aggregate()` 行为**：

```python
for f in self._fetchers:
    if f.enabled and not f.is_failing:
        tasks.append(...)           # 正常抓取
    elif f.should_probe():
        tasks.append(...)           # 半开探测（发 1 条请求）
    else:
        logger.debug("skipped")     # 熔断中或冷却中
```

### Phase 2: Proxy Pool + Fallback (网络绕过)

#### 2.1 `ProxyPool` — 自动抓取 + 验证 + 缓存

**新文件**：`src/stock_watcher/proxy_pool.py`

```python
@dataclass
class ProxyEntry:
    url: str
    score: float        # 0.0 ~ 1.0，健康评分
    latency: float      # 最近验证延迟(秒)
    last_check: float   # 最后验证时间戳
    fail_count: int     # 连续失败次数

class ProxyPool:
    """单例。自动从免费源抓取代理，验证可用性，健康评分排序。"""

    def __init__(self, cache_path: str = "proxy_cache.json"):
        self._proxies: list[ProxyEntry] = []
        self._cache_path = Path(cache_path)
        self._load_cache()

    async def refresh(self) -> int:
        """抓取 + 验证，返回新增可用代理数。
        1. 从 2-3 个免费代理源并发抓取（httpx 请求，解析 HTML/JSON）
        2. 去重 + 过滤已失效
        3. 并发验证（请求百度首页，超时 5s）
        4. 更新 _proxies，按 score 降序排列
        """

    def get_proxy(self) -> str | None:
        """轮询返回一个代理地址（http://ip:port）"""

    def get_proxies(self, n: int) -> list[str]:
        """返回最多 n 个代理，按评分降序"""

    def mark_dead(self, proxy: str) -> None:
        """标记失效，fail_count += 1，≥3 次移除"""

    def mark_slow(self, proxy: str, latency: float) -> None:
        """更新延迟和评分"""

    @property
    def available_count(self) -> int: ...

    @property
    def stats(self) -> dict: ...
```

**代理源**（内置 2-3 个免费 API，写在模块常量中）：

- 源 1: 解析 JSON 的免费代理 API（如 `proxylist.geonode.com/api`）
- 源 2: 解析 HTML 的免费代理列表页
- 源 3: fallback 静态代理列表

**刷新策略**：
- 启动时加载缓存 + 后台异步刷新
- 每 30 分钟自动刷新
- 可用代理 < 3 个时立即刷新
- 缓存到 `proxy_cache.json`，过期时间 2 小时

#### 2.2 直连 → 代理 Fallback

**`BaseNewsFetcher._fetch_with_retry()`**：

```python
async def _fetch_with_retry(self, func, *args, timeout: float | None = None) -> Any:
    """混合策略：直连优先 → 代理 fallback。

    1. 直连尝试（_run_blocking）
    2. 失败 → 最多尝试 3 个不同代理
    3. 全部失败 → raise last error
    """
    timeout = timeout or self._fetch_timeout

    # Step 1: 直连
    try:
        return await self._run_blocking(func, *args, timeout=timeout)
    except Exception as e:
        if not self._is_retryable(type(e)):
            raise
        last_error = e

    # Step 2: 代理 fallback
    if self._proxy_pool and self._proxy_pool.available_count > 0:
        for proxy in self._proxy_pool.get_proxies(3):
            try:
                return await self._run_blocking_with_proxy(
                    func, *args, proxy=proxy, timeout=max(timeout, 20)
                )
            except Exception as e:
                self._proxy_pool.mark_dead(proxy)
                last_error = e
                continue

    raise last_error
```

**代理注入方式**（仅对 akshare/requests 有效）：
- `_run_blocking_with_proxy()` 在执行前设置环境变量 `HTTP_PROXY` / `HTTPS_PROXY`
- akshare → requests → 自动读取环境变量
- 执行后恢复原值（线程安全：在 `_runner` 闭包内操作）

**httpx 抓取器的处理**（`sina_roll.py`, `sina_stock.py`）：
- 它们不经过 `_run_blocking`，直接使用 httpx
- `fetch()` 方法接受可选的 `proxy` 参数传给 `httpx.AsyncClient(proxy=proxy)`
- `NewsAggregator.aggregate()` 在调用 `_fetch_one()` 前传入 proxy

#### 2.3 可重试性判断

```python
# 瞬时错误（应重试）
_RETRYABLE_ERRORS = (
    TimeoutError,
    ConnectionError,
    # httpx
    "httpx.TimeoutException",
    "httpx.ConnectError",
    "httpx.RemoteProtocolError",
)

# 永久错误（不应重试，含代理也不管用）
_NON_RETRYABLE_ERRORS = (
    "SSLError",          # SSL 握手失败 = 被墙
    "HTTPStatusError",   # 4xx（除 429）
)

@staticmethod
def _is_retryable(error_type: type) -> bool:
    ...
```

**注意**：当前 East Money / Yahoo 的 `SSLError: Connection reset by peer` 被归类为非可重试。这意味着它们直连失败后不会走代理 fallback —— 这是**刻意设计**：SSL reset 意味着 TCP 层被阻断，同一条链路的代理大概率也无效。应该让熔断器快速将其标记为 failing，等冷却后在半开探测时再试代理。

### Phase 1 → 2 集成关系

```
Phase 1 (先去)
├── BaseNewsFetcher._run_blocking()       ← 超时
├── Circuit breaker (3-state)             ← 熔断/半开/恢复
└── Aggregator: green/yellow/red 分支      ← 聚合器适配

Phase 2 (后上)
├── ProxyPool                              ← 代理池模块
├── BaseNewsFetcher._fetch_with_retry()    ← 混合 fallback
├── BaseNewsFetcher._run_blocking_with_proxy()  ← 代理注入
└── Aggregator: 持有 ProxyPool，注入 fetcher
```

## Data Flow (After Phase 2)

```
_news_loop (app.py)
  │
  ├── NewsAggregator.aggregate(codes)
  │     │
  │     ├── proxy_pool.refresh_if_stale()     # 维护代理池
  │     │
  │     ├── for each fetcher:
  │     │     │
  │     │     ├── state == GREEN  → _fetch_one(fetcher, codes)
  │     │     │   fetcher.fetch() 调用 self._fetch_with_retry(akshare_func)
  │     │     │     ├── 直连 15s → 成功 ✓
  │     │     │     └── 失败 → 代理1 20s → 代理2 20s → 代理3 20s → raise
  │     │     │
  │     │     ├── state == YELLOW → should_probe()? → _fetch_one() (单条探测)
  │     │     │
  │     │     └── state == RED   → skip
  │     │
  │     └── asyncio.wait(tasks, timeout=60)   # 整批超时从 30s → 60s（含代理重试）
  │
  ├── dedup → classify → push critical → persist_seen
  │
  └── sleep(poll_interval)
```

## Files Changed

| File | Phase | Change |
|------|-------|--------|
| `news/base.py` | 1 | `_run_blocking()`, circuit breaker (3-state), `_fetch_timeout` |
| `news/base.py` | 2 | `_fetch_with_retry()`, `_run_blocking_with_proxy()`, `_is_retryable()`, `_proxy_pool` |
| `news/aggregator.py` | 1 | 3-state branch (green/yellow/red), timeout 30→60s |
| `news/aggregator.py` | 2 | Hold ProxyPool, inject into fetchers |
| `news/eastmoney_flash.py` | 1 | `asyncio.to_thread` → `_fetch_with_retry` |
| `news/eastmoney_stock.py` | 1 | same |
| `news/cls_telegraph.py` | 1 | same |
| `news/sina_flash.py` | 1 | same |
| `news/yahoo_news.py` | 1 | same |
| `news/sina_roll.py` | 2 | Accept optional proxy param |
| `news/sina_stock.py` | 2 | Accept optional proxy param |
| `proxy_pool.py` | 2 | **NEW** — ProxyPool module |
| `app.py` | 1+2 | Wire ProxyPool into aggregator |
| `config.yaml` | 2 | Optional proxy pool config (refresh interval, sources) |
| `tests/test_news_base.py` | 1 | `_run_blocking` timeout/cancel tests |
| `tests/test_news_circuit.py` | 1 | Circuit breaker state transitions |
| `tests/test_proxy_pool.py` | 2 | Proxy fetch/validate/score/dead |
| `tests/test_news_fallback.py` | 2 | Direct→proxy fallback integration |

## Error Handling

| 场景 | 行为 |
|------|------|
| 直连超时 | `_run_blocking` 15s 超时 → 抛 `TimeoutError` → 进入代理 fallback |
| 代理超时 | 20s 超时 → `mark_dead(proxy)` → 换下一个代理 |
| 所有代理失败 | raise → `_fetch_one` catch → `_record_failure()` → 聚合器继续 |
| SSL reset (被墙) | 直连快速失败 → `_is_retryable=False` → 不 fallback → `_record_failure()` |
| 熔断中 | 聚合器跳过，日志 debug |
| 半开探测成功 | `_record_success()` → 恢复 GREEN |
| 半开探测失败 | `_record_failure()` → 重置冷却计时器，留在 YELLOW |
| 代理池为空 | `_fetch_with_retry` 跳过代理阶段，仅直连 |

## Configuration (config.yaml)

```yaml
news:
  # ... existing ...

  # 抓取超时（秒）—— Phase 1
  fetch_timeout: 15

  # 熔断器 —— Phase 1
  circuit_breaker:
    failure_window: 300      # 失败计数窗口（秒）
    failure_threshold: 5     # 触发熔断的失败次数
    cooldown_period: 300     # 熔断冷却时间（秒）
    probe_interval: 60       # 半开探测间隔（秒）

  # 代理池 —— Phase 2
  proxy_pool:
    enabled: true
    refresh_interval: 1800   # 刷新间隔（秒），默认 30 分钟
    min_proxies: 3           # 低于此数立即刷新
    validate_timeout: 5      # 验证超时（秒）
    max_proxies: 20          # 池子容量上限
    cache_path: "proxy_cache.json"
```

## Testing Strategy

### Phase 1
- **Unit**: `_run_blocking` timeout fires correctly, circuit breaker state transitions (green→red→yellow→green)
- **Integration**: Mock akshare with delay, verify aggregator skips RED, probes YELLOW

### Phase 2
- **Unit**: ProxyPool refresh/validate/cache/mark_dead
- **Unit**: `_fetch_with_retry` direct success, direct fail→proxy success, all fail
- **Integration**: Full aggregator run with proxy pool, verify fallback path triggers
- **Mock**: All external HTTP calls mocked via respx

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| 免费代理不稳定，验证通过但实际不能用 | `mark_dead` 快速淘汰，`score` 衰减机制 |
| `_run_blocking` 泄漏线程 | `max_workers=1` + `fut.cancel()`，每次调用新建 Executor 即销毁 |
| 代理环境变量污染 | 在 `_runner` 闭包内 save/restore，线程隔离 |
| 代理池抓取被墙 | 多源 fallback + 缓存兜底 |
| 熔断参数不适合所有源 | 硬编码默认值 + config.yaml 可覆盖 |
