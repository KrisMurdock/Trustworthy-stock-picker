"""Unit tests for cninfo_fetcher — 巨潮资讯网公告查询与去重."""

import json
import tempfile
from pathlib import Path

import pytest
import respx
import httpx

from stock_watcher.cninfo_fetcher import (
    fetch_announcements,
    filter_major,
    AnnouncementDedup,
    _resolve_org_id,
    _strip_prefix,
    _market_to_column,
    _ORG_ID_CACHE,
)
from stock_watcher.models import Announcement
from stock_watcher.config import CninfoConfig, RequestConfig


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def req_cfg() -> RequestConfig:
    return RequestConfig(timeout=10)


@pytest.fixture
def cninfo_cfg() -> CninfoConfig:
    return CninfoConfig(enabled=True, page_size=5)


@pytest.fixture
def sample_items() -> list[dict]:
    return [
        {
            "id": "ann001",
            "announcementId": "ann001",
            "secCode": "600519",
            "secName": "贵州茅台",
            "announcementTitle": "2024年年度报告",
            "announcementTime": "2025-03-31 16:30",
            "adjunctUrl": "final/2025/03/31/ann001.PDF",
        },
        {
            "id": "ann002",
            "announcementId": "ann002",
            "secCode": "600519",
            "secName": "贵州茅台",
            "announcementTitle": "关于控股股东减持股份的公告",
            "announcementTime": "2025-04-01 08:30",
            "adjunctUrl": "final/2025/04/01/ann002.PDF",
        },
        {
            "id": "ann003",
            "announcementId": "ann003",
            "secCode": "600519",
            "secName": "贵州茅台",
            "announcementTitle": "日常关联交易公告",
            "announcementTime": "2025-04-01 10:00",
            "adjunctUrl": "",
        },
    ]


@pytest.fixture(autouse=True)
def _clear_org_cache():
    """Clear orgId cache between tests."""
    _ORG_ID_CACHE.clear()
    yield
    _ORG_ID_CACHE.clear()


# ---------------------------------------------------------------------------
# _strip_prefix / _market_to_column
# ---------------------------------------------------------------------------


class TestHelpers:
    def test_strip_prefix_sh(self):
        prefix, raw = _strip_prefix("sh600519")
        assert prefix == "sh"
        assert raw == "600519"

    def test_strip_prefix_sz(self):
        prefix, raw = _strip_prefix("sz000001")
        assert prefix == "sz"
        assert raw == "000001"

    def test_strip_prefix_invalid(self):
        with pytest.raises(ValueError):
            _strip_prefix("hk")

    def test_market_to_column_sh(self):
        assert _market_to_column("sh") == "sse"

    def test_market_to_column_sz(self):
        assert _market_to_column("sz") == "szse"

    def test_market_to_column_invalid(self):
        with pytest.raises(ValueError):
            _market_to_column("hk")


# ---------------------------------------------------------------------------
# _resolve_org_id
# ---------------------------------------------------------------------------


class TestResolveOrgId:
    def test_resolve_success(self, req_cfg):
        with respx.mock() as mock:
            mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(
                json=[{"orgId": "gssc0000001", "code": "600519", "name": "贵州茅台"}]
            )

            import asyncio
            org_id = asyncio.run(_resolve_org_id("600519", req_cfg))

        assert org_id == "gssc0000001"

    def test_resolve_empty_list(self, req_cfg):
        with respx.mock() as mock:
            mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(json=[])

            import asyncio
            org_id = asyncio.run(_resolve_org_id("000001", req_cfg))

        assert org_id is None

    def test_resolve_cached(self, req_cfg):
        """Second call should use cache, not hit network."""
        with respx.mock() as mock:
            route = mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(
                json=[{"orgId": "gssc0000001", "code": "600519"}]
            )

            import asyncio

            # First call
            org_id1 = asyncio.run(_resolve_org_id("600519", req_cfg))
            assert org_id1 == "gssc0000001"

            # Second call — should use cache
            org_id2 = asyncio.run(_resolve_org_id("600519", req_cfg))
            assert org_id2 == "gssc0000001"

            # Route should have been called only once
            assert route.call_count == 1

    def test_resolve_http_error(self, req_cfg):
        with respx.mock() as mock:
            mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(status_code=500)

            import asyncio
            org_id = asyncio.run(_resolve_org_id("600519", req_cfg))

        assert org_id is None


