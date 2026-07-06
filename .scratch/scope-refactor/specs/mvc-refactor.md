# Spec: MVC 架构拆分 (MVC Refactor)

## Overview

将 `app.py` (2270 行) 深度重构为 MVC 架构：`app.py` 保留纯 TUI 渲染层，业务逻辑提取到 4 个独立控制器。

---

## Requirements

### MVC-S01: 目录结构

SHALL 创建 `src/stock_watcher/controllers/` 目录，包含：

```
controllers/
├── __init__.py              # 控制器统一导出
├── poll_controller.py       # 行情轮询 + 告警判断
├── news_controller.py       # 新闻聚合 + 板块增强 + 推送
├── bot_controller.py        # 飞书 Bot 轮询 + DeepSeek 回复
└── alert_controller.py      # 告警规则管理
```

### MVC-S02: PollController

SHALL 从 `app.py` 提取以下逻辑：
- `_poll_loop()` → `PollController.run(stop_event)`
- `_fetch_one(code)` → `PollController._fetch_one(code)`
- `_check_alerts(quote)` → `PollController._check_alerts(quote)`
- `_backoff` 管理器

SHALL 通过回调 `on_quote(quote)` 通知 app.py 更新 TUI，而非直接操作 `self._latest_quotes`。

#### Scenario: PollController 独立运行

- **WHEN** `PollController(cfg, on_quote=callback).run(stop)` 启动
- **THEN** 行情数据通过 `on_quote` 回调传递，不依赖 `StockWatcherApp` 实例

### MVC-S03: NewsController

SHALL 从 `app.py` 提取：
- `_news_loop()` → `NewsController.run(stop_event)`
- 所有新闻管道组件（Aggregator / Classifier / SectorMatcher / TrendScorer / TrendRanker / CardBuilder）
- `_news_stats` 状态

SHALL 通过回调 `on_push(card)` 通知 app.py 推送飞书卡片。

#### Scenario: NewsController 独立运行

- **WHEN** `NewsController(cfg, on_push=callback).run(stop)` 启动
- **THEN** 新闻轮询 + 分类 + 板块增强 + 卡片构建完整执行，卡片通过回调外发

### MVC-S04: BotController

SHALL 从 `app.py` 提取：
- `_bot_poll_once()` → `BotController.poll_once()`
- Bot 相关的 DeepSeek 对话逻辑
- `_bot_seen_ids` 状态
- `asyncio.Lock` 并发保护

SHALL 通过 `app.py` 的 `set_interval(3.0, ...)` 调度，但自身不依赖 Textual。

#### Scenario: BotController 带锁防重入

- **WHEN** `BotController.poll_once()` 并发调用
- **THEN** `asyncio.Lock` SHALL 保证单次执行

### MVC-S05: AlertController

SHALL 从 `app.py` 提取：
- `_add_alert()` → `AlertController.add(code, alert_type, value)`
- `_remove_alert()` → `AlertController.remove(code, alert_type)`
- `_update_alert_codes()` → `AlertController.update_codes(watchlist)`
- 告警历史持久化

SHALL 返回 `list[AlertRule]` 供 PollController 消费，不直接操作 `app._alerts`。

### MVC-S06: App 精简

重构后 `app.py` SHALL：
- 仅保留 TUI 渲染（`compose`, `on_mount`, 状态栏, 键盘绑定）
- 在 `on_mount` 中创建 4 个 controller 实例并启动
- 通过回调接收数据更新 `self._latest_quotes` 等 TUI 状态
- SHALL ≤ 600 行

#### Scenario: App 行数验证

- **WHEN** `wc -l app.py` 执行
- **THEN** 行数 SHALL ≤ 600
