# Execution Contract: 项目全局优化 (scope-refactor)

> **Change**: `scope-refactor`
> **Generated**: 2025-07-03
> **Status**: ⏳ awaiting approval (DP-3)

---

## 1. Intent Lock

**Problem**: `app.py` 2270 行上帝类混合所有职责，日志系统静默，HTTP 连接无复用，异常被静默吞噬，bot_server.py 为死代码，新闻扇区串行阻塞。

**Solution**: MVC 深度重构（4 个 Controller）+ 7 项基础设施修复（日志/连接池/异常处理/死代码/配置/并发锁/扇区并发）。

**Scope fence**: 不改密钥管理、不引入数据库、288 现有测试必须全绿。

---

## 2. Approved Behavior Summary

### 基础设施修复 (7 requirements)

| ID | Requirement | Task |
|----|-------------|:--:|
| IF-S01 | `main()` 调用 `logging.basicConfig`，日志输出到 stderr | T01 |
| IF-S02 | 共享 `httpx.AsyncClient` 单例 `get_shared_client()` | T02 |
| IF-S03 | 所有 `except: pass` → `logger.error/warning(exc_info=True)` | T09 |
| IF-S04 | 删除 `bot_server.py`，零引用残留 | T10 |
| IF-S05 | `NewsConfig.from_dict` 默认值 120→300.0 | T03 |
| IF-S06 | `BotController.poll_once()` 使用 `asyncio.Lock` | T07 |
| IF-S07 | 新闻历史数据 `asyncio.gather` + `Semaphore(5)` 并发 | T12 |

### MVC 架构拆分 (6 requirements)

| ID | Requirement | Task |
|----|-------------|:--:|
| MVC-S01 | 创建 `controllers/` 目录，4 个 controller + `__init__.py` | T04–T07 |
| MVC-S02 | `PollController` — 行情轮询 + 告警判断，通过 `on_quote` 回调解耦 | T05 |
| MVC-S03 | `NewsController` — 新闻管道完整封装，通过 `on_push` 回调外发 | T06 |
| MVC-S04 | `BotController` — Bot 轮询 + `asyncio.Lock` 防重入 | T07 |
| MVC-S05 | `AlertController` — 告警 CRUD + 持久化 | T04 |
| MVC-S06 | `app.py` ≤ 600 行，纯 TUI 渲染 + 控制器编排 | T08 |

---

## 3. Out of Scope

- 密钥管理（用户确认保留现有形式）
- 低优先级问题 #10-#15（测试覆盖、依赖锁、配置校验等）
- 新功能开发
- 数据库引入

---

## 4. Constraints

| 约束 | 来源 |
|------|------|
| 288 现有测试必须全绿，零回归 | proposal |
| `StockWatcherApp.run()` 签名不变 | proposal |
| `run.sh` 启动方式不变 | proposal |
| 配置热加载行为不变 | proposal |
| Controller 通过回调与 App 通信，不持有 App 引用 | D2 |
| `get_shared_client()` 模块级单例，支持 `reset` 用于测试 | D3 |
| 日志输出到 stderr，不干扰 Textual stdout | D4 |
| 异常处理使用 `exc_info=True` 保留完整堆栈 | D5 |
| 新闻扇区 Semaphore(5) 限流 | D6 |

---

## 5. Execution Batches

### Batch 1 — Infrastructure Foundation (T01–T03)
- **T01**: `logging.basicConfig` in `main()`
- **T02**: `http_client.py` shared `AsyncClient` singleton
- **T03**: Fix `NewsConfig.from_dict` default

**Done when**: T01 → stderr shows log lines on startup; T02 → singleton test passes; T03 → config default confirmed.

### Batch 2 — Controller Extraction (T04–T08)
- **T04**: AlertController
- **T05**: PollController
- **T06**: NewsController
- **T07**: BotController
- **T08**: Slim app.py, wire controllers

**Done when**: 5 controller tests pass; `wc -l app.py` ≤ 600; 288 existing tests green.

### Batch 3 — Cleanup + Perf (T09–T12)
- **T09**: Replace `except: pass` with logged warnings
- **T10**: Delete `bot_server.py`
- **T11**: Replace direct `httpx.AsyncClient()` with `get_shared_client()` in fetcher.py
- **T12**: Parallelize news sector historical data fetch

**Done when**: `rg "except.*pass" src/` → 0; `rg bot_server src/` → 0; fetcher tests pass; full suite 288 green.

---

## 6. Test Obligations

| Requirement | Test Type | Test File |
|-------------|-----------|-----------|
| IF-S01 logging | manual (stderr inspection) | — |
| IF-S02 shared client | unit | `tests/test_http_client.py` |
| IF-S05 config default | unit (inline) | — |
| MVC-S02 PollController | unit + async | `tests/test_poll_controller.py` |
| MVC-S03 NewsController | unit | `tests/test_news_controller.py` |
| MVC-S04 BotController | unit + async | `tests/test_bot_controller.py` |
| MVC-S05 AlertController | unit | `tests/test_alert_controller.py` |
| MVC-S06 app line count | manual (`wc -l`) | — |
| IF-S03 except:pass | manual (`rg`) | — |
| IF-S04 dead code | manual (`rg`) | — |
| 288 regression | integration | entire test suite |

---

## 7. Review Gates

| Gate | When | What |
|------|------|------|
| **R1** | After Batch 1 | `get_shared_client()` singleton test passes; config default verified |
| **R2** | After Batch 2 | All 5 controller tests pass; `wc -l app.py` ≤ 600; 288 regression green |
| **R3** | After Batch 3 | `rg "except.*pass"` = 0; `rg bot_server` = 0; 288 regression green |

---

## 8. Rewind Triggers

Stop and return to planning if:

- Any controller extraction breaks ≥ 5 existing tests (re-evaluate MVC boundary)
- `app.py` cannot be reduced below 800 lines without breaking behavior (reduce MVC scope)
- `bot_server.py` has callers outside the project that would break
- `get_shared_client()` singleton causes test pollution that can't be cleaned with reset
- Semaphore(5) causes deadlock in news pipeline

---

## 9. Coverage Cross-Check

**Result: ALL COVERED** ✅

| Spec | Requirements | Tasks | Tests | Status |
|------|:------------:|:-----:|:-----:|:------:|
| infrastructure-fixes | 7 (IF-S01–S07) | T01,T02,T03,T09,T10,T12 | 1 + 6 manual | ✅ |
| mvc-refactor | 6 (MVC-S01–S06) | T04–T08 | 4 unit files | ✅ |
| **Total** | **13** | **12 tasks** | **5 test files + 6 manual checks** | ✅ |

---

## 10. Escalation Rules

| Situation | Action |
|-----------|--------|
| Controller callback signature mismatch | Check `on_quote(SectorStock)` vs expected type; adjust callback |
| `get_shared_client()` leaks between tests | Call `close_shared_client()` in test `teardown_function` |
| NewsController test requires live akshare | Use mock `NewsConfig` with all sources disabled |
| `wc -l app.py` > 600 after extraction | Accept up to 700 if remaining lines are unavoidable TUI boilerplate |
| `except: pass` replacement breaks a test | Preserve the `pass` if it's intentionally ignoring a non-critical path |
