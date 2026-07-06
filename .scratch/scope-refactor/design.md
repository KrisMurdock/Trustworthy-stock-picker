# Design: 项目全局优化 (scope-refactor)

## Context

### Current State
`app.py` 是 2270 行的 Textual TUI 应用，混合了 UI 渲染、行情轮询、告警判断、新闻管道、Bot 通信、CSV 导出、配置热加载所有职责。日志系统未初始化，HTTP 连接无复用，异常被静默吞噬。

### Constraints
- 288 现有测试必须全绿
- `StockWatcherApp.run()` 签名不变，`run.sh` 启动方式不变
- 配置热加载行为不变
- Python ≥ 3.10, asyncio, Textual, httpx

## Goals
1. app.py ≤ 600 行，纯 TUI 渲染 + 控制器编排
2. 所有模块输出结构化日志
3. 共享 `httpx.AsyncClient` 连接池
4. 零 `except: pass`
5. 删除 `bot_server.py` 死代码
6. 新闻历史数据并发拉取

## Decisions

### D1: MVC 拆分粒度

**Choice**: 4 个 Controller 类（Poll / News / Bot / Alert），每个持有自己的状态和依赖。
**Rationale**: 每个 Controller 对应一组内聚的功能。通过回调而非直接引用解耦 TUI 层和业务层。
**Alternatives**: 
- 纯函数式拆分（无类）— 状态管理复杂，不如 Controller 类清晰
- 微服务化 — 过度设计，单进程足矣

### D2: Controller 与 App 通信方式

**Choice**: 回调函数注入（`on_quote`, `on_push` 等），Controller 不持有 `StockWatcherApp` 引用。
**Rationale**: 回调是 Python 中最轻量的依赖反转方式，不需要引入消息总线或事件系统。
**Alternatives**:
- asyncio.Queue — 需要额外的消费者协程，增加复杂度
- 直接引用 app — 循环依赖，不利于测试

### D3: HTTP 连接池生命周期

**Choice**: 模块级单例 `get_shared_client()`，在 `main()` 退出时调用 `close_shared_client()`。
**Rationale**: 所有 HTTP 请求共享同一个连接池，利用 HTTP keep-alive 减少 TCP 握手开销。Textual 应用生命周期内只需要一个 client。
**Alternatives**:
- 每个 Controller 独立 client — 连接数翻倍，无收益
- 上下文管理器 — 无法跨 Controller 共享

### D4: 日志格式

**Choice**: `logging.basicConfig(level=INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s")`，输出到 stderr。
**Rationale**: Textual 占用 stdout，stderr 不受影响。格式包含时间戳、级别、模块名，便于 grep 和故障排查。
**Alternatives**:
- logging 到文件 — 配置复杂度增加，对于单进程 TUI 无必要

### D5: 异常处理策略

**Choice**: 捕获具体异常类型（`except httpx.HTTPError`），记录完整 traceback（`logger.error(..., exc_info=True)`）。
**Rationale**: `exc_info=True` 保留完整堆栈，比 `str(e)` 更利于排查。具体类型避免误吞 `KeyboardInterrupt` 等系统异常。
**Alternatives**:
- `except Exception: logger.error(...)` — 过于宽泛，但作为兜底可接受

### D6: 新闻扇区并发

**Choice**: `asyncio.gather` + `asyncio.Semaphore(5)` 限流。
**Rationale**: 历史数据拉取是完全独立的 I/O 任务，天然适合并发。Semaphore(5) 防止同时 30 个请求触发 API 限流。
**Alternatives**:
- 串行拉取（当前方案）— 30 × 1s = 30s，不可接受
- 无限制并发 — 可能被 API 封 IP

## Risks And Trade-Offs

| Risk | Mitigation |
|------|------------|
| MVC 拆分引入回归 bug | 288 现有测试保持全绿；每个 Controller 单独测试 |
| 回调链过长导致调试困难 | 每个 Controller 的回调 ≤ 3 个，命名包含 `on_` 前缀 |
| 连接池单例在测试中污染状态 | `get_shared_client` 支持 `reset=True` 用于测试清理 |
| Bot 并发锁可能丢失消息 | `lock.locked()` 检查后直接返回，下一轮 3s 后重新处理 |
| 删除 `bot_server.py` 影响未知引用 | 删除前 `rg "bot_server"` 全量扫描 |
