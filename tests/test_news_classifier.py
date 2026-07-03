"""Unit tests for NewsClassifier."""

import pytest
from unittest.mock import AsyncMock, patch, MagicMock

from stock_watcher.news.classifier import NewsClassifier, NewsLevel, ClassifyResult
from stock_watcher.config import DeepSeekConfig
from stock_watcher.models import NewsItem


class TestNewsClassifier:
    """T09: NewsClassifier — keyword pre-filter + DeepSeek batch classify."""

    def _make_item(self, title="Test news", summary="Summary", id="test1"):
        return NewsItem(
            id=id, source="test", title=title, summary=summary,
            url="http://x.com", published_at="2025-07-01T10:00:00Z",
            related_codes=[], source_priority=2,
        )

    def test_keyword_prefilter_critical(self):
        """critical_keywords should short-circuit to P0."""
        classifier = NewsClassifier(
            deepseek_cfg=None,
            critical_keywords=["停牌", "退市"],
        )
        import asyncio
        item = self._make_item(title="公司公告：股票停牌")
        result = asyncio.run(classifier.classify(item))
        assert result.level == NewsLevel.CRITICAL
        assert "停牌" in result.reason

    def test_keyword_prefilter_ignore(self):
        """ignore_keywords should short-circuit to P2."""
        classifier = NewsClassifier(
            deepseek_cfg=None,
            ignore_keywords=["龙虎榜"],
        )
        import asyncio
        item = self._make_item(title="龙虎榜数据公布")
        result = asyncio.run(classifier.classify(item))
        assert result.level == NewsLevel.IGNORE
        assert "龙虎榜" in result.reason

    def test_no_keywords_defaults_to_notable(self):
        """Without AI, default level is NOTABLE."""
        classifier = NewsClassifier(deepseek_cfg=None)
        import asyncio
        item = self._make_item(title="普通新闻")
        result = asyncio.run(classifier.classify(item))
        assert result.level == NewsLevel.NOTABLE

    def test_cache_returns_same_result(self):
        """Second classify call should return cached result."""
        classifier = NewsClassifier(deepseek_cfg=None)
        import asyncio
        item = self._make_item(id="cache_test")
        result1 = asyncio.run(classifier.classify(item))
        result2 = asyncio.run(classifier.classify(item))
        assert result1 is result2  # same object from cache
        assert result1.level == result2.level

    def test_case_insensitive_keywords(self):
        """Keywords should match case-insensitively."""
        classifier = NewsClassifier(
            deepseek_cfg=None,
            critical_keywords=["停牌"],
        )
        import asyncio
        item = self._make_item(title="公司 停牌 公告")  # mixed
        result = asyncio.run(classifier.classify(item))
        assert result.level == NewsLevel.CRITICAL

    def test_ai_classify_called_when_configured(self):
        """With DeepSeek config, AI should be called."""
        import respx
        cfg = DeepSeekConfig(api_key="sk-test", model="deepseek-chat", api_base="https://api.test.com")
        classifier = NewsClassifier(deepseek_cfg=cfg)
        item = self._make_item(title="央行降准")

        mock_response = {
            "choices": [{
                "message": {
                    "content": '{"level":"critical","summary":"降准利好","stocks":[],"sectors":["银行"],"sentiment":"利好","reason":"重大政策"}'
                }
            }]
        }
        with respx.mock as mock:
            mock.post("https://api.test.com/chat/completions").respond(json=mock_response)
            import asyncio
            result = asyncio.run(classifier.classify(item))

        assert result.level == NewsLevel.CRITICAL
        assert "降准" in result.summary

    def test_ai_failure_returns_notable(self):
        """On DeepSeek failure, safe default is NOTABLE."""
        import respx
        cfg = DeepSeekConfig(api_key="sk-test", api_base="https://api.test.com")
        classifier = NewsClassifier(deepseek_cfg=cfg)
        item = self._make_item()

        with respx.mock as router:
            router.post("https://api.test.com/chat/completions").mock(
                side_effect=Exception("timeout")
            )
            import asyncio
            result = asyncio.run(classifier.classify(item))

        assert result.level == NewsLevel.NOTABLE
        assert "API 调用失败" in result.reason
