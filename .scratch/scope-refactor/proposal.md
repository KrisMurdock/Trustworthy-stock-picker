# Proposal: 项目全局优化 (scope-refactor)

## Summary

修复 8 个代码质量问题：启用日志系统、MVC 深度重构 app.py 上帝类、引入共享 HTTP 连接池、删除死代码、修复静默异常吞噬、并发化新闻扇区拉取、对齐配置默认值、添加 Bot 轮询并发锁。

---

## Why

`app.py` 2270 行混合了 TUI 渲染、行情轮询、告警判断、新闻管道、Bot 通信、CSV 导出等所有职责，是项目最大单一脆弱点。日志系统完全静默导致线上故障不可见，HTTP 连接无复用浪费资源，10+ 处 `except: pass` 使运行时排查几乎不可能。这些问题在任一生产故障中都会放大调试成本。

---

## What Changes

### 基础设施修复

| # | 问题 | 变更 |
|---|------|------|
| 2 | 日志静默 | `main()` 调用 `logging.basicConfig(level=INFO)` |
| 4 | HTTP 连接池 | 新增 `http_client.py`，提供 `get_shared_client()` 返回单例 `httpx.AsyncClient` |
| 5 | Bot 死代码 | 删除 `bot_server.py`（已被 `app.py._bot_poll_once` 替代） |
| 7 | 新闻扇区阻塞 | `_news_loop` 中历史数据拉取从串行改为 `asyncio.gather` + `to_thread` 并发 |
| 8 | 配置默认值 | `NewsConfig.from_dict` 的 `poll_interval` 默认值从 120 改为 300.0 |
| 9 | Bot 并发锁 | `_bot_poll_once` 加 `asyncio.Lock` 防止重叠执行 |

### MVC 架构拆分

| 新文件 | 职责 | 从 app.py 移出 |
|--------|------|---------------|
| `controllers/poll_controller.py` | 行情轮询 + 告警判断 + backoff | `_poll_loop`, `_fetch_one`, `_check_alerts`, `_backoff` |
| `controllers/news_controller.py` | 新闻聚合 + 板块增强 + 推送 | `_news_loop`, `_push_news_batch` |
| `controllers/bot_controller.py` | 飞书 Bot 轮询 + DeepSeek 回复 | `_bot_poll_once`, `_bot_loop` |
| `controllers/alert_controller.py` | 告警规则 CRUD | `_add_alert`, `_remove_alert`, `_update_alert_codes` |
| `app.py` | 纯 TUI 渲染 + 控制器编排 | 保留 `compose`, `on_mount`, 状态栏, 按键处理, 热键 |

### 异常处理规范

- 所有现有 `except: pass` 和 `except Exception: pass` → `except Exception: logger.error/warning(...)`
- 涉及文件：`app.py`, `cninfo_fetcher.py`, `chat_sender.py`, `bot_server.py`（删除前检查）

---

## Scope

### In Scope

1. 日志系统启用
2. app.py MVC 拆分到 4 个 controller + 1 个 http_client
3. 删除 `bot_server.py`
4. 所有 `except: pass` → `logger.error/warning`
5. 新闻历史数据并发拉取
6. `NewsConfig.from_dict` 默认值修复
7. `_bot_poll_once` 加 `asyncio.Lock`

### Out of Scope

- 密钥管理（用户已确认保留现有形式）
- 低优先级问题 #10-#15（后续迭代）
- 新功能开发
- 数据库引入
- `deepseek_chat.py` / `chat_sender.py` / `bot_server.py` 的测试覆盖（#10）

---

## Capabilities

### New Capabilities

- **logging**: 所有模块输出结构化日志到 stderr
- **http-client**: 共享连接池，单例 `httpx.AsyncClient`
- **controllers**: 4 个独立控制器，各自职责边界清晰

### Modified Capabilities

- **app**: 降为纯 TUI 渲染层，从 2270 → ≤600 行
- **news-pipeline**: 历史数据拉取从串行改为并发
- **bot-polling**: 加并发锁防重叠
- **config**: 默认值对齐

### Removed Capabilities

- **bot-server**: `bot_server.py` 删除（死代码）

---

## Impact

### Affected Code
- `app.py` — 大幅删减，提取到 controllers/
- 新增 `controllers/` 目录（4 个模块 + `__init__.py`）
- 新增 `http_client.py`
- 删除 `bot_server.py`
- 修改 `config.py`（默认值对齐）
- 修改 `cninfo_fetcher.py`（异常日志）
- 修改 `chat_sender.py`（异常日志）

### Constraints
- 288 现有测试必须全绿，零回归
- 对外 API 不变：`StockWatcherApp.run()` 签名不变
- 配置热加载行为不变
- `run.sh` 启动方式不变
