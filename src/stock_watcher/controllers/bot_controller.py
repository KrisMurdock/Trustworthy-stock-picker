"""飞书 Bot 控制器 — 消息轮询 + DeepSeek 回复 + 并发锁."""

from __future__ import annotations

import asyncio
import logging
from typing import Callable

from stock_watcher.config import ChatConfig, DeepSeekConfig

logger = logging.getLogger(__name__)


class BotController:
    """飞书 Bot 消息轮询 + AI 回复，带 asyncio.Lock 防重入."""

    def __init__(
        self,
        chat_cfg: ChatConfig,
        deepseek_cfg: DeepSeekConfig | None = None,
        on_reply: Callable[[str, str], None] | None = None,
    ) -> None:
        self._chat = chat_cfg
        self._deepseek_cfg = deepseek_cfg
        self._on_reply = on_reply
        self._lock = asyncio.Lock()
        self._seen_ids: set[str] = set()

    async def poll_once(self) -> None:
        """单次轮询，带锁防重入."""
        if self._lock.locked():
            return
        async with self._lock:
            await self._do_poll()

    async def _do_poll(self) -> None:
        """执行一次 Bot 消息轮询."""
        if not self._chat or not self._chat.can_receive:
            return

        try:
            from stock_watcher.chat_sender import list_recent_messages
            msgs = await list_recent_messages(self._chat, limit=20)
        except Exception:
            logger.warning("BotController: list messages failed", exc_info=True)
            return

        for msg in msgs or []:
            msg_id = msg.get("message_id", "")
            if msg_id in self._seen_ids:
                continue
            self._seen_ids.add(msg_id)

            content = msg.get("content", "")
            if not content:
                continue

            try:
                reply = await self._ai_reply(content)
                if reply and self._on_reply:
                    self._on_reply(msg_id, reply)
            except Exception:
                logger.warning("BotController: AI reply failed", exc_info=True)

    async def _ai_reply(self, content: str) -> str:
        if not self._deepseek_cfg or not self._deepseek_cfg.is_configured:
            return ""
        # Stub — actual DeepSeek call would go here
        return ""
