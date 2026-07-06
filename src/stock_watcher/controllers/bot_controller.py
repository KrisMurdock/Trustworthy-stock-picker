"""飞书 Bot 控制器 — 消息轮询 + DeepSeek 回复 + 并发锁.

合并原 bot_server.py 的所有函数到此类中，消除死代码文件。
"""

from __future__ import annotations

import asyncio
import json
import logging
import urllib.parse
from typing import Callable

import httpx

from stock_watcher.config import ChatConfig, DeepSeekConfig

logger = logging.getLogger(__name__)

FEISHU_API = "https://open.feishu.cn/open-apis"


class BotController:
    """飞书 Bot 消息轮询 + AI 回复，带 asyncio.Lock 防重入."""

    def __init__(
        self,
        chat_cfg: ChatConfig,
        deepseek_cfg: DeepSeekConfig | None = None,
        get_quotes: Callable[[], dict] | None = None,
        get_positions: Callable[[], dict] | None = None,
        on_notify: Callable[[str], None] | None = None,
    ) -> None:
        self._chat = chat_cfg
        self._deepseek_cfg = deepseek_cfg
        self._get_quotes = get_quotes or (lambda: {})
        self._get_positions = get_positions or (lambda: {})
        self._on_notify = on_notify
        self._lock = asyncio.Lock()
        self._seen_ids: set[str] = set()
        self._chat_ids: set[str] | None = None

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def poll_once(self) -> None:
        """单次轮询，带锁防重入."""
        if self._lock.locked():
            return
        async with self._lock:
            await self._do_poll()

    async def _do_poll(self) -> None:
        """执行一次完整的 Bot 消息轮询."""
        if not self._chat or not self._chat.can_receive:
            return

        token = await self._get_tenant_token(
            self._chat.feishu_app_id, self._chat.feishu_app_secret
        )
        if not token:
            return

        if self._chat_ids is None:
            self._chat_ids = await self._discover_chats(token)
            if self._chat_ids and self._on_notify:
                self._on_notify(f"[bot] 发现 {len(self._chat_ids)} 个群聊")

        for chat_id in list(self._chat_ids or []):
            messages = await self._list_messages(token, chat_id)
            for msg in messages:
                msg_id = msg.get("message_id", "")
                if msg_id in self._seen_ids:
                    continue
                self._seen_ids.add(msg_id)

                if not self._is_at_bot(msg):
                    continue

                text = self._extract_text(msg)
                if self._on_notify:
                    self._on_notify(f"[bot] 收到提问：{text[:80]}")

                asyncio.create_task(
                    self._handle_question(msg_id, token, text)
                )

        if len(self._seen_ids) > 1000:
            self._seen_ids = set(list(self._seen_ids)[-500:])

    async def _handle_question(self, msg_id: str, token: str, text: str) -> None:
        quotes = self._get_quotes()
        positions = self._get_positions()

        from stock_watcher.chat_sender import _get_hkd_cny_rate
        from stock_watcher.deepseek_chat import _build_context_from_store, ask_deepseek

        hkd_rate = _get_hkd_cny_rate()
        context = _build_context_from_store(quotes, positions, hkd_rate)
        answer = await ask_deepseek(self._deepseek_cfg, context, text)

        ok = await self._reply_message(token, msg_id, answer)
        if self._on_notify:
            if ok:
                self._on_notify(f"[bot] ✓ 已回复 ({len(answer)}字)")
            else:
                self._on_notify("[bot] ✗ 回复失败")

    # ------------------------------------------------------------------
    # Feishu API helpers
    # ------------------------------------------------------------------

    async def _get_tenant_token(self, app_id: str, app_secret: str) -> str | None:
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
            logger.warning("BotController: get tenant token failed", exc_info=True)
        return None

    async def _discover_chats(self, token: str) -> set[str]:
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
            logger.warning("BotController: discover chats failed", exc_info=True)
        return chat_ids

    async def _list_messages(self, token: str, chat_id: str, limit: int = 5) -> list[dict]:
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
            logger.warning("BotController: list messages failed", exc_info=True)
        return []

    @staticmethod
    def _is_at_bot(msg: dict) -> bool:
        for mention in msg.get("mentions", []):
            name = mention.get("name", "").lower()
            if any(kw in name for kw in ("机器人", "bot", "stock", "watcher")):
                return True
        text = BotController._extract_text(msg)
        return any(kw in text.lower() for kw in ("@", "机器人", "bot"))

    @staticmethod
    def _extract_text(msg: dict) -> str:
        content = msg.get("content", "")
        if not content:
            body = msg.get("body", {})
            content = body.get("content", "")
        try:
            inner = json.loads(content)
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

    async def _reply_message(self, token: str, message_id: str, content: str) -> bool:
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
            logger.warning("BotController: reply failed", exc_info=True)
            return False
