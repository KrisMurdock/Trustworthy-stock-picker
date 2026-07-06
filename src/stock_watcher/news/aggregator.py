"""新闻聚合器 — 并发抓取、跨源去重、按自选股过滤."""

from __future__ import annotations

import asyncio
import json
import logging
import time
from datetime import datetime, timezone, timedelta
from pathlib import Path

from Levenshtein import ratio as levenshtein_ratio

from stock_watcher.news.base import BaseNewsFetcher
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

_SIMILARITY_THRESHOLD = 0.85
_MAX_SEEN_LINES = 5000


class NewsAggregator:
    """编排所有启用的 Fetcher，并发抓取，去重，按自选股过滤."""

    def __init__(
        self,
        fetchers: list[BaseNewsFetcher],
        seen_path: str = "news_seen.jsonl",
        proxy_pool = None,
    ) -> None:
        self._fetchers = fetchers
        self._proxy_pool = proxy_pool
        self._seen_path = Path(seen_path)
        self._seen_ids: set[str] = set()
        self._seen_urls: set[str] = set()
        self._seen_titles: list[str] = []
        self._load_seen()

    # ------------------------------------------------------------------
    # 主入口
    # ------------------------------------------------------------------

    async def aggregate(self, watchlist_codes: list[str]) -> list[NewsItem]:
        """并发抓取所有源，返回去重、过滤后的新闻列表."""
        if not self._fetchers:
            return []

        watchlist_set = set(watchlist_codes)

        # 并发抓取 — 根据熔断状态分支
        tasks = {}
        for f in self._fetchers:
            if not f.enabled:
                continue
            if f.state == "green":
                task = asyncio.create_task(self._fetch_one(f, watchlist_codes))
                tasks[task] = f
            elif f.state == "yellow" and f.should_probe():
                logger.info("%s: probing (half-open)", f.source_name)
                task = asyncio.create_task(self._fetch_one(f, watchlist_codes))
                tasks[task] = f
            else:
                state_label = f.state.upper()
                logger.debug(
                    "%s: skipped (state=%s, cooldown=%.0fs)",
                    f.source_name, state_label, f.cooldown_remaining,
                )

        if not tasks:
            return []

        done, pending = await asyncio.wait(
            tasks.keys(), timeout=60, return_when=asyncio.ALL_COMPLETED,
        )

        # Cancel any still-pending tasks (e.g. akshare hung in thread)
        for task in pending:
            fetcher = tasks[task]
            logger.warning("%s: fetch timed out (60s)", fetcher.source_name)
            task.cancel()

        # 收集
        all_items: list[NewsItem] = []
        for task in done:
            fetcher = tasks[task]
            try:
                result = task.result()
                all_items.extend(result)
            except Exception as exc:
                logger.warning("%s: fetch error: %s", fetcher.source_name, exc)

        # 时间过滤：仅保留今天的新闻
        today_items = self._today_filter(all_items)

        # 去重（排除已见 + URL 精确匹配 + 标题相似度）
        deduped = self._dedup(today_items)

        # 按自选股过滤
        filtered = [item for item in deduped if item.relates_to(watchlist_set)]

        logger.info(
            "aggregated: %d raw → %d today → %d deduped → %d filtered (watchlist: %d codes)",
            len(all_items), len(today_items), len(deduped), len(filtered), len(watchlist_codes),
        )
        return filtered

    async def _fetch_one(
        self, fetcher: BaseNewsFetcher, codes: list[str]
    ) -> list[NewsItem]:
        """包装单个 Fetcher 调用，处理异常。注入代理池引用."""
        # Inject proxy pool reference into fetcher
        if self._proxy_pool is not None:
            fetcher._proxy_pool = self._proxy_pool  # type: ignore

        # For httpx fetchers, pick a proxy if available
        proxy = None
        if self._proxy_pool is not None and self._proxy_pool.available_count > 0:
            proxy = self._proxy_pool.get_proxy()

        try:
            # Pass proxy to httpx fetchers that accept it
            import inspect
            sig = inspect.signature(fetcher.fetch)
            if 'proxy' in sig.parameters:
                result = await fetcher.fetch(codes, proxy=proxy)  # type: ignore
            else:
                result = await fetcher.fetch(codes)
            fetcher._record_success()
            return result
        except Exception as exc:
            logger.error("%s fetch error: %s", fetcher.source_name, exc)
            fetcher._record_failure()
            return []

    # ------------------------------------------------------------------
    # 时间过滤
    # ------------------------------------------------------------------

    @staticmethod
    def _today_filter(items: list[NewsItem]) -> list[NewsItem]:
        """仅保留发布日期为今天的新闻，过时新闻丢弃."""
        today = datetime.now(timezone(timedelta(hours=8))).date()
        kept: list[NewsItem] = []
        dropped = 0
        for item in items:
            if item.published_at:
                try:
                    # ISO 8601 格式
                    ts = item.published_at.replace("Z", "+00:00")
                    dt_obj = datetime.fromisoformat(ts)
                    if dt_obj.date() == today:
                        kept.append(item)
                        continue
                except (ValueError, TypeError):
                    pass
            # 没有 published_at → 保留（部分源不提供时间，不能误杀）
            kept.append(item)
        logger.debug("today filter: %d kept, %d dropped (not today)", len(kept), dropped)
        return kept

    # ------------------------------------------------------------------
    # 去重
    # ------------------------------------------------------------------

    def _dedup(self, items: list[NewsItem]) -> list[NewsItem]:
        """多级去重：已见 ID + URL 精确 + 标题相似度（跨周期持久）."""
        result: list[NewsItem] = []

        for item in items:
            # 层级 1: 已持久化的 seen IDs
            if item.id in self._seen_ids:
                continue

            # 层级 2: URL 精确匹配（跨周期持久）
            if item.url and item.url in self._seen_urls:
                continue

            # 层级 3: 标题相似度 > 85%（跨周期持久）
            if self._title_seen(item.title, self._seen_titles):
                continue

            result.append(item)
            self._seen_ids.add(item.id)
            if item.url:
                self._seen_urls.add(item.url)
            self._seen_titles.append(item.title)

        # 限制标题缓存大小，防止内存膨胀
        if len(self._seen_titles) > 2000:
            self._seen_titles = self._seen_titles[-1000:]

        return result

    def _title_seen(self, title: str, seen: list[str]) -> bool:
        """检查标题是否与已见标题高度相似."""
        if not title:
            return False
        for s in seen:
            try:
                if levenshtein_ratio(title, s) > _SIMILARITY_THRESHOLD:
                    return True
            except Exception:
                pass
        return False

    # ------------------------------------------------------------------
    # JSONL 持久化
    # ------------------------------------------------------------------

    def persist_seen(self) -> None:
        """将 seen_ids 和 seen_urls 写入文件（覆盖写入，去重）. 
        
        每次调用时完整重写文件，确保跨周期无重叠。
        """
        now_ts = int(time.time())
        try:
            lines: list[str] = []
            for nid in self._seen_ids:
                lines.append(json.dumps(
                    {"id": nid, "ts": now_ts}, ensure_ascii=False
                ))
            for url in self._seen_urls:
                # URL 条目以 "url:" 前缀区分
                lines.append(json.dumps(
                    {"id": f"url:{url}", "ts": now_ts}, ensure_ascii=False
                ))
            self._seen_path.write_text(
                "\n".join(lines) + "\n" if lines else "",
                encoding="utf-8",
            )
        except OSError:
            pass

    def _load_seen(self) -> None:
        """从 JSONL 文件加载已见 ID 和 URL，仅保留前日收盘后的记录."""
        if not self._seen_path.exists():
            return

        cutoff = self._expiry_cutoff()
        valid_entries: list[dict] = []
        needs_rewrite = False

        try:
            with open(self._seen_path, "r", encoding="utf-8") as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        entry = json.loads(line)
                        nid = entry.get("id", "")
                        ts = entry.get("ts", 0)
                        if nid and ts > cutoff:
                            valid_entries.append(entry)
                        else:
                            needs_rewrite = True
                    except json.JSONDecodeError:
                        needs_rewrite = True
                        continue
        except OSError:
            pass

        # 恢复到内存
        for entry in valid_entries:
            nid = entry["id"]
            if nid.startswith("url:"):
                self._seen_urls.add(nid[4:])
            else:
                self._seen_ids.add(nid)

        # 有过期条目时，重写文件
        if needs_rewrite:
            try:
                lines = [
                    json.dumps(e, ensure_ascii=False)
                    for e in valid_entries
                ]
                self._seen_path.write_text(
                    "\n".join(lines) + "\n" if lines else "",
                    encoding="utf-8",
                )
            except OSError:
                pass

    @staticmethod
    def _expiry_cutoff() -> float:
        """计算过期截止时间：前一个交易日收盘 16:15 CST 的 Unix 时间戳."""
        from datetime import datetime, timedelta, timezone

        cst = timezone(timedelta(hours=8))
        now = datetime.now(cst)

        # 今天的 16:15
        today_close = now.replace(hour=16, minute=15, second=0, microsecond=0)

        if now > today_close:
            # 已过今天收盘 → 保留今天收盘后的
            return today_close.timestamp()
        else:
            # 未到收盘 → 保留昨天收盘后的
            yesterday_close = today_close - timedelta(days=1)
            # 跳过周末
            while yesterday_close.weekday() >= 5:  # Sat=5, Sun=6
                yesterday_close -= timedelta(days=1)
            return yesterday_close.timestamp()

    def _rotate(self) -> None:
        """保持 JSONL ≤ _MAX_SEEN_LINES 行."""
        if not self._seen_path.exists():
            return
        try:
            lines = self._seen_path.read_text(encoding="utf-8").strip().split("\n")
            lines = [l for l in lines if l.strip()]
            if len(lines) > _MAX_SEEN_LINES:
                kept = lines[-_MAX_SEEN_LINES:]
                self._seen_path.write_text("\n".join(kept) + "\n", encoding="utf-8")
        except OSError:
            pass
