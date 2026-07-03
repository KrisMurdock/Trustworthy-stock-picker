"""飞书新闻卡片构建."""

from __future__ import annotations

import datetime as dt

from stock_watcher.news.classifier import ClassifyResult, NewsLevel
from stock_watcher.models import NewsItem

SOURCE_LABELS: dict[str, str] = {
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
