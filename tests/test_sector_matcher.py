"""Unit tests for SectorMatcher."""

import pytest
from unittest.mock import patch, MagicMock

from stock_watcher.news.sector_matcher import SectorMatcher
from stock_watcher.models import NewsItem


class TestSectorMatcher:
    """T14: SectorMatcher — keyword→sector fuzzy matching."""

    def _make_news(self, title="", summary=""):
        return NewsItem(
            id="test", source="test", title=title, summary=summary,
            url="", published_at="", related_codes=[], source_priority=2,
        )

    def _matcher(self, keywords=None, sector_list=None):
        return SectorMatcher(
            sector_keywords=keywords or {},
            sector_list=sector_list or ["机器人执行器", "人形机器人", "减速器", "新能源车", "银行"],
        )

    def test_direct_sector_name_match(self):
        """News title contains exact sector name → match."""
        matcher = self._matcher(
            keywords={"机器人": ["机器人执行器"]},
        )
        result = matcher.match(self._make_news(title="机器人执行器概念股午后异动拉升"))
        assert "机器人执行器" in result

    def test_partial_keyword_match(self):
        """Keyword "机器人" matches sector even when sector name isn't exact."""
        matcher = self._matcher(
            keywords={"机器人": ["机器人执行器", "减速器"]},
        )
        result = matcher.match(self._make_news(title="机器人产业迎来爆发期"))
        assert len(result) >= 1
        assert all(s in ["机器人执行器", "减速器"] for s in result)

    def test_case_insensitive(self):
        matcher = self._matcher(
            keywords={"ai": ["人工智能"]},
            sector_list=["机器人执行器", "人工智能"],
        )
        result = matcher.match(self._make_news(title="AI概念持续走强"))
        assert "人工智能" in result

    def test_no_match_returns_empty(self):
        matcher = self._matcher(
            keywords={"机器人": ["机器人执行器"]},
        )
        result = matcher.match(self._make_news(title="央行发布货币政策报告"))
        assert result == []

    def test_empty_title_returns_empty(self):
        matcher = self._matcher(
            keywords={"机器人": ["机器人执行器"]},
        )
        result = matcher.match(self._make_news(title=""))
        assert result == []

    def test_multi_keyword_to_same_sector_dedup(self):
        matcher = self._matcher(
            keywords={"机器人": ["机器人执行器"], "自动化": ["机器人执行器"]},
        )
        result = matcher.match(self._make_news(title="机器人和自动化产业"))
        assert result.count("机器人执行器") == 1

    def test_validates_against_live_sector_list(self):
        """Only return sectors that exist in the live list."""
        matcher = self._matcher(
            keywords={"机器人": ["机器人执行器", "不存在的板块"]},
            sector_list=["机器人执行器"],  # "不存在的板块" not in list
        )
        result = matcher.match(self._make_news(title="机器人"))
        assert "机器人执行器" in result
        assert "不存在的板块" not in result

    def test_summary_matching(self):
        """Keywords matched in summary field too."""
        matcher = self._matcher(
            keywords={"新能源": ["新能源车"]},
        )
        result = matcher.match(self._make_news(
            title="市场异动",
            summary="新能源车板块资金大幅流入",
        ))
        assert "新能源车" in result
