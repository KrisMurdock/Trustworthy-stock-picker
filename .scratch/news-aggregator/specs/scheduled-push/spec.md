# Spec: 飞书定时推送 (Scheduled Push)

## Overview

补全飞书的定时推送能力：开盘提醒、收盘汇总、定时简报。

---

## Requirements

### SP-S01: 交易日历集成

SHALL use existing `TradingCalendar` to determine push windows.
SHALL push only on trading days (Mon–Fri).
SHALL skip pushes on detected holidays (reuse existing calendar logic).

### SP-S02: 开盘提醒 (09:25 CST)

SHALL push at 09:25 on A-share trading days.
Content:
- 自选股当前开盘价、涨跌幅
- 持仓股开盘市值概览
- 格式：复用 `build_price_card` 或轻量版本

SHALL NOT push if watchlist is empty.

### SP-S03: A股收盘汇总 (15:05 CST)

SHALL push at 15:05 on A-share trading days.
Content:
- 持仓总盈亏、总市值
- 各持仓股当日表现（涨跌幅、日内高低）
- 格式：复用 `build_summary_card`

### SP-S04: 港股收盘汇总 (16:15 CST)

SHALL push at 16:15 on HK trading days.
Same content format as SP-S03, only for HK stocks.
SHALL include HKD→CNY conversion in totals.

### SP-S05: 定时简报 (09:00 / 20:00 CST)

SHALL push at 09:00 (覆盖昨夜+今晨新闻) and 20:00 (覆盖白天新闻).
Content:
- 过去 12 小时内分类为 P0 的新闻汇总
- 如果没有 P0 新闻，发送 "无重大新闻" 简短提示
- 格式：简报卡片，每条新闻标题 + 一句话摘要 + 链接

SHALL NOT push if no new P0 news in window (skip entirely).

### SP-S06: 配置化

All push times SHALL be configurable in `config.yaml`:
```yaml
news:
  scheduled_push:
    morning_briefing: "09:00"
    market_open: "09:25"
    a_share_close: "15:05"
    hk_close: "16:15"
    evening_briefing: "20:00"
```

Each push type SHALL have individual `enabled: true/false` toggle.

### SP-S07: 调度器实现

SHALL implement as a lightweight timer-based scheduler within existing async loop.
SHALL use `asyncio.create_task` with `asyncio.sleep` to next target time.
SHALL recalculate next push time after each successful push.
SHALL handle missed pushes gracefully (e.g., app started at 10:00 → skip 09:25 push, don't fire immediately).

---

## Acceptance Criteria

- [ ] 09:25 opening push fires on A-share trading days
- [ ] 15:05 closing push includes all position P&L
- [ ] 16:15 HK closing push fires on HK trading days
- [ ] 09:00 / 20:00 briefing pushes news from past 12 hours
- [ ] Missed push (app started after time) does not retroactively fire
- [ ] Individual push types can be disabled via config
- [ ] Config file hot-reload picks up schedule changes
