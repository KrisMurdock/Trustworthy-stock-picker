"""Feishu bot HTTP polling event listener + DeepSeek AI reply.

Polls the Feishu IM API for new @mention messages, gathers stock context,
sends to DeepSeek, and replies in-thread.
"""

from __future__ import annotations

import asyncio
import json
import urllib.parse
from typing import TYPE_CHECKING

import httpx

from stock_watcher.config import ChatConfig, DeepSeekConfig

if TYPE_CHECKING:
    from stock_watcher.models import Position, StockQuote

FEISHU_API = "https://open.feishu.cn/open-apis"
POLL_INTERVAL = 3  # seconds between polls

# ---------------------------------------------------------------------------
# Access token
# ---------------------------------------------------------------------------


async def _get_tenant_token(app_id: str, app_secret: str) -> str | None:
    url = f"{FEISHU_API}/auth/v3/tenant_access_token/internal"
    try:
        async with httpx.AsyncClient(timeout=10) as client:
            resp = await client.post(url, json={
                "app_id": app_id,
                "app_secret": app_secret,
            })
        if resp.status_code == 200:
            data = resp.json()
            if data.get("code") == 0:
                return data["tenant_access_token"]
    except Exception:
        pass
    return None


# ---------------------------------------------------------------------------
# Polling loop
# ---------------------------------------------------------------------------


async def run_bot_server(
    chat_cfg: ChatConfig,
    deepseek_cfg: DeepSeekConfig,
    get_quotes: callable,
    get_positions: callable,
    get_hkd_rate: callable,
    notify: callable,
    stop_event: asyncio.Event,
) -> None:
    """Main loop: poll Feishu IM for @mentions, reply via LLM."""
    if not chat_cfg.can_receive:
        notify("[bot] 缺少 feishu_app_id / feishu_app_secret，跳过")
        return

    seen_msg_ids: set[str] = set()
    chat_ids: set[str] | None = None  # lazily discovered

    notify("[bot] 轮询已启动 (3s interval)")

    while not stop_event.is_set():
        token = await _get_tenant_token(chat_cfg.feishu_app_id, chat_cfg.feishu_app_secret)
        if not token:
            await asyncio.sleep(10)
            continue

        # Discover chat IDs if we haven't yet
        if chat_ids is None:
            chat_ids = await _discover_chats(token)
            if chat_ids:
                notify(f"[bot] 发现 {len(chat_ids)} 个群聊")

        # Poll each chat for new messages
        for chat_id in list(chat_ids or []):
            messages = await _list_messages(token, chat_id)
            for msg in messages:
                msg_id = msg.get("message_id", "")
                if msg_id in seen_msg_ids:
                    continue
                seen_msg_ids.add(msg_id)

                # Only process @mentions
                if not _is_at_bot(msg):
                    continue

                text = _extract_text(msg)
                notify(f"[bot] 收到提问：{text[:80]}")

                asyncio.create_task(
                    _handle_question(
                        msg_id, token, text, deepseek_cfg,
                        get_quotes, get_positions, get_hkd_rate, notify,
                    )
                )

        # Keep seen set from growing unbounded
        if len(seen_msg_ids) > 1000:
            seen_msg_ids = set(list(seen_msg_ids)[-500:])

        await asyncio.sleep(POLL_INTERVAL)


# ---------------------------------------------------------------------------
# Chat discovery
# ---------------------------------------------------------------------------


async def _discover_chats(token: str) -> set[str]:
    """List all group chats the bot is in."""
    chat_ids: set[str] = set()
    page_token: str | None = None
    try:
        while True:
            params = {"page_size": 20}
            if page_token:
                params["page_token"] = page_token
            async with httpx.AsyncClient(timeout=10) as client:
                resp = await client.get(
                    f"{FEISHU_API}/im/v1/chats",
                    params=params,
                    headers={"Authorization": f"Bearer {token}"},
                )
            if resp.status_code != 200:
                break
            data = resp.json()
            if data.get("code") != 0:
                break
            for item in data.get("data", {}).get("items", []):
                chat_ids.add(item["chat_id"])
            if not data.get("data", {}).get("has_more"):
                break
            page_token = data.get("data", {}).get("page_token", "")
            if not page_token:
                break
    except Exception:
        pass
    return chat_ids


# ---------------------------------------------------------------------------
# Message listing
# ---------------------------------------------------------------------------


async def _list_messages(token: str, chat_id: str, limit: int = 5) -> list[dict]:
    try:
        async with httpx.AsyncClient(timeout=10) as client:
            resp = await client.get(
                f"{FEISHU_API}/im/v1/messages",
                params={
                    "container_id_type": "chat",
                    "container_id": chat_id,
                    "page_size": limit,
                    "sort_type": "ByCreateTimeDesc",
                },
                headers={"Authorization": f"Bearer {token}"},
            )
        if resp.status_code == 200:
            data = resp.json()
            if data.get("code") == 0:
                return data.get("data", {}).get("items", [])
    except Exception:
        pass
    return []


# ---------------------------------------------------------------------------
# Message helpers
# ---------------------------------------------------------------------------


def _is_at_bot(msg: dict) -> bool:
    """Check if the message @mentions the bot."""
    for mention in msg.get("mentions", []):
        name = mention.get("name", "").lower()
        if any(kw in name for kw in ("机器人", "bot", "stock", "watcher")):
            return True
    # Fallback: check text content for @mention
    text = _extract_text(msg)
    return any(kw in text.lower() for kw in ("@", "机器人", "bot"))


def _extract_text(msg: dict) -> str:
    """Extract plain text from a Feishu message object."""
    content = msg.get("content", "")
    if not content:
        # body.content might be the raw JSON text
        body = msg.get("body", {})
        content = body.get("content", "")
    try:
        inner = json.loads(content)
        # Could be {"text": "..."} or {"elements": [...]}
        if isinstance(inner, dict):
            if "text" in inner:
                return inner["text"]
            if "elements" in inner:
                return "".join(
                    e.get("text", "") for e in inner["elements"]
                    if isinstance(e, dict)
                )
    except (json.JSONDecodeError, TypeError):
        pass
    return str(content)


# ---------------------------------------------------------------------------
# Question handler
# ---------------------------------------------------------------------------


async def _handle_question(
    message_id: str,
    token: str,
    text: str,
    deepseek_cfg: DeepSeekConfig,
    get_quotes: callable,
    get_positions: callable,
    get_hkd_rate: callable,
    notify: callable,
) -> None:
    quotes = get_quotes()
    positions = get_positions()
    hkd_rate = get_hkd_rate()

    from stock_watcher.deepseek_chat import _build_context_from_store, ask_deepseek

    context = _build_context_from_store(quotes, positions, hkd_rate)
    answer = await ask_deepseek(deepseek_cfg, context, text)

    success = await _reply_message(token, message_id, answer)
    if success:
        notify(f"[bot] ✓ 已回复 ({len(answer)}字)")
    else:
        notify("[bot] ✗ 回复失败")


# ---------------------------------------------------------------------------
# Reply
# ---------------------------------------------------------------------------


async def _reply_message(token: str, message_id: str, content: str) -> bool:
    url = f"{FEISHU_API}/im/v1/messages/{urllib.parse.quote(message_id, safe='')}/reply"
    body = {"content": json.dumps({"text": content}), "msg_type": "text"}
    try:
        async with httpx.AsyncClient(timeout=10) as client:
            resp = await client.post(
                url, json=body,
                headers={"Authorization": f"Bearer {token}"},
            )
        return resp.status_code == 200 and resp.json().get("code") == 0
    except Exception:
        return False
