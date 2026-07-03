# Spec: 新闻重要性分类 (News Classification)

## Overview

使用 DeepSeek AI 对新闻进行三分类（重大/关注/忽略），仅「重大」级新闻触发飞书推送。

---

## Requirements

### NC-S01: 三分类体系

SHALL classify each `NewsItem` into exactly one of:

| 级别 | 标签 | 推送 | 定义 |
|------|------|:----:|------|
| P0 | `critical` | ✅ | 直接影响持仓/自选股的重大事件（如并购重组、业绩暴雷、政策突变） |
| P1 | `notable` | ❌ | 值得关注但无需立即行动（如行业动态、高管变动） |
| P2 | `ignore` | ❌ | 常规报道、软文、重复信息 |

### NC-S02: 分类 Prompt

SHALL use structured prompt with:
- System: role definition + classification criteria + output format
- User: stock context (name, current price, change%) + news title + summary
- Temperature: 0.1 (deterministic classification)
- Max tokens: 50 (just the label)

Expected output format: `{"level": "critical|notable|ignore", "reason": "一句话理由"}`

### NC-S03: 批量分类

SHALL classify news items in batches of ≤ 5 (one API call).
SHALL implement retry (max 2 attempts) with 1s backoff on API failure.
SHALL return `notable` as safe default when API is unavailable.
SHALL NOT exceed 30 API calls per minute (rate limiting).

### NC-S04: 缓存机制

SHALL cache classification results by `news_id` in memory (dict) for current session.
SHALL NOT persist classification cache across restarts (news freshness matters).

### NC-S05: 关键词预筛选

Before calling AI, SHALL pre-filter using configurable keyword lists:
- `critical_keywords`: 强制 P0（如 "停牌", "退市", "重大资产重组"）
- `ignore_keywords`: 强制 P2（如 "龙虎榜", "大宗交易", "融资融券" 等常规数据披露）

Keywords SHALL be case-insensitive and match title or summary.
Keyword match SHALL short-circuit AI call.

---

## Acceptance Criteria

- [ ] Classifier returns correct JSON structure
- [ ] Batch of 10 items classified within 5 seconds (including API calls)
- [ ] Keyword pre-filter short-circuits correctly (critical keyword → P0, ignore keyword → P2)
- [ ] API failure → returns `notable` as default
- [ ] API retry works (2 attempts, 1s backoff)
- [ ] Rate limiter prevents > 30 calls/minute
