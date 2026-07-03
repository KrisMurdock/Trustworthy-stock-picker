"""趋势排序器 — 规则初筛 + DeepSeek 精选 Top5，分级输出."""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

import httpx

from stock_watcher.models import SectorStock, TrendScore

if TYPE_CHECKING:
    from stock_watcher.config import DeepSeekConfig

logger = logging.getLogger(__name__)

RANK_TIMEOUT = 25


@dataclass
class RankedResult:
    """Tiered stock ranking result."""
    tier1: list[SectorStock] = field(default_factory=list)  # 持仓
    tier2: list[SectorStock] = field(default_factory=list)  # 自选
    tier3: list[SectorStock] = field(default_factory=list)  # 趋势精选

    @property
    def all(self) -> list[SectorStock]:
        return self.tier1 + self.tier2 + self.tier3


class TrendRanker:
    """Two-stage ranking: rule pre-filter → DeepSeek top5, with tiered output."""

    def __init__(
        self,
        deepseek_cfg: DeepSeekConfig | None = None,
        min_score: int = 50,
        top_n_candidates: int = 10,
        top_n_final: int = 5,
        max_stocks: int = 8,
    ) -> None:
        self._cfg = deepseek_cfg
        self._min_score = min_score
        self._top_n_candidates = top_n_candidates
        self._top_n_final = top_n_final
        self._max_stocks = max_stocks

    def rank(
        self,
        stocks: list[SectorStock],
        scores: dict[str, TrendScore],
        positions: set[str],
        watchlist: set[str],
    ) -> RankedResult:
        """Classify stocks into tiers and rank Tier3 by trend strength.

        Args:
            stocks: Constituent stocks with real-time quotes.
            scores: TrendScore per stock code.
            positions: Prefixed position codes (e.g. {"sh600519"}).
            watchlist: Prefixed watchlist codes (e.g. {"sh600519", "sz000001"}).

        Returns:
            RankedResult with tier1 (positions), tier2 (watchlist), tier3 (ranked).
        """
        if not stocks:
            return RankedResult()

        # Normalize position/watchlist codes to raw codes for matching
        pos_raw: set[str] = {_strip_prefix(c) for c in positions}
        wl_raw: set[str] = {_strip_prefix(c) for c in watchlist}

        tier1: list[SectorStock] = []
        tier2: list[SectorStock] = []
        tier3_candidates: list[SectorStock] = []

        for s in stocks:
            raw = _strip_prefix(s.code)
            ts = scores.get(s.code) or scores.get(raw)
            score_val = ts.total_score if ts else 0

            if raw in pos_raw:
                tier1.append(s)
            elif raw in wl_raw:
                tier2.append(s)
            elif score_val >= self._min_score:
                tier3_candidates.append(s)

        # Sort within tiers by score
        tier1.sort(key=lambda s: scores.get(_strip_prefix(s.code), TrendScore(code=s.code)).total_score, reverse=True)
        tier2.sort(key=lambda s: scores.get(_strip_prefix(s.code), TrendScore(code=s.code)).total_score, reverse=True)
        tier3_candidates.sort(key=lambda s: scores.get(_strip_prefix(s.code), TrendScore(code=s.code)).total_score, reverse=True)

        # If Tier1+Tier2 already fill max_stocks, skip Tier3
        if len(tier1) + len(tier2) >= self._max_stocks:
            return RankedResult(
                tier1=tier1[:self._max_stocks],
                tier2=tier2[:max(0, self._max_stocks - len(tier1))],
            )

        # Tier3: top_n_candidates → DeepSeek (or rule fallback)
        candidates = tier3_candidates[:self._top_n_candidates]
        slots_left = self._max_stocks - len(tier1) - len(tier2)

        if not candidates:
            return RankedResult(tier1=tier1, tier2=tier2)

        if self._cfg and self._cfg.is_configured:
            ranked_candidates = self._deepseek_rank(candidates, scores, slots_left)
        else:
            # Rule-based: take top N by score
            ranked_candidates = candidates[:min(slots_left, self._top_n_final)]

        return RankedResult(
            tier1=tier1,
            tier2=tier2,
            tier3=ranked_candidates[:slots_left],
        )

    def _deepseek_rank(
        self,
        candidates: list[SectorStock],
        scores: dict[str, TrendScore],
        top_n: int,
    ) -> list[SectorStock]:
        """Use DeepSeek to select top N from candidates."""
        if not self._cfg:
            return candidates[:top_n]

        import asyncio
        return asyncio.run(self._deepseek_rank_async(candidates, scores, top_n))

    async def _deepseek_rank_async(
        self,
        candidates: list[SectorStock],
        scores: dict[str, TrendScore],
        top_n: int,
    ) -> list[SectorStock]:
        candidate_data = []
        for s in candidates:
            ts = scores.get(s.code, TrendScore(code=s.code))
            candidate_data.append({
                "code": s.code,
                "name": s.name,
                "price": s.price,
                "change_pct": s.change_pct,
                "ma5": ts.ma5,
                "ma10": ts.ma10,
                "ma20": ts.ma20,
                "rsi14": ts.rsi14,
                "consecutive_up_days": ts.consecutive_up_days,
                "total_score": ts.total_score,
            })

        prompt = (
            "你是一个量化分析师。根据以下股票的技术指标数据，"
            f"选出未来3天最可能继续上涨的{top_n}只股票，按置信度排序。\n"
            f"数据：{json.dumps(candidate_data, ensure_ascii=False)}\n"
            f"请仅输出JSON：{{\"top{top_n}\":[{{\"code\":\"...\",\"name\":\"...\",\"confidence\":0.XX,\"reason\":\"一句话\"}}]}}"
        )

        try:
            url = f"{self._cfg.api_base.rstrip('/')}/chat/completions"
            async with httpx.AsyncClient(timeout=RANK_TIMEOUT) as client:
                resp = await client.post(
                    url,
                    json={
                        "model": self._cfg.model,
                        "messages": [
                            {"role": "system", "content": "你是一个量化分析师。请仅输出JSON。"},
                            {"role": "user", "content": prompt},
                        ],
                        "temperature": 0.1,
                        "max_tokens": 500,
                    },
                    headers={
                        "Authorization": f"Bearer {self._cfg.api_key}",
                        "Content-Type": "application/json",
                    },
                )
            if resp.status_code != 200:
                raise RuntimeError(f"DeepSeek returned {resp.status_code}")

            data = resp.json()
            content = data.get("choices", [{}])[0].get("message", {}).get("content", "")
            if not content:
                raise RuntimeError("Empty response")

            # Parse JSON from content
            if "```" in content:
                parts = content.split("```")
                content = parts[1] if len(parts) >= 2 else content
                if content.startswith("json"):
                    content = content[4:]
            parsed = json.loads(content.strip())
            top_key = f"top{top_n}"
            ranked_codes = [item["code"] for item in parsed.get(top_key, [])]

        except Exception:
            logger.warning("TrendRanker: DeepSeek failed, falling back to rule ranking", exc_info=True)
            # Fallback: rule-ranking
            return candidates[:top_n]

        # Map codes back to SectorStock objects
        stock_map = {s.code: s for s in candidates}
        result = []
        for code in ranked_codes:
            if code in stock_map:
                result.append(stock_map[code])
        # Pad with rule-ranked if AI returned fewer
        if len(result) < top_n:
            for s in candidates:
                if s not in result:
                    result.append(s)
                    if len(result) >= top_n:
                        break
        return result[:top_n]


def _strip_prefix(code: str) -> str:
    """Remove sh/sz/hk prefix from stock code."""
    code = code.lower()
    if code.startswith(("sh", "sz", "hk", "us")):
        return code[2:]
    return code
