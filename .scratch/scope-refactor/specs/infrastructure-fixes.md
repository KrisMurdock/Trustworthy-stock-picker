# Spec: 基础设施修复 (Infrastructure Fixes)

## Overview

修复日志、HTTP 连接池、异常处理、配置默认值、并发锁等底层基础设施问题。

---

## Requirements

### IF-S01: 日志系统启用

`main()` 函数 SHALL 在 `app.run()` 之前调用 `logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s")`。

SHALL NOT 影响 Textual 的 TUI 渲染（日志输出到 stderr，TUI 使用 stdout）。

#### Scenario: 启动时可见日志行

- **WHEN** `bash run.sh` 启动
- **THEN** stderr SHALL 输出格式为 `2025-07-03 10:30:00 [INFO] stock_watcher.news.aggregator: aggregated: ...`

---

### IF-S02: 共享 HTTP 连接池

SHALL 新增 `src/stock_watcher/http_client.py`，提供 `get_shared_client() → httpx.AsyncClient` 函数。

返回的 client SHALL 是模块级单例（首次调用创建，后续复用）。

SHALL 在应用退出时通过 `close_shared_client()` 关闭。

所有现有 `httpx.AsyncClient()` 直接构造 SHALL 替换为 `get_shared_client()` 调用。

#### Scenario: 多次调用返回同一实例

- **WHEN** `get_shared_client()` 被调用 3 次
- **THEN** 返回的 3 个对象 SHALL 是同一实例（`is` 比较为 True）

#### Scenario: 替换现有 fetcher

- **WHEN** `SinaFetcher.fetch()` 执行
- **THEN** 不应出现 `httpx.AsyncClient(timeout=...)` 直接构造，应使用 `get_shared_client()`

---

### IF-S03: 静默异常修复

所有 `except Exception: pass` 和 `except: pass` SHALL 替换为带日志的错误处理。

每处修改 SHALL 至少 `logger.error()` 或 `logger.warning()` 记录异常信息。

涉及文件：`app.py`, `cninfo_fetcher.py`, `chat_sender.py`。

#### Scenario: cninfo_fetcher 异常不再静默

- **WHEN** `cninfo_fetcher.py` 的 HTTP 请求失败
- **THEN** stderr SHALL 输出包含异常类型的 WARNING 日志

---

### IF-S04: 死代码删除

`src/stock_watcher/bot_server.py` SHALL 被删除。

项目中 SHALL NOT 存在任何 `from stock_watcher.bot_server import` 或 `import stock_watcher.bot_server`。

`run_bot_server` 函数 SHALL 不再可导入。

#### Scenario: 删除后无引用

- **WHEN** `rg "bot_server" src/` 执行
- **THEN** 输出 SHALL 为空

---

### IF-S05: 配置默认值对齐

`NewsConfig.from_dict()` 中 `poll_interval` 的默认值 SHALL 从 `120` 改为 `300.0`，与 `NewsConfig` dataclass 的 `field(default=300.0)` 一致。

#### Scenario: 缺少 poll_interval 键时使用 300

- **WHEN** `NewsConfig.from_dict({})` 调用
- **THEN** `result.poll_interval` SHALL 等于 `300.0`

---

### IF-S06: Bot 并发锁

`_bot_poll_once()` 方法 SHALL 使用 `asyncio.Lock` 防止重叠执行。

当上一轮 Bot 轮询未完成时，新的 `set_interval` 触发 SHALL 跳过本轮（不排队、不阻塞）。

#### Scenario: 重叠执行被跳过

- **WHEN** `_bot_poll_once` 正在执行且 `set_interval(3.0)` 触发新一轮
- **THEN** 第二轮 SHALL 检测到锁被持有，直接 return 不执行

---

### IF-S07: 新闻历史数据并发拉取

`_news_loop` 中历史数据拉取 SHALL 从串行 `for` 循环改为 `asyncio.gather(*[to_thread(...)])` 并发。

SHALL 限制并发数为 5（`asyncio.Semaphore(5)`）以避免触发 API 限流。

#### Scenario: 30 只股票的历史数据并发拉取

- **WHEN** 扇区增强需要拉取 30 只股票的 90 天日线
- **THEN** 总耗时 SHALL ≤ `max(单次耗时) * ceil(30/5)` 而非 `sum(30次耗时)`