# ---------------------------------------------------------------------------
# fetch_announcements
# ---------------------------------------------------------------------------


class TestFetchAnnouncements:
    def test_fetch_hk_returns_empty(self, cninfo_cfg, req_cfg):
        """HK stocks are not supported by cninfo."""
        import asyncio
        anns = asyncio.run(fetch_announcements("hk00700", cninfo_cfg, req_cfg))
        assert anns == []

    def test_fetch_us_returns_empty(self, cninfo_cfg, req_cfg):
        import asyncio
        anns = asyncio.run(fetch_announcements("ustsla", cninfo_cfg, req_cfg))
        assert anns == []

    def test_fetch_no_org_id(self, cninfo_cfg, req_cfg):
        """When orgId resolution fails, return empty."""
        with respx.mock() as mock:
            mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(json=[])

            import asyncio
            anns = asyncio.run(fetch_announcements("sh600519", cninfo_cfg, req_cfg))

        assert anns == []

    def test_fetch_success(self, cninfo_cfg, req_cfg, sample_items):
        with respx.mock() as mock:
            # orgId resolution
            mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(
                json=[{"orgId": "gssc0000001", "code": "600519"}]
            )
            # Announcement query
            mock.post(
                "http://www.cninfo.com.cn/new/hisAnnouncement/query"
            ).respond(
                json={
                    "announcements": sample_items,
                    "totalAnnouncement": 3,
                }
            )

            import asyncio
            anns = asyncio.run(fetch_announcements("sh600519", cninfo_cfg, req_cfg))

        assert len(anns) == 3
        assert anns[0].id == "ann001"
        assert anns[0].title == "2024年年度报告"
        assert anns[0].sec_name == "贵州茅台"
        assert anns[0].pdf_url == "http://static.cninfo.com.cn/final/2025/03/31/ann001.PDF"
        # ann003 has no adjunctUrl → pdf_url should be empty
        assert anns[2].pdf_url == ""

    def test_fetch_http_error(self, cninfo_cfg, req_cfg):
        with respx.mock() as mock:
            mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(
                json=[{"orgId": "gssc0000001", "code": "600519"}]
            )
            mock.post(
                "http://www.cninfo.com.cn/new/hisAnnouncement/query"
            ).respond(status_code=500)

            import asyncio
            anns = asyncio.run(fetch_announcements("sh600519", cninfo_cfg, req_cfg))

        assert anns == []

    def test_fetch_empty_result(self, cninfo_cfg, req_cfg):
        with respx.mock() as mock:
            mock.post(
                "http://www.cninfo.com.cn/new/information/topSearch/detailOfQuery"
            ).respond(
                json=[{"orgId": "gssc0000001"}]
            )
            mock.post(
                "http://www.cninfo.com.cn/new/hisAnnouncement/query"
            ).respond(
                json={"announcements": [], "totalAnnouncement": 0}
            )

            import asyncio
            anns = asyncio.run(fetch_announcements("sh600519", cninfo_cfg, req_cfg))

        assert anns == []


# ---------------------------------------------------------------------------
# filter_major
# ---------------------------------------------------------------------------


class TestFilterMajor:
    def test_match_keywords(self, sample_items):
        anns = [
            Announcement.from_api_item(item, "600519", "gssc0000001")
            for item in sample_items
        ]
        keywords = ["年度报告", "减持"]

        major = filter_major(anns, keywords)
        assert len(major) == 2
        assert major[0].id == "ann001"  # 年度报告
        assert major[1].id == "ann002"  # 减持
        # ann003 "日常关联交易" should not match

    def test_no_keywords_returns_all(self, sample_items):
        anns = [
            Announcement.from_api_item(item, "600519", "gssc0000001")
            for item in sample_items
        ]
        major = filter_major(anns, [])
        assert len(major) == 3

    def test_no_match_returns_empty(self, sample_items):
        anns = [
            Announcement.from_api_item(item, "600519", "gssc0000001")
            for item in sample_items
        ]
        major = filter_major(anns, ["不存在的关键词"])
        assert major == []

    def test_empty_input(self):
        assert filter_major([], ["年报"]) == []


