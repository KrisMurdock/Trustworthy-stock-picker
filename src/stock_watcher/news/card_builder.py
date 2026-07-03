"""飞书新闻卡片构建."""

from __future__ import annotations

import datetime as dt

from stock_watcher.news.classifier import ClassifyResult, NewsLevel
from stock_watcher.news.trend_ranker import RankedResult
from stock_watcher.models import NewsItem

SOURCE_LABELS: dict[str, str] = {
    "eastmoney_stock": "东财个股",
    "eastmoney_flash": "东财快讯",
    "sina_flash": "新浪快讯",
    "cls_telegraph": "财联社",
    "yahoo_news": "雅虎财经",
    "sina_roll": "新浪财经",
    "sina_stock": "新浪个股",
    "yahoo_search": "雅虎财经",
}

SENTIMENT_STYLE: dict[str, str] = {
    "利好": "🟢",
    "利空": "🔴",
    "中性": "⚪",
}


def build_news_card(
    items: list[tuple[NewsItem, ClassifyResult]],
    fetched: int = 0,
) -> dict:
    """构建新闻批量推送卡片（含 AI 摘要 + 标的 + 多空）.

    Args:
        items: (NewsItem, ClassifyResult) 列表
        fetched: 本轮抓取总数
    """
    count = len(items)
    now_str = dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).strftime("%H:%M")

    # 统计
    critical_n = sum(1 for _, r in items if r.level == NewsLevel.CRITICAL)
    notable_n = sum(1 for _, r in items if r.level == NewsLevel.NOTABLE)
    ignore_n = sum(1 for _, r in items if r.level == NewsLevel.IGNORE)
    bullish_n = sum(1 for _, r in items if r.sentiment == "利好")
    bearish_n = sum(1 for _, r in items if r.sentiment == "利空")

    parts = [f"抓取 {fetched} 条", f"输出 {count} 条"]
    parts.append(f"🔴{critical_n} 🟡{notable_n} ⚪{ignore_n}")
    parts.append(f"🟢{bullish_n} 🔴{bearish_n}")

    elements: list[dict] = [
        {"tag": "div", "text": {"tag": "lark_md", "content": f"🕐 {now_str}　{'  '.join(parts)}"}},
        {"tag": "hr"},
    ]

    # 排序：critical > notable > ignore；利好优先
    def _sort_key(x: tuple[NewsItem, ClassifyResult]) -> tuple:
        _, r = x
        level_order = {NewsLevel.CRITICAL: 0, NewsLevel.NOTABLE: 1, NewsLevel.IGNORE: 2}
        sent_order = {"利好": 0, "利空": 1, "中性": 2}
        return (level_order.get(r.level, 9), sent_order.get(r.sentiment, 9))

    sorted_items = sorted(items, key=_sort_key)

    for item, result in sorted_items:
        level_emoji = {"critical": "🔴", "notable": "🟡", "ignore": "⚪"}.get(
            result.level.value, "⚪"
        )
        sent_emoji = SENTIMENT_STYLE.get(result.sentiment, "⚪")
        source_tag = SOURCE_LABELS.get(item.source, item.source)

        # 标题行
        title_text = f"{level_emoji} {sent_emoji} **{item.title}**"

        # 标签行：板块 + 个股
        tag_parts = []
        if result.sectors:
            tag_parts.append(f"板块：{', '.join(result.sectors[:5])}")
        if result.stocks:
            tag_parts.append(f"个股：{', '.join(result.stocks[:5])}")
        # 兼容：无 stocks/sectors 但有 entities
        if not tag_parts and result.entities:
            tag_parts.append(f"标的：{', '.join(str(e) for e in result.entities[:5])}")
        elif not tag_parts and item.related_codes:
            tag_parts.append(f"代码：{', '.join(item.related_codes[:5])}")

        elements.append({
            "tag": "div",
            "text": {"tag": "lark_md", "content": title_text},
        })

        if tag_parts:
            elements.append({
                "tag": "div",
                "text": {"tag": "lark_md", "content": f"　{'　'.join(tag_parts)}"},
            })

        # AI 摘要
        summary = result.summary or item.summary
        if summary and summary != item.title:
            elements.append({
                "tag": "div",
                "text": {
                    "tag": "lark_md",
                    "content": f"　{summary[:150]}",
                },
            })

        # 元信息：理由 + 来源 + 链接
        meta_parts = []
        if result.reason:
            meta_parts.append(f"💡 {result.reason}")
        meta_parts.append(f"📡 {source_tag}")
        if item.url:
            meta_parts.append(f"[原文]({item.url})")

        elements.append({
            "tag": "div",
            "text": {
                "tag": "lark_md",
                "content": "　" + "  │  ".join(meta_parts),
            },
        })

        elements.append({"tag": "hr"})

    return {
        "msg_type": "interactive",
        "card": {
            "header": {
                "title": {"tag": "plain_text", "content": f"📰 新闻速报 ({count}条)"},
                "template": "blue",
            },
            "elements": elements,
        },
    }


