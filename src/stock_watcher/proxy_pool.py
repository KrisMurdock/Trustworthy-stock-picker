"""自动代理池 — 从免费源抓取代理，验证可用性，健康评分排序."""
from __future__ import annotations

import asyncio
import json
import logging
import time
from dataclasses import dataclass, field
from pathlib import Path

import httpx

logger = logging.getLogger(__name__)

# 免费代理源
_PROXY_SOURCES = [
    # JSON API — returns list of proxy objects
    {
        "url": "https://proxylist.geonode.com/api/proxy-list?limit=50&page=1"
               "&sort_by=lastChecked&sort_type=desc&protocols=http,https"
               "&anonymityLevel=elite&anonymityLevel=anonymous",
        "parser": "geonode_json",
    },
    # Fallback: text-based proxy list
    {
        "url": "https://api.proxyscrape.com/v2/?request=displayproxies"
               "&protocol=http&timeout=10000&country=all&ssl=all&anonymity=all",
        "parser": "text_lines",
    },
]

_VALIDATE_URL = "https://www.baidu.com"
_VALIDATE_TIMEOUT = 5.0
_CACHE_EXPIRY = 7200  # 2 hours


@dataclass
class ProxyEntry:
    """代理条目."""
    url: str
    score: float = 1.0       # 0.0 ~ 1.0，健康评分
    latency: float = 0.0     # 最近验证延迟(秒)
    last_check: float = field(default_factory=time.monotonic)
    fail_count: int = 0