# ---------------------------------------------------------------------------
# AnnouncementDedup
# ---------------------------------------------------------------------------


class TestAnnouncementDedup:
    def test_is_new_and_mark_seen(self):
        with tempfile.NamedTemporaryFile(suffix=".jsonl", delete=False) as f:
            path = Path(f.name)

        try:
            dedup = AnnouncementDedup(path)
            ann = Announcement(
                id="abc123", sec_code="600519", sec_name="茅台",
                title="年报", announcement_time="2025-01-01", adjunct_url="",
            )

            assert dedup.is_new(ann) is True
            dedup.mark_seen(ann)
            assert dedup.is_new(ann) is False
        finally:
            path.unlink(missing_ok=True)

    def test_persistence(self):
        with tempfile.NamedTemporaryFile(suffix=".jsonl", delete=False) as f:
            path = Path(f.name)

        try:
            ann = Announcement(
                id="xyz789", sec_code="000001", sec_name="平安银行",
                title="季报", announcement_time="2025-01-01", adjunct_url="",
            )

            dedup1 = AnnouncementDedup(path)
            dedup1.mark_seen(ann)

            dedup2 = AnnouncementDedup(path)
            assert dedup2.is_new(ann) is False
        finally:
            path.unlink(missing_ok=True)

    def test_filter_new(self):
        with tempfile.NamedTemporaryFile(suffix=".jsonl", delete=False) as f:
            path = Path(f.name)

        try:
            dedup = AnnouncementDedup(path)
            ann1 = Announcement(id="a", sec_code="0", sec_name="T", title="T1", announcement_time="", adjunct_url="")
            ann2 = Announcement(id="b", sec_code="0", sec_name="T", title="T2", announcement_time="", adjunct_url="")
            ann3 = Announcement(id="c", sec_code="0", sec_name="T", title="T3", announcement_time="", adjunct_url="")

            dedup.mark_seen(ann2)

            new = dedup.filter_new([ann1, ann2, ann3])
            assert len(new) == 2
            assert ann1 in new
            assert ann2 not in new
            assert ann3 in new
        finally:
            path.unlink(missing_ok=True)

    def test_empty_id_skipped(self):
        """Announcements with empty ID should not be tracked."""
        with tempfile.NamedTemporaryFile(suffix=".jsonl", delete=False) as f:
            path = Path(f.name)

        try:
            dedup = AnnouncementDedup(path)
            ann = Announcement(id="", sec_code="0", sec_name="T", title="T", announcement_time="", adjunct_url="")

            # Empty ID is considered new (not cached)
            assert dedup.is_new(ann) is True
            # marking an empty ID should be a no-op
            dedup.mark_seen(ann)
            assert dedup.is_new(ann) is True
        finally:
            path.unlink(missing_ok=True)

    def test_rotation(self):
        """Rotation triggers via mark_seen when file exceeds size limit."""
        with tempfile.NamedTemporaryFile(suffix=".jsonl", delete=False) as f:
            path = Path(f.name)

        try:
            dedup = AnnouncementDedup(path, max_lines=10)

            # Write 100 entries to trigger rotation (500KB threshold)
            big_entry = json.dumps({"id": "x" * 500, "code": "0", "title": "T" * 200, "time": "", "logged_at": 0}, ensure_ascii=False)
            for i in range(200):
                entry = json.dumps({"id": f"big_{i}", "code": "0", "title": "T", "time": "", "logged_at": i}, ensure_ascii=False)
                ann = Announcement(id=f"big_{i}", sec_code="0", sec_name="T", title="T", announcement_time="", adjunct_url="")
                dedup.mark_seen(ann)

            # After rotation, the file should have at most max_lines entries
            with open(path, "r", encoding="utf-8") as f:
                lines = [l for l in f.read().strip().split("\n") if l.strip()]
            # Should be 10 or fewer (rotated)
            assert len(lines) <= 10, f"Expected ≤10 lines after rotation, got {len(lines)}"
        finally:
            path.unlink(missing_ok=True)
