"""巨潮资讯网公告飞书推送卡片 + DeepSeek 摘要."""

from __future__ import annotations

import datetime as dt
from typing import Optional

import httpx

from stock_watcher.config import ChatConfig, DeepSeekConfig
from stock_watcher.models import Announcement

DEEPSEEK_TIMEOUT = 20

_SUMMARIZE_PROMPT = """你是一个上市公司公告分析助手。请用 1-2 句简洁的中文总结以下公告的核心内容。

规则：
1. 只输出摘要，不要输出标题或任何前缀
2. 突出对投资者的影响（利好/利空/中性）
3. 每句不超过 40 个字
"""


# ---------------------------------------------------------------------------
# 飞书卡片构建
# ---------------------------------------------------------------------------


def _cell(width: str, md: str) -> dict:
    return {
        "tag": "column",
        "width": width,
        "elements": [{"tag": "div", "text": {"tag": "lark_md", "content": md}}],
    }


def _row(cells: list[dict]) -> dict:
    return {"tag": "column_set", "flex_mode": "none", "columns": cells}


def build_announcement_card(
    code: str,
    name: str,
    announcements: list[Announcement],
    summaries: dict[str, str] | None = None,
) -> dict:
    """Build a Feishu card for one or more announcements.

    Args:
        code: Stock code with prefix (e.g. "sh600519")
        name: Stock name
        announcements: List of new announcements
        summaries: Optional map of announcement id → AI summary
    """
    summaries = summaries or {}
    now_str = dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).strftime("%Y-%m-%d %H:%M")
    count = len(announcements)

    elements: list[dict] = [
        _row([
            _cell("weighted", f"📄 {code} **{name}**  —  {count} 条新公告  🕐 {now_str}"),
        ]),
        {"tag": "hr"},
    ]

    for ann in announcements:
        # Title line
        time_short = ann.announcement_time[-5:] if len(ann.announcement_time) >= 5 else ann.announcement_time
        elements.append({
            "tag": "div",
            "text": {
                "tag": "lark_md",
                "content": f"🕐 {time_short}　**{ann.title}**",
            },
        })

        # Summary line
        summary = summaries.get(ann.id, "")
        if summary:
            elements.append({
                "tag": "div",
                "text": {
                    "tag": "lark_md",
                    "content": f"📝 {summary}",
                },
            })

        # PDF link
        if ann.adjunct_url:
            pdf_url = ann.pdf_url
            elements.append({
                "tag": "div",
                "text": {
                    "tag": "lark_md",
                    "content": f"📎 [查看PDF]({pdf_url})",
                },
            })

        elements.append({"tag": "hr"})

    return {
        "msg_type": "interactive",
        "card": {
            "header": {
                "title": {"tag": "plain_text", "content": f"📰 {name} 公告"},
                "template": "blue",
            },
            "elements": elements,
        },
    }


# ---------------------------------------------------------------------------
# DeepSeek 摘要
# ---------------------------------------------------------------------------


async def summarize_announcement(
    cfg: DeepSeekConfig,
    title: str,
) -> str:
    """Use DeepSeek to summarize an announcement title into 1-2 sentences."""
    if not cfg.is_configured:
        return ""

    url = f"{cfg.api_base.rstrip('/')}/chat/completions"

    messages = [
        {"role": "system", "content": _SUMMARIZE_PROMPT},
        {"role": "user", "content": f"公告标题：{title}"},
    ]

    try:
        async with httpx.AsyncClient(timeout=DEEPSEEK_TIMEOUT) as client:
            resp = await client.post(
                url,
                json={
                    "model": cfg.model,
                    "messages": messages,
                    "temperature": 0.3,
                    "max_tokens": 150,
                },
                headers={
                    "Authorization": f"Bearer {cfg.api_key}",
                    "Content-Type": "application/json",
                },
            )
        if resp.status_code == 200:
            data = resp.json()
            return data["choices"][0]["message"]["content"].strip()
        return ""
    except Exception:
        return ""


async def summarize_batch(
    cfg: DeepSeekConfig,
    announcements: list[Announcement],
) -> dict[str, str]:
    """Summarize a batch of announcements, returning {id: summary}."""
    results: dict[str, str] = {}
    for ann in announcements:
        summary = await summarize_announcement(cfg, ann.title)
        if summary:
            results[ann.id] = summary
    return results


# ---------------------------------------------------------------------------
# 发送
# ---------------------------------------------------------------------------


# Re-use from chat_sender — avoid circular import at module level
async def send_announcement_card(cfg: ChatConfig, card: dict) -> bool:
    """Send a card message to Feishu. Returns True on success."""
    from stock_watcher.chat_sender import send_feishu_card

    return await send_feishu_card(cfg, card)
