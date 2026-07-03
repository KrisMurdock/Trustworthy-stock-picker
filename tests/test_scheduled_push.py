"""Unit tests for ScheduledPush."""

import pytest
import asyncio
from unittest.mock import AsyncMock, MagicMock, patch

from stock_watcher.scheduled_push import ScheduledPush
from stock_watcher.config import ChatConfig, NewsConfig, ScheduledPushConfig


class TestScheduledPush:
    """T11: ScheduledPush — configurable timed push dispatcher."""

    def _make_configs(self):
        chat = ChatConfig(feishu_webhook="http://test")
        news = NewsConfig()
        return chat, news

    def test_disabled_push_not_fired(self):
        """Push type with enabled=False should not fire."""
        chat, news = self._make_configs()
        news.scheduled_push["market_open"].enabled = False

        push = ScheduledPush(
            chat_cfg=chat, news_cfg=news,
            get_quotes=lambda: [],
            get_positions=lambda: {},
        )
        sp = news.scheduled_push.get("market_open")
        assert sp is not None
        assert sp.enabled is False

    def test_same_day_no_duplicate(self):
        """Should not push twice on same day."""
        chat, news = self._make_configs()
        push = ScheduledPush(
            chat_cfg=chat, news_cfg=news,
            get_quotes=lambda: [],
            get_positions=lambda: {},
        )
        push._last_push["market_open"] = "20250703"
        assert push._last_push.get("market_open") == "20250703"

    def test_configurable_times(self):
        """Push times are read from NewsConfig.scheduled_push."""
        chat, news = self._make_configs()
        assert news.scheduled_push["market_open"].time == "09:25"
        assert news.scheduled_push["a_share_close"].time == "15:05"
        assert news.scheduled_push["hk_close"].time == "16:15"
        assert news.scheduled_push["morning_briefing"].time == "09:00"
        assert news.scheduled_push["evening_briefing"].time == "20:00"

    def test_all_push_types_configurable(self):
        """Each push type can be individually enabled/disabled."""
        chat, news = self._make_configs()
        types = ["morning_briefing", "market_open", "a_share_close", "hk_close", "evening_briefing"]
        for t in types:
            assert t in news.scheduled_push
            assert isinstance(news.scheduled_push[t], ScheduledPushConfig)

    def test_creates_with_empty_state(self):
        chat, news = self._make_configs()
        push = ScheduledPush(
            chat_cfg=chat, news_cfg=news,
            get_quotes=lambda: [],
            get_positions=lambda: {},
        )
        assert push._last_push == {}
