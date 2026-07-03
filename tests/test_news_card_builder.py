"""Unit tests for NewsCardBuilder (extended with sector + tiered quotes)."""

import pytest

from stock_watcher.news.card_builder import build_news_card, build_enriched_card
from stock_watcher.models import NewsItem, SectorStock
from stock_watcher.news.trend_ranker import RankedResult
from stock_watcher.news.classifier import ClassifyResult, NewsLevel


def _make_news(title="测试新闻", id="test1"):
    return NewsItem(
        id=id, source="eastmoney_flash", title=title, summary="摘要",
        url="http://x.com", published_at="2025-07-01T10:00:00Z",
        related_codes=["sh600519"], source_priority=2,
    )


def _make_classify():
    return ClassifyResult(
        level=NewsLevel.CRITICAL, summary="AI摘要", reason="重要",
        sentiment="利好", stocks=["贵州茅台"], sectors=["白酒"],
    )


def _make_stock(code="000001", name="测试股", sector="银行", price=10.0, change_pct=5.0):
    return SectorStock(
        code=code, name=name, sector=sector,
        price=price, change_pct=change_pct,
        high=10.5, low=9.5, open=9.8, prev_close=9.7,
        volume=100000, turnover=3.0, pe=15.0, pb=1.5,
    )


class TestEnrichedCard:
    """T10: Enriched card with sector overview + tiered stock quotes."""

    def test_enriched_card_structure(self):
        """Card has header, sector row, tiered stock rows."""
        news = _make_news(title="机器人概念异动拉升")
        classify = _make_classify()
        tiered = RankedResult(
            tier1=[_make_stock("000001", "持仓A", change_pct=10.0)],
            tier2=[_make_stock("000002", "自选B", change_pct=7.0)],
            tier3=[_make_stock("000003", "趋势C", change_pct=5.0)],
        )

        card = build_enriched_card(
            news=news, classify=classify, sector="机器人执行器", tiered=tiered,
        )

        assert card["msg_type"] == "interactive"
        header_title = card["card"]["header"]["title"]["content"]
        assert "机器人执行器" in header_title
        assert "📰" in header_title

        # Card elements should contain tier labels
        elements_text = str(card["card"]["elements"])
        assert "📌" in elements_text
        assert "持仓A" in elements_text
        assert "000001" in elements_text
        assert "10.00" in elements_text  # price

    def test_stock_row_format(self):
        """Each stock row shows: name code change% price H/L/O."""
        news = _make_news()
        classify = _make_classify()
        tiered = RankedResult(
            tier1=[_make_stock("000001", "测试股", price=50.0, change_pct=12.34)],
        )

        card = build_enriched_card(news, classify, "银行", tiered)
        elements_text = str(card["card"]["elements"])
        assert "测试股" in elements_text
        assert "000001" in elements_text
        assert "50.00" in elements_text  # price
        assert "10.50" in elements_text  # high
        assert "9.50" in elements_text   # low
        assert "9.80" in elements_text   # open

    def test_basic_card_fallback(self):
        """When no sector matched, use basic news card."""
        news = _make_news()
        classify = _make_classify()
        card = build_enriched_card(news=news, classify=classify, sector=None, tiered=None)
        assert "重大新闻" in card["card"]["header"]["title"]["content"]
        assert "📰" in card["card"]["header"]["title"]["content"]

    def test_stock_cap_at_8(self):
        """Max 8 stocks across all tiers."""
        stocks = [_make_stock(f"{i:06d}", f"Stock{i}") for i in range(1, 15)]
        tiered = RankedResult(
            tier1=stocks[:3],
            tier2=stocks[3:7],
            tier3=stocks[7:],
        )
        news = _make_news()
        classify = _make_classify()
        card = build_enriched_card(news, classify, "银行", tiered)

        # Count stock mentions in elements
        elements_text = str(card["card"]["elements"])
        # After cap, should have ≤ 8 unique stocks
        # Tier1(3) + Tier2(4) = 7, then Tier3 gets 1 slot → total 8
        stock_mentions = sum(1 for s in stocks if s.name in elements_text)
        assert stock_mentions <= 8

    def test_empty_tier_not_shown(self):
        """Don't show tier header when tier is empty."""
        news = _make_news()
        classify = _make_classify()
        tiered = RankedResult(
            tier1=[_make_stock("000001", "Pos")],
            tier2=[],
            tier3=[],
        )
        card = build_enriched_card(news, classify, "银行", tiered)
        elements_text = str(card["card"]["elements"])
        assert "👁" not in elements_text  # Tier2 empty → no label
        assert "🔥" not in elements_text  # Tier3 empty → no label
        assert "📌" in elements_text      # Tier1 has items


class TestBasicCard:
    """Original basic card still works."""

    def test_basic_card_builds(self):
        news = _make_news()
        classify = _make_classify()
        card = build_news_card(items=[(news, classify)])
        assert card["msg_type"] == "interactive"
        assert "新闻速报" in card["card"]["header"]["title"]["content"]
