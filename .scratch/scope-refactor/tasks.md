# Tasks: 项目全局优化 (scope-refactor)

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `src/stock_watcher/http_client.py` | 共享 `httpx.AsyncClient` 单例 |
| Create | `src/stock_watcher/controllers/__init__.py` | 控制器统一导出 |
| Create | `src/stock_watcher/controllers/poll_controller.py` | 行情轮询 + 告警判断 |
| Create | `src/stock_watcher/controllers/news_controller.py` | 新闻聚合 + 板块增强 + 推送 |
| Create | `src/stock_watcher/controllers/bot_controller.py` | 飞书 Bot 轮询 + 回复 |
| Create | `src/stock_watcher/controllers/alert_controller.py` | 告警规则 CRUD + 持久化 |
| Modify | `src/stock_watcher/app.py` | 提取控制器后精简至 ≤600 行 |
| Modify | `src/stock_watcher/config.py` | `NewsConfig.from_dict` 默认值 120→300.0 |
| Modify | `src/stock_watcher/fetcher.py` | 使用 `get_shared_client()` |
| Modify | `src/stock_watcher/cninfo_fetcher.py` | `except: pass` → `logger.error` |
| Modify | `src/stock_watcher/chat_sender.py` | `except: pass` → `logger.warning` |
| Delete | `src/stock_watcher/bot_server.py` | 死代码删除 |
| Create | `tests/test_http_client.py` | 连接池单例测试 |
| Create | `tests/test_poll_controller.py` | PollController 单元测试 |
| Create | `tests/test_news_controller.py` | NewsController 集成测试 |
| Create | `tests/test_bot_controller.py` | BotController 并发锁测试 |
| Create | `tests/test_alert_controller.py` | AlertController CRUD 测试 |

## Interfaces

### Batch 1 → Batch 2
- **Produces**: `get_shared_client()`, `close_shared_client()` — consumed by fetcher.py, controllers

### Batch 2 → Batch 3
- **Produces**: `PollController`, `AlertController` — consumed by app.py for wiring
- **Produces**: `NewsController` — consumed by app.py for wiring
- **Produces**: `BotController` — consumed by app.py for wiring

---

## Batch 1: Infrastructure Foundation

**Depends on**: none

### T01 — logging.basicConfig 启用

1. Modify `app.py:main()`: 在 `app.run()` 之前插入
```python
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
    stream=sys.stderr,
)
```
2. Run `python3 -c "from stock_watcher.app import main; print('logging configured')"` → confirm no errors
3. Commit: `fix: enable logging via basicConfig in main()`

**Files**: `app.py:2256-2261`

### T02 — 共享 HTTP 连接池

1. Write failing test `tests/test_http_client.py`:
```python
def test_singleton():
    c1 = get_shared_client()
    c2 = get_shared_client()
    assert c1 is c2

def test_close_and_reset():
    close_shared_client()
    c1 = get_shared_client()
    close_shared_client()
    c2 = get_shared_client()
    assert c1 is not c2  # new instance after reset
```
2. Run → fail (no module)
3. Create `src/stock_watcher/http_client.py`:
```python
import httpx
_client: httpx.AsyncClient | None = None

def get_shared_client() -> httpx.AsyncClient:
    global _client
    if _client is None:
        _client = httpx.AsyncClient(timeout=15.0)
    return _client

def close_shared_client() -> None:
    global _client
    if _client:
        # schedule close, don't block
        import asyncio
        try:
            loop = asyncio.get_running_loop()
            loop.create_task(_client.aclose())
        except RuntimeError:
            pass
        _client = None
```
4. Run test → pass
5. Commit: `feat: add shared httpx.AsyncClient singleton`

**Files**: `http_client.py`, `tests/test_http_client.py`

### T03 — 配置默认值对齐

1. Test: `python3 -c "from stock_watcher.config import NewsConfig; c = NewsConfig.from_dict({}); assert c.poll_interval == 300.0"`
2. Modify `config.py:358`: `float(d.get("poll_interval", 120))` → `float(d.get("poll_interval", 300.0))`
3. Verify → pass
4. Commit: `fix: align NewsConfig.from_dict poll_interval default to 300.0`

**Files**: `config.py:358`

---

## Batch 2: MVC Controller Extraction

**Depends on**: Batch 1

### T04 — AlertController 提取

1. Write `tests/test_alert_controller.py`:
```python
def test_add_alert():
    ctrl = AlertController()
    ctrl.add("sh600519", "price_above", 2000.0)
    assert len(ctrl.rules) == 1
    assert ctrl.rules[0].code == "sh600519"

def test_remove_alert():
    ctrl = AlertController()
    ctrl.add("sh600519", "price_above", 2000.0)
    ctrl.remove("sh600519", "price_above")
    assert len(ctrl.rules) == 0
```
2. Run → fail
3. Create `controllers/alert_controller.py` with `add(code, alert_type, value)`, `remove(code, alert_type)`, `rules` property
4. Run → pass
5. Commit: `refactor: extract AlertController from app.py`

**Files**: `controllers/__init__.py`, `controllers/alert_controller.py`, `tests/test_alert_controller.py`

### T05 — PollController 提取