class ProxyPool:
    """单例。自动从免费源抓取代理，验证可用性，健康评分排序。"""

    def __init__(self, cache_path: str = "proxy_cache.json") -> None:
        self._proxies: list[ProxyEntry] = []
        self._cache_path = Path(cache_path)
        self._last_refresh: float = 0.0
        self._refresh_interval: float = 1800.0  # 30 min
        self._min_proxies: int = 3
        self._max_proxies: int = 20
        self._load_cache()

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def refresh(self) -> int:
        """抓取 + 验证，返回新增可用代理数。"""
        raw_proxies = await self._fetch_from_sources()
        if not raw_proxies:
            logger.warning("proxy_pool: no proxies fetched from sources")
            return 0

        # 去重
        existing_urls = {p.url for p in self._proxies}
        new_urls = [u for u in raw_proxies if u not in existing_urls]

        # 并发验证
        validated: list[ProxyEntry] = []
        sem = asyncio.Semaphore(10)  # limit concurrent validation

        async def validate_one(url: str) -> ProxyEntry | None:
            async with sem:
                latency = await self._validate_one(url)
                if latency is not None:
                    return ProxyEntry(
                        url=url, score=self._score_from_latency(latency),
                        latency=latency, last_check=time.monotonic(),
                    )
                return None

        tasks = [asyncio.create_task(validate_one(u)) for u in new_urls]
        results = await asyncio.gather(*tasks)
        for entry in results:
            if entry is not None:
                validated.append(entry)

        # Merge: keep existing + new, sort by score desc, cap
        self._proxies.extend(validated)
        self._proxies.sort(key=lambda p: p.score, reverse=True)
        if len(self._proxies) > self._max_proxies:
            self._proxies = self._proxies[:self._max_proxies]

        self._last_refresh = time.monotonic()
        self._save_cache()

        logger.info(
            "proxy_pool: refreshed — %d fetched, %d valid, %d total in pool",
            len(new_urls), len(validated), len(self._proxies),
        )
        return len(validated)

    def get_proxy(self) -> str | None:
        """轮询返回一个代理地址（http://ip:port）。"""
        if not self._proxies:
            return None
        # Round-robin via moving first to last
        entry = self._proxies.pop(0)
        self._proxies.append(entry)
        return entry.url

    def get_proxies(self, n: int) -> list[str]:
        """返回最多 n 个代理，按评分降序。"""
        sorted_proxies = sorted(self._proxies, key=lambda p: p.score, reverse=True)
        return [p.url for p in sorted_proxies[:n]]

    def mark_dead(self, proxy: str) -> None:
        """标记失效，fail_count += 1，≥3 次移除。"""
        for p in self._proxies:
            if p.url == proxy:
                p.fail_count += 1
                p.score *= 0.5
                if p.fail_count >= 3:
                    self._proxies.remove(p)
                    logger.debug("proxy_pool: removed dead proxy %s", proxy)
                return

    def mark_slow(self, proxy: str, latency: float) -> None:
        """更新延迟和评分。"""
        for p in self._proxies:
            if p.url == proxy:
                p.latency = latency
                p.score = self._score_from_latency(latency)
                p.last_check = time.monotonic()
                return

    @property
    def available_count(self) -> int:
        return len(self._proxies)

    @property
    def stats(self) -> dict:
        if not self._proxies:
            return {"total": 0, "available": 0, "avg_latency": 0.0}
        latencies = [p.latency for p in self._proxies if p.latency > 0]
        return {
            "total": len(self._proxies),
            "available": len(self._proxies),
            "avg_latency": sum(latencies) / len(latencies) if latencies else 0.0,
        }

    # ------------------------------------------------------------------
    # Internal
    # ------------------------------------------------------------------

    async def _fetch_from_sources(self) -> list[str]:
        """从所有代理源抓取原始代理列表。"""
        all_proxies: list[str] = []
        async with httpx.AsyncClient(timeout=15.0) as client:
            for src in _PROXY_SOURCES:
                try:
                    resp = await client.get(src["url"])
                    resp.raise_for_status()
                    parsed = self._parse_proxy_response(resp.text, src["parser"])
                    all_proxies.extend(parsed)
                    logger.debug(
                        "proxy_pool: fetched %d from %s", len(parsed), src["url"][:60]
                    )
                except Exception as exc:
                    logger.warning(
                        "proxy_pool: source %s failed: %s", src["url"][:60], exc
                    )
        return list(set(all_proxies))  # dedup

    @staticmethod
    def _parse_proxy_response(text: str, parser: str) -> list[str]:
        """解析代理源响应。"""
        if parser == "geonode_json":
            try:
                data = json.loads(text)
                proxies = []
                for item in data.get("data", []):
                    ip = item.get("ip", "")
                    port = item.get("port", "")
                    proto = item.get("protocols", ["http"])[0]
                    if ip and port:
                        proxies.append(f"{proto}://{ip}:{port}")
                return proxies
            except (json.JSONDecodeError, KeyError):
                return []
        elif parser == "text_lines":
            proxies = []
            for line in text.strip().split("\n"):
                line = line.strip()
                if line and ":" in line and not line.startswith("#"):
                    # Ensure it has a protocol prefix
                    if not line.startswith("http"):
                        line = f"http://{line}"
                    proxies.append(line)
            return proxies
        return []

    async def _validate_one(self, proxy: str) -> float | None:
        """验证单个代理是否可用。返回延迟(秒)或 None。"""
        try:
            t0 = time.monotonic()
            async with httpx.AsyncClient(
                proxy=proxy, timeout=_VALIDATE_TIMEOUT, follow_redirects=False,
            ) as client:
                resp = await client.get(_VALIDATE_URL)
                if resp.status_code in (200, 301, 302):
                    return time.monotonic() - t0
            return None
        except Exception:
            return None

    @staticmethod
    def _score_from_latency(latency: float) -> float:
        """将延迟映射为 0.0~1.0 的分数。"""
        if latency <= 1.0:
            return 1.0
        elif latency <= 3.0:
            return 0.7
        elif latency <= 5.0:
            return 0.4
        else:
            return 0.1

    # ------------------------------------------------------------------
    # Cache persistence
    # ------------------------------------------------------------------

    def _load_cache(self) -> None:
        """从缓存文件加载代理列表。"""
        if not self._cache_path.exists():
            return
        try:
            data = json.loads(self._cache_path.read_text(encoding="utf-8"))
            now = time.monotonic()
            for item in data:
                age = now - item.get("last_check", 0)
                if age > _CACHE_EXPIRY:
                    continue
                self._proxies.append(ProxyEntry(
                    url=item["url"],
                    score=item.get("score", 0.5),
                    latency=item.get("latency", 0.0),
                    last_check=item.get("last_check", now),
                    fail_count=item.get("fail_count", 0),
                ))
            logger.debug("proxy_pool: loaded %d from cache", len(self._proxies))
        except (OSError, json.JSONDecodeError, KeyError) as exc:
            logger.warning("proxy_pool: cache load failed: %s", exc)

    def _save_cache(self) -> None:
        """保存代理列表到缓存文件。"""
        try:
            data = [
                {
                    "url": p.url,
                    "score": p.score,
                    "latency": p.latency,
                    "last_check": p.last_check,
                    "fail_count": p.fail_count,
                }
                for p in self._proxies
            ]
            self._cache_path.write_text(
                json.dumps(data, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
        except OSError as exc:
            logger.warning("proxy_pool: cache save failed: %s", exc)
