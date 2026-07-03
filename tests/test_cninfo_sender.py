"""Unit tests for cninfo_sender — 飞书公告卡片 + DeepSeek 摘要."""

import pytest
import respx
import httpx

from stock_watcher.models import Announcement
from stock_watcher.cninfo_sender import (
    build_announcement_card,
    summarize_announcement,
    summarize_batch,
)
from stock_watcher.config import DeepSeekConfig


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def sample_ann() -> Announcement:
    return Announcement(
        id="ann001",
        sec_code="600519",
        sec_name="贵州茅台",
        title="2024年年度报告",
        announcement_time="2025-03-31 16:30",
        adjunct_url="final/2025/03/31/ann001.PDF",
        org_id="gssc0000001",
    )


@pytest.fixture
def sample_anns() -> list[Announcement]:
    return [
        Announcement(
            id="ann001", sec_code="600519", sec_name="贵州茅台",
            title="2024年半年度报告",
            announcement_time="2025-08-31 16:30",
            adjunct_url="final/2025/08/31/ann001.PDF",
        ),
        Announcement(
            id="ann002", sec_code="600519", sec_name="贵州茅台",
            title="关于控股股东减持股份的公告",
            announcement_time="2025-09-01 08:30",
            adjunct_url="final/2025/09/01/ann002.PDF",
        ),
    ]


# ---------------------------------------------------------------------------
# build_announcement_card
# ---------------------------------------------------------------------------


class TestBuildAnnouncementCard:
    def test_basic_card(self, sample_ann):
        card = build_announcement_card("sh600519", "贵州茅台", [sample_ann])

        assert card["msg_type"] == "interactive"
        assert "card" in card
        assert card["card"]["header"]["template"] == "blue"
        assert "📰 贵州茅台 公告" in card["card"]["header"]["title"]["content"]

        elements = card["card"]["elements"]
        # Should contain: header row, hr, title, hr, total 4+ elements
        assert len(elements) >= 4

    def test_card_with_summary(self, sample_ann):
        summaries = {"ann001": "茅台半年营收增长18%，净利润增长15%"}
        card = build_announcement_card("sh600519", "茅台", [sample_ann], summaries)

        elements_text = str(card["card"]["elements"])
        assert "茅台半年营收增长18%" in elements_text

    def test_card_without_summary(self, sample_ann):
        card = build_announcement_card("sh600519", "茅台", [sample_ann])

        elements_text = str(card["card"]["elements"])
        assert "📝" not in elements_text  # no summary mark

    def test_card_with_pdf_link(self, sample_ann):
        card = build_announcement_card("sh600519", "茅台", [sample_ann])

        elements_text = str(card["card"]["elements"])
        assert "static.cninfo.com.cn/final/2025/03/31/ann001.PDF" in elements_text
        assert "查看PDF" in elements_text

    def test_card_without_pdf_link(self):
        ann = Announcement(
            id="no_pdf", sec_code="600519", sec_name="茅台",
            title="公告无附件", announcement_time="2025-01-01",
            adjunct_url="",  # no PDF
        )
        card = build_announcement_card("sh600519", "茅台", [ann])

        elements_text = str(card["card"]["elements"])
        assert "查看PDF" not in elements_text

    def test_multiple_announcements(self, sample_anns):
        card = build_announcement_card("sh600519", "茅台", sample_anns)
        elements = card["card"]["elements"]

        # Should contain headers for both announcements
        text = str(elements)
        assert "半年度报告" in text
        assert "减持股份" in text


# ---------------------------------------------------------------------------
# summarize_announcement (DeepSeek)
# ---------------------------------------------------------------------------


class TestSummarizeAnnouncement:
    def test_not_configured(self):
        cfg = DeepSeekConfig(api_key="")  # not configured
        import asyncio
        result = asyncio.run(summarize_announcement(cfg, "年报"))
        assert result == ""

    def test_summarize_success(self):
        cfg = DeepSeekConfig(api_key="sk-test", api_base="https://api.deepseek.com", model="deepseek-chat")

        with respx.mock() as mock:
            mock.post("https://api.deepseek.com/chat/completions").respond(
                json={
                    "choices": [
                        {"message": {"content": "公司全年营收增长良好，建议关注利润提升空间。"}}
                    ]
                }
            )

            import asyncio
            result = asyncio.run(
                summarize_announcement(cfg, "2024年年度报告")
            )

        assert "全年营收增长良好" in result

    def test_summarize_http_error(self):
        cfg = DeepSeekConfig(api_key="sk-test", api_base="https://api.deepseek.com", model="deepseek-chat")

        with respx.mock() as mock:
            mock.post("https://api.deepseek.com/chat/completions").respond(status_code=500)

            import asyncio
            result = asyncio.run(summarize_announcement(cfg, "年报"))

        assert result == ""

    def test_summarize_batch(self, sample_anns):
        cfg = DeepSeekConfig(api_key="sk-test", api_base="https://api.deepseek.com", model="deepseek-chat")

        with respx.mock() as mock:
            mock.post("https://api.deepseek.com/chat/completions").respond(
                json={
                    "choices": [
                        {"message": {"content": "核心摘要内容"}}
                    ]
                }
            )

            import asyncio
            results = asyncio.run(summarize_batch(cfg, sample_anns))

        assert len(results) == 2
        assert "ann001" in results
        assert "ann002" in results
        assert results["ann001"] == "核心摘要内容"
