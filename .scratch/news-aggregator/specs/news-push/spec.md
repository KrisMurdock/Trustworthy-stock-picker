# Spec: 新闻飞书推送（含板块增强） (News Push)

## Overview

将已分类为「重大」的新闻构建为飞书卡片消息，嵌入板块概览和分级个股实时行情（持仓Tier1/自选Tier2/趋势Tier3），通过 Webhook 推送。

---

## Requirements

### NP-S01: 新闻卡片格式（板块增强）

SHALL build Feishu interactive card with:
- **Header**: 蓝色模板，标题 "📰 {板块名} 异动" 或 "📰 重大新闻"（无板块匹配时）
- **板块概览行**（有板块匹配时）: 板块名称 + 板块涨跌幅 + 板块领涨股
- **Body elements**（按 Tier 分组）:
  - `📌 你的持仓`（Tier 1）: 每行 `名称 代码 涨跌幅% 最新价 H:最高 L:最低 O:今开`
  - `👁 你关注的`（Tier 2）: 同上格式
  - `🔥 趋势最强`（Tier 3）: 同上格式，标注 AI 置信度
- **新闻原文**: 标题（加粗）+ 来源 + 时间
- **原文链接**: `[查看详情](url)`

Card SHALL be structurally valid Feishu card JSON (validated against schema).

SHALL NOT display Tier labels when that tier is empty.

SHALL cap at 8 stocks total across all tiers.

#### Scenario: 完整板块增强卡片

- **WHEN** news matched sector "机器人执行器", Tier1 has 1 position, Tier2 has 1 watchlist, TrendRanker returns 5 Tier3 stocks
- **THEN** card SHALL show: Header "📰 机器人执行器 异动" + sector overview + Tier1(📌) 1 stock + Tier2(👁) 1 stock + Tier3(🔥) 5 stocks = total 7 stocks

### NP-S02: 聚合去重推送

SHALL group news items by matched sector.
SHALL send one card per sector-news pair (multi-stock within card).
SHALL implement per-sector push dedup: same sector + same event → one push per 30 minutes.
SHALL NOT send separate cards for each news item within same sector event.
SHALL skip sector-enrichment entirely when no sector is matched (fall back to basic news card).

### NP-S03: 推送频率控制

SHALL track push count per minute.
SHALL cap at 10 pushes per minute (well under Feishu 100/min limit).
SHALL queue overflow news for next minute bucket.

### NP-S04: 与现有推送的关系

SHALL use `chat_sender.send_news_card()` for news push (separate webhook from price/alerts).
SHALL NOT modify existing alert/price/summary card logic.
SHALL be a new consumer of the chat infrastructure.
SHALL use `cfg.news_webhook` and `cfg.news_secret` (independent from main webhook).

---

## Acceptance Criteria

- [ ] Card JSON passes Feishu card schema validation
- [ ] Sector-enhanced card shows sector overview + tiered stock list with full quote data
- [ ] Tier labels (📌/👁/🔥) only shown when tier is non-empty
- [ ] Stock cap at 8 across all tiers enforced
- [ ] Per-sector 30-min dedup works
- [ ] Rate cap 10/min enforced
- [ ] Overflow queue drains on next minute
- [ ] Falls back to basic card when no sector matched