# ---------------------------------------------------------------------------
# Enriched card builder (sector + tiered stock quotes)
# ---------------------------------------------------------------------------

TIER_LABELS = {
    1: "📌 你的持仓",
    2: "👁 你关注的",
    3: "🔥 趋势最强",
}


def build_enriched_card(
    news: NewsItem,
    classify: ClassifyResult | None = None,
    sector: str | None = None,
    tiered: RankedResult | None = None,
    max_stocks: int = 8,
) -> dict:
    """Build a Feishu card with sector overview and tiered stock quotes.

    Args:
        news: The news item being pushed.
        classify: AI classification result (optional).
        sector: Matched sector name (None → fall back to basic card).
        tiered: Tiered stock list from TrendRanker (None → basic card).
        max_stocks: Max stocks to show across all tiers.

    Returns:
        Feishu interactive card JSON dict.
    """
    now_str = dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).strftime("%H:%M")
    source_tag = SOURCE_LABELS.get(news.source, news.source)

    # --- Determine card type ---
    if sector and tiered:
        return _build_sector_card(news, classify, sector, tiered, max_stocks, now_str, source_tag)
    else:
        return _build_basic_card(news, classify, now_str, source_tag)


def _build_sector_card(
    news: NewsItem,
    classify: ClassifyResult | None,
    sector: str,
    tiered: RankedResult,
    max_stocks: int,
    now_str: str,
    source_tag: str,
) -> dict:
    """Build the enriched sector + tiered stock card."""
    elements: list[dict] = []

    # Header info row
    elements.append({
        "tag": "div",
        "text": {"tag": "lark_md", "content": f"🕐 {now_str}  📡 {source_tag}  📰 {news.title}"},
    })
    elements.append({"tag": "hr"})

    # Sector overview
    elements.append({
        "tag": "div",
        "text": {"tag": "lark_md", "content": f"**板块：{sector}**"},
    })

    if classify and classify.reason:
        elements.append({
            "tag": "div",
            "text": {"tag": "lark_md", "content": f"💡 {classify.reason}"},
        })

    # Tiered stocks — cap at max_stocks total
    all_tiers = [(1, tiered.tier1), (2, tiered.tier2), (3, tiered.tier3)]
    shown = 0

    for tier_num, tier_stocks in all_tiers:
        if not tier_stocks:
            continue
        if shown >= max_stocks:
            break

        elements.append({
            "tag": "div",
            "text": {"tag": "lark_md", "content": TIER_LABELS[tier_num]},
        })

        for s in tier_stocks:
            if shown >= max_stocks:
                break
            # Format: 名称 代码 涨跌幅% 价格 H:高 L:低 O:开
            sign = "+" if s.change_pct > 0 else ""
            row = (
                f"　{s.name} {s.code} "
                f"{sign}{s.change_pct:.2f}% "
                f"¥{s.price:.2f} "
                f"H:{s.high:.2f} L:{s.low:.2f} O:{s.open:.2f}"
            )
            elements.append({
                "tag": "div",
                "text": {"tag": "lark_md", "content": row},
            })
            shown += 1

    elements.append({"tag": "hr"})

    # News link
    if news.url:
        elements.append({
            "tag": "div",
            "text": {"tag": "lark_md", "content": f"[查看原文]({news.url})"},
        })

    title = f"📰 {sector} 异动"
    return {
        "msg_type": "interactive",
        "card": {
            "header": {
                "title": {"tag": "plain_text", "content": title},
                "template": "blue",
            },
            "elements": elements,
        },
    }


def _build_basic_card(
    news: NewsItem,
    classify: ClassifyResult | None,
    now_str: str,
    source_tag: str,
) -> dict:
    """Fall back to basic single-news card when no sector matched."""
    elements: list[dict] = [
        {"tag": "div", "text": {"tag": "lark_md", "content": f"🕐 {now_str}  📡 {source_tag}"}},
        {"tag": "hr"},
        {"tag": "div", "text": {"tag": "lark_md", "content": f"**{news.title}**"}},
    ]

    if classify and classify.summary:
        elements.append({
            "tag": "div",
            "text": {"tag": "lark_md", "content": classify.summary[:200]},
        })

    if news.url:
        elements.append({
            "tag": "div",
            "text": {"tag": "lark_md", "content": f"[查看原文]({news.url})"},
        })

    return {
        "msg_type": "interactive",
        "card": {
            "header": {
                "title": {"tag": "plain_text", "content": "📰 重大新闻"},
                "template": "blue",
            },
            "elements": elements,
        },
    }