1. Write `tests/test_poll_controller.py`:
```python
@pytest.mark.asyncio
async def test_poll_controller_calls_callback():
    quotes_received = []
    async def on_quote(quote): quotes_received.append(quote)
    ctrl = PollController(
        watchlist=["sh600519"],
        get_fetcher=lambda code: MockFetcher(),
        on_quote=on_quote,
    )
    stop = asyncio.Event()
    task = asyncio.create_task(ctrl.run(stop))
    await asyncio.sleep(0.5)
    stop.set()
    await task
    assert len(quotes_received) > 0
```
2. Run → fail
3. Create `controllers/poll_controller.py` with `run(stop_event)`, `_fetch_one(code)`, callback injection
4. Run → pass
5. Commit: `refactor: extract PollController from app.py`

**Files**: `controllers/poll_controller.py`, `tests/test_poll_controller.py`

### T06 — NewsController 提取

1. Write `tests/test_news_controller.py`:
```python
def test_news_controller_init():
    cfg = NewsConfig()
    ctrl = NewsController(cfg, deepseek_cfg=None, on_push=lambda card: None)
    assert ctrl._cfg is cfg
```
2. Run → fail
3. Create `controllers/news_controller.py` — wraps `_news_loop` logic, accepts `on_push` callback
4. Run → pass
5. Commit: `refactor: extract NewsController from app.py`

**Files**: `controllers/news_controller.py`, `tests/test_news_controller.py`

### T07 — BotController 提取

1. Write `tests/test_bot_controller.py`:
```python
@pytest.mark.asyncio
async def test_bot_controller_lock_prevents_reentry():
    ctrl = BotController(chat_cfg=MagicMock(), deepseek_cfg=None)
    # Simulate concurrent calls
    t1 = asyncio.create_task(ctrl.poll_once())
    t2 = asyncio.create_task(ctrl.poll_once())
    await asyncio.gather(t1, t2)
    # Second call should have been skipped by lock
```
2. Run → fail
3. Create `controllers/bot_controller.py` with `poll_once()`, `asyncio.Lock` guard
4. Run → pass
5. Commit: `refactor: extract BotController from app.py with concurrency lock`

**Files**: `controllers/bot_controller.py`, `tests/test_bot_controller.py`

### T08 — App 精简 + 控制器编排

1. Modify `app.py`:
   - Replace `_poll_loop` with `PollController` 实例
   - Replace `_news_loop` with `NewsController` 实例
   - Replace `_bot_poll_once` with `BotController` 实例
   - Replace `_add_alert`/`_remove_alert` with `AlertController` 实例
   - In `on_mount`: create controllers, wire callbacks, start via `@work`
2. Verify: `wc -l app.py` ≤ 600
3. Run full test suite → 288 pass
4. Commit: `refactor: slim app.py to ≤600 lines via MVC controllers`

**Files**: `app.py`

---

## Batch 3: Error Handling + Dead Code + Concurrency

**Depends on**: Batch 2

### T09 — 静默异常修复

1. Scan: `rg "except.*:" app.py cninfo_fetcher.py chat_sender.py | grep -i pass`
2. For each `except: pass` or `except Exception: pass`:
   - Replace with `except Exception: logger.warning("...", exc_info=True)`
   - Target files: `app.py` (~5 处), `cninfo_fetcher.py` (~3 处), `chat_sender.py` (~2 处)
3. Run full test suite → 288 pass
4. Commit: `fix: replace silent except:pass with logged warnings`

**Files**: `app.py`, `cninfo_fetcher.py`, `chat_sender.py`

### T10 — 死代码删除

1. Verify no imports: `rg "bot_server" src/`
2. Delete `src/stock_watcher/bot_server.py`
3. Verify app imports still work: `python3 -c "from stock_watcher.app import StockWatcherApp"`
4. Verify no references remain: `rg "bot_server" src/` → empty
5. Commit: `chore: delete dead bot_server.py`

**Files**: `bot_server.py` (deleted)

### T11 — Fetcher 连接池替换

1. Modify `fetcher.py`: replace `httpx.AsyncClient(timeout=...)` with `get_shared_client()`
2. Run `tests/test_fetcher.py` → pass
3. Commit: `refactor: use shared AsyncClient in fetcher.py`

**Files**: `fetcher.py`

### T12 — 新闻扇区并发拉取

1. Modify `controllers/news_controller.py` (or `app.py` if still inlined):
   - Replace serial `for s in all_stocks[:30]` with `asyncio.gather`
   - Add `asyncio.Semaphore(5)` for concurrency control
```python
sem = asyncio.Semaphore(5)
async def fetch_one(stock):
    async with sem:
        return await asyncio.to_thread(akshare.stock_zh_a_hist_tx, ...)
dfs = await asyncio.gather(*[fetch_one(s) for s in all_stocks[:30]], return_exceptions=True)
```
2. Run full test suite → 288 pass
3. Commit: `perf: parallelize news sector historical data fetching with semaphore`

**Files**: `controllers/news_controller.py`

---

## Estimated Effort

| Batch | Tasks | Est. Hours |
|-------|-------|-----------|
| B1: Foundation | T01–T03 | 0.5h |
| B2: Controller Extraction | T04–T08 | 3h |
| B3: Error + Dead + Perf | T09–T12 | 1.5h |
| **Total** | **12 tasks** | **~5h** |
