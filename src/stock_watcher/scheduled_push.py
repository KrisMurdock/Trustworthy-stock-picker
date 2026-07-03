"""飞书定时推送 — 开盘提醒、收盘汇总、定时简报."""

from __future__ import annotations

import asyncio
import datetime as dt
import logging
from typing import TYPE_CHECKING

from stock_watcher.config import ChatConfig, NewsConfig
from stock_watcher.scheduler import (
    TradingCalendar,
    is_a_share_trading_time,
    is_hk_trading_time,
)

if TYPE_CHECKING:
    from stock_watcher.models import StockQuote, Position

logger = logging.getLogger(__name__)

CST = dt.timezone(dt.timedelta(hours=8))


class ScheduledPush:
    """管理所有定时推送：开盘、收盘、简报."""

    def __init__(
        self,
        chat_cfg: ChatConfig,
        news_cfg: NewsConfig,
        get_quotes: callable,
        get_positions: callable,
        get_news_stats: callable | None = None,
    ) -> None:
        self._chat = chat_cfg
        self._news = news_cfg
        self._get_quotes = get_quotes
        self._get_positions = get_positions
        self._get_news_stats = get_news_stats
        self._calendar = TradingCalendar()
        self._last_push: dict[str, str] = {}  # push_type → date_str

    # ------------------------------------------------------------------
    # 主循环
    # ------------------------------------------------------------------

    async def run(self, stop_event: asyncio.Event) -> None:
        """主循环：每分钟检查一次到期的推送."""
        logger.info("scheduled_push started")
        while not stop_event.is_set():
            try:
                now_cst = dt.datetime.now(CST)
                today_str = now_cst.strftime("%Y%m%d")
                time_str = now_cst.strftime("%H:%M")

                await self._check_and_fire("morning_briefing", today_str, time_str)
                await self._check_and_fire("market_open", today_str, time_str)
                await self._check_and_fire("a_share_close", today_str, time_str)
                await self._check_and_fire("hk_close", today_str, time_str)
                await self._check_and_fire("evening_briefing", today_str, time_str)

            except Exception as exc:
                logger.warning("scheduled_push tick error: %s", exc)

            await asyncio.sleep(60)  # tick every minute

    async def _check_and_fire(
        self, push_type: str, today: str, now_time: str
    ) -> None:
        """如果配置的时间到了且今天还没推送过，则触发."""
        sp = self._news.scheduled_push.get(push_type)
        if not sp or not sp.enabled:
            return
        target = sp.time
        if now_time < target:
            return
        if self._last_push.get(push_type) == today:
            return  # 今天已推送

        # 如果是交易时段相关的推送，检查交易日历
        if push_type in ("market_open", "a_share_close") and not is_a_share_trading_time():
            return
        if push_type == "hk_close" and not is_hk_trading_time():
            return

        self._last_push[push_type] = today
        asyncio.create_task(self._fire(push_type))

    async def _fire(self, push_type: str) -> None:
        """执行具体推送."""
        try:
            quotes = self._get_quotes() if callable(self._get_quotes) else {}
            positions = self._get_positions() if callable(self._get_positions) else {}

            if push_type in ("market_open", "a_share_close", "hk_close"):
                await self._push_price_card(push_type, quotes, positions)
            elif push_type in ("morning_briefing", "evening_briefing"):
                await self._push_briefing_card(push_type)
        except Exception as exc:
            logger.error("scheduled_push fire %s failed: %s", push_type, exc)

    # ------------------------------------------------------------------
    # 价格卡片推送
    # ------------------------------------------------------------------

    async def _push_price_card(
        self,
        push_type: str,
        quotes: dict[str, "StockQuote"],
        positions: dict[str, "Position"],
    ) -> None:
        """推送行情卡片（开盘/收盘）."""
        from stock_watcher.chat_sender import build_summary_card, send_feishu_card

        labels = {
            "market_open": "📈 开盘提醒",
            "a_share_close": "📉 A股收盘汇总",
            "hk_close": "📉 港股收盘汇总",
        }
        label = labels.get(push_type, push_type)

        card = await build_summary_card(quotes, positions)
        # Override header title
        card["card"]["header"]["title"]["content"] = label
        ok = await send_feishu_card(self._chat, card)
        if ok:
            logger.info("scheduled_push: %s sent", push_type)
        else:
            logger.warning("scheduled_push: %s send failed", push_type)

    # ------------------------------------------------------------------
    # 简报推送
    # ------------------------------------------------------------------

    async def _push_briefing_card(self, push_type: str) -> None:
        """推送简报卡片."""
        from stock_watcher.chat_sender import send_news_card

        labels = {
            "morning_briefing": "☀️ 早间简报",
            "evening_briefing": "🌙 晚间简报",
        }
        label = labels.get(push_type, push_type)
        now_str = dt.datetime.now(CST).strftime("%H:%M")

        # Get news stats from aggregator
        stats = {}
        if callable(self._get_news_stats):
            stats = self._get_news_stats()
        fetched = stats.get("fetched", 0)
        filtered = stats.get("filtered", 0)
        critical = stats.get("critical", 0)

        if critical > 0:
            stat_line = f"📊 抓取 {fetched} 条 → 过滤 {filtered} 条 → **{critical} 条重大**"
        else:
            stat_line = f"📊 抓取 {fetched} 条，过滤 {filtered} 条，无重大新闻"

        card = {
            "msg_type": "interactive",
            "card": {
                "header": {
                    "title": {"tag": "plain_text", "content": label},
                    "template": "blue",
                },
                "elements": [
                    {"tag": "div", "text": {"tag": "lark_md",
                        "content": f"🕐 {now_str}"}},
                    {"tag": "hr"},
                    {"tag": "div", "text": {"tag": "lark_md",
                        "content": stat_line}},
                ],
            },
        }
        await send_news_card(self._chat, card)
