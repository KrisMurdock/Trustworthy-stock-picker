"""巨潮资讯网 (cninfo.com.cn) 公告查询与去重."""

from __future__ import annotations

import json
import logging
import time
from pathlib import Path
from typing import Optional

import httpx

logger = logging.getLogger(__name__)

from stock_watcher.config import CninfoConfig, RequestConfig
from stock_watcher.models import Announcement

CNINFO_TIMEOUT = 15  # seconds
CNINFO_BASE = "http://www.cninfo.com.cn/new"


# ---------------------------------------------------------------------------
# orgId 解析
# ---------------------------------------------------------------------------

_ORG_ID_CACHE: dict[str, str] = {}  # raw_code → orgId


def _market_to_column(prefix: str) -> str:
    """Map stock code prefix to cninfo column parameter."""
    if prefix == "sh":
        return "sse"
    elif prefix == "sz":
        return "szse"
    else:
        raise ValueError(f"cninfo does not support market prefix: {prefix}")


def _strip_prefix(code: str) -> tuple[str, str]:
    """Split 'sh600519' → ('sh', '600519')."""
    if len(code) < 3:
        raise ValueError(f"Invalid stock code: {code!r}")
    return code[:2], code[2:]


async def _resolve_org_id(
    raw_code: str,
    request_cfg: RequestConfig,
    proxy: str = "",
) -> Optional[str]:
    """Resolve a raw stock code to cninfo orgId (cached)."""
    if raw_code in _ORG_ID_CACHE:
        return _ORG_ID_CACHE[raw_code]

    url = f"{CNINFO_BASE}/information/topSearch/detailOfQuery"
    headers = {
        "User-Agent": request_cfg.get_random_ua(),
        "Content-Type": "application/x-www-form-urlencoded; charset=UTF-8",
        "Origin": "http://www.cninfo.com.cn",
        "Referer": "http://www.cninfo.com.cn/new/commonUrl?url=disclosure/list/notice",
    }

    try:
        client_kwargs = {"timeout": CNINFO_TIMEOUT}
        if proxy:
            client_kwargs["proxy"] = proxy
        async with httpx.AsyncClient(**client_kwargs) as client:
            resp = await client.post(
                url,
                content=f"keyWord={raw_code}",
                headers=headers,
            )
        if resp.status_code != 200:
            return None

        data = resp.json()
        # Response format: [{"orgId": "gssc0000001", "code": "000001", ...}, ...]
        if isinstance(data, list) and len(data) > 0:
            org_id = str(data[0].get("orgId", ""))
            if org_id:
                _ORG_ID_CACHE[raw_code] = org_id
                return org_id

        return None
    except Exception:
        logger.warning("cninfo fetch failed for %s", sec_code, exc_info=True)
        return None


# ---------------------------------------------------------------------------
# 公告查询
# ---------------------------------------------------------------------------


async def fetch_announcements(
    code: str,
    cninfo_cfg: CninfoConfig,
    request_cfg: RequestConfig,
    proxy: str = "",
) -> list[Announcement]:
    """Fetch recent announcements for a stock code from cninfo.

    Returns an empty list when the stock is not A-share or the API fails.
    """
    prefix, raw_code = _strip_prefix(code)

    try:
        column = _market_to_column(prefix)
    except ValueError:
        return []  # HK/US not supported by cninfo

    org_id = await _resolve_org_id(raw_code, request_cfg, proxy)
    if not org_id:
        return []

    stock_param = f"{raw_code},{org_id}"

    url = f"{CNINFO_BASE}/hisAnnouncement/query"
    headers = {
        "User-Agent": request_cfg.get_random_ua(),
        "Content-Type": "application/x-www-form-urlencoded; charset=UTF-8",
        "Origin": "http://www.cninfo.com.cn",
        "Referer": "http://www.cninfo.com.cn/new/commonUrl?url=disclosure/list/notice",
    }

    body = (
        f"pageNum=1&pageSize={cninfo_cfg.page_size}"
        f"&column={column}"
        f"&stock={stock_param}"
    )

    try:
        client_kwargs = {"timeout": CNINFO_TIMEOUT}
        if proxy:
            client_kwargs["proxy"] = proxy
        async with httpx.AsyncClient(**client_kwargs) as client:
            resp = await client.post(url, content=body, headers=headers)

        if resp.status_code != 200:
            return []

        data = resp.json()
        items = data.get("announcements") or []
        results: list[Announcement] = []
        for item in items:
            ann = Announcement.from_api_item(item, raw_code, org_id)
            if ann.id:  # skip items without an ID
                results.append(ann)

        return results
    except Exception:
        return []


# ---------------------------------------------------------------------------
# 关键词过滤
# ---------------------------------------------------------------------------


def filter_major(announcements: list[Announcement], keywords: list[str]) -> list[Announcement]:
    """Filter announcements by title keywords (重大事项)."""
    if not keywords:
        return list(announcements)

    result: list[Announcement] = []
    for ann in announcements:
        for kw in keywords:
            if kw in ann.title:
                result.append(ann)
                break
    return result


# ---------------------------------------------------------------------------
# 去重 — JSONL 持久化
# ---------------------------------------------------------------------------


class AnnouncementDedup:
    """Track seen announcement IDs with JSONL persistence."""

    def __init__(self, path: Path, max_lines: int = 5000) -> None:
        self._path = path
        self._max_lines = max_lines
        self._seen: set[str] = set()
        self._load()

    def _load(self) -> None:
        """Load seen IDs from the JSONL file."""
        if not self._path.exists():
            return
        try:
            with open(self._path, "r", encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if line:
                        try:
                            entry = json.loads(line)
                            aid = entry.get("id", "")
                            if aid:
                                self._seen.add(aid)
                        except json.JSONDecodeError:
                            continue
        except Exception:
            pass

    def _rotate(self) -> None:
        """Keep only the most recent max_lines entries."""
        if not self._path.exists():
            return
        try:
            lines = self._path.read_text(encoding="utf-8").strip().split("\n")
            # Keep non-empty lines only
            lines = [l for l in lines if l.strip()]
            if len(lines) > self._max_lines:
                kept = lines[-self._max_lines:]
                self._path.write_text("\n".join(kept) + "\n", encoding="utf-8")
        except Exception:
            pass

    def is_new(self, ann: Announcement) -> bool:
        """Return True if this announcement hasn't been seen."""
        return ann.id not in self._seen

    def mark_seen(self, ann: Announcement) -> None:
        """Record an announcement as seen."""
        if not ann.id:
            return
        if ann.id in self._seen:
            return
        self._seen.add(ann.id)

        entry = json.dumps({
            "id": ann.id,
            "code": ann.sec_code,
            "title": ann.title,
            "time": ann.announcement_time,
            "logged_at": int(time.time()),
        }, ensure_ascii=False)

        try:
            with open(self._path, "a", encoding="utf-8") as f:
                f.write(entry + "\n")
        except Exception:
            pass

        # Rotate if needed (check both size and count)
        if self._path.exists():
            if self._path.stat().st_size > 500_000 or len(self._seen) > self._max_lines * 2:
                self._rotate()

    def filter_new(self, announcements: list[Announcement]) -> list[Announcement]:
        """Return only announcements that haven't been seen yet."""
        return [a for a in announcements if self.is_new(a)]
