"""新闻 AI 分析 — DeepSeek 多维度分类（级别 + 摘要 + 涉及标的 + 多空）."""

from __future__ import annotations

import asyncio as _asyncio
import json
import logging
import time
from dataclasses import dataclass, field
from enum import Enum

import httpx

from stock_watcher.config import DeepSeekConfig
from stock_watcher.models import NewsItem

logger = logging.getLogger(__name__)

CLASSIFY_TIMEOUT = 25
MAX_CALLS_PER_MINUTE = 30


class NewsLevel(Enum):
    CRITICAL = "critical"
    NOTABLE = "notable"
    IGNORE = "ignore"


@dataclass
class ClassifyResult:
    """AI 分析结果."""
    level: NewsLevel = NewsLevel.NOTABLE
    summary: str = ""          # AI 生成的 1-2 句摘要
    entities: list[str] = field(default_factory=list)  # 全部关联标的（兼容旧字段）
    stocks: list[str] = field(default_factory=list)    # 涉及的个股
    sectors: list[str] = field(default_factory=list)   # 涉及的板块
    sentiment: str = ""        # "利好" | "利空" | "中性"
    reason: str = ""           # 判定理由


_ANALYZE_PROMPT = """你是一个资深财经新闻分析师。请对以下新闻做多维度分析。

分析维度：
1. level: 重要性分级标准：
   - critical：满足任一即判为 critical — ①直接影响持仓个股的重大事件（业绩、重组、增减持等），②重大宏观政策/行业变革（利率调整、产业政策、监管新规、贸易政策等），③热门赛道/板块龙头的重大事件（技术突破、市场份额剧变、重大合作/并购），④可能导致市场大幅波动的突发事件（地缘政治、自然灾害影响供应链、黑天鹅事件）
   - notable：对市场/行业有一般性影响但非紧急的新闻
   - ignore：常规报道、重复信息、与投资决策无关的内容
2. summary: 用 1-2 句简洁中文总结核心内容，突出对投资者的影响
3. stocks: 涉及的个股名称列表，如["贵州茅台", "宁德时代"]，无关则 []
4. sectors: 涉及的板块/行业名称列表，如["白酒板块", "新能源"]，无关则 []
5. sentiment: 对相关标的的综合影响 — 利好 / 利空 / 中性
6. reason: 一句话（≤25字）说明判定理由

请仅输出 JSON（不要 markdown 包裹）：
{"level":"critical|notable|ignore","summary":"摘要","stocks":[],"sectors":[],"sentiment":"利好|利空|中性","reason":"理由"}"""


class NewsClassifier:
    """AI 新闻分析器 — 级别 + 摘要 + 标的 + 多空."""

    def __init__(
        self,
        deepseek_cfg: DeepSeekConfig | None,
        critical_keywords: list[str] | None = None,
        ignore_keywords: list[str] | None = None,
        watchlist_info: str = "",
    ) -> None:
        self._cfg = deepseek_cfg
        self._critical_kw = [k.lower() for k in (critical_keywords or [])]
        self._ignore_kw = [k.lower() for k in (ignore_keywords or [])]
        self._watchlist_info = watchlist_info
        self._cache: dict[str, ClassifyResult] = {}
        self._call_times: list[float] = []

    # ------------------------------------------------------------------
    # 主入口
    # ------------------------------------------------------------------

    def set_watchlist(self, codes: list[str]) -> None:
        """设置当前自选股列表，AI 分类时会据此判断新闻相关性."""
        if codes:
            self._watchlist_info = f"自选股代码: {', '.join(codes[:15])}"
        else:
            self._watchlist_info = ""

    async def classify(self, item: NewsItem) -> ClassifyResult:
        """对单条新闻做全维度分析."""
        if item.id in self._cache:
            return self._cache[item.id]

        text = f"{item.title} {item.summary}".lower()

        # 检查关键词，预判级别
        preset_level: NewsLevel | None = None
        preset_reason = ""
        for kw in self._critical_kw:
            if kw in text:
                preset_level = NewsLevel.CRITICAL
                preset_reason = f"关键词: {kw}"
                break
        if preset_level is None:
            for kw in self._ignore_kw:
                if kw in text:
                    preset_level = NewsLevel.IGNORE
                    preset_reason = f"关键词: {kw}"
                    break

        # AI 全维度分析（关键词匹配后仍调用 AI，获取摘要+标的+多空）
        if self._cfg and self._cfg.is_configured:
            ai_result = await self._ai_analyze(item)
            # 关键词预判的 level 优先于 AI 的 level
            if preset_level is not None:
                ai_result.level = preset_level
                if preset_reason:
                    ai_result.reason = preset_reason
            self._cache[item.id] = ai_result
            return ai_result

        # 无 AI：降级输出
        result = ClassifyResult(
            level=preset_level or NewsLevel.NOTABLE,
            summary=item.summary or item.title,
            entities=item.related_codes,
            sentiment="中性",
            reason=preset_reason or "AI 未配置",
        )
        self._cache[item.id] = result
        return result

    # ------------------------------------------------------------------
    # AI 调用
    # ------------------------------------------------------------------

    async def _ai_analyze(self, item: NewsItem) -> ClassifyResult:
        """调用 DeepSeek 做全维度分析."""
        if not self._cfg or not self._cfg.is_configured:
            return ClassifyResult(level=NewsLevel.NOTABLE, reason="AI 不可用")

        self._rate_limit()

        url = f"{self._cfg.api_base.rstrip('/')}/chat/completions"
        user_content = f"标题：{item.title}\n内容：{item.summary[:300]}"
        if self._watchlist_info:
            user_content = f"用户持仓关注：{self._watchlist_info}\n\n{user_content}"
        messages = [
            {"role": "system", "content": _ANALYZE_PROMPT},
            {"role": "user", "content": user_content},
        ]

        for attempt in range(2):
            try:
                async with httpx.AsyncClient(timeout=CLASSIFY_TIMEOUT) as client:
                    resp = await client.post(
                        url,
                        json={
                            "model": self._cfg.model,
                            "messages": messages,
                            "temperature": 0.3,
                            "max_tokens": 2000,
                        },
                        headers={
                            "Authorization": f"Bearer {self._cfg.api_key}",
                            "Content-Type": "application/json",
                        },
                    )
                if resp.status_code == 200:
                    data = resp.json()
                    choice = data.get("choices", [{}])[0]
                    msg = choice.get("message", {})
                    content = msg.get("content", "").strip()

                    # 推理模型兼容：content 为空时取 reasoning_content
                    if not content:
                        content = msg.get("reasoning_content", "").strip()

                    if content:
                        result = self._parse_response(content, item)
                        self._cache[item.id] = result
                        return result
            except Exception as exc:
                logger.warning("DeepSeek analyze attempt %d failed: %s", attempt + 1, exc)
                if attempt == 0:
                    await _asyncio.sleep(1)

        return ClassifyResult(level=NewsLevel.NOTABLE, reason="API 调用失败")

    def _parse_response(self, content: str, item: NewsItem) -> ClassifyResult:
        """解析 AI 返回的 JSON."""
        try:
            # 清理可能的 markdown 包裹
            if "```" in content:
                parts = content.split("```")
                content = parts[1] if len(parts) >= 2 else content
                if content.startswith("json"):
                    content = content[4:]

            data = json.loads(content.strip())

            level_map = {
                "critical": NewsLevel.CRITICAL,
                "notable": NewsLevel.NOTABLE,
                "ignore": NewsLevel.IGNORE,
            }
            level_str = data.get("level", "notable")

            # 新版：stocks + sectors；旧版 entities 做兼容
            stocks = list(data.get("stocks", [])) if isinstance(data.get("stocks"), list) else []
            sectors = list(data.get("sectors", [])) if isinstance(data.get("sectors"), list) else []
            entities = list(data.get("entities", [])) if isinstance(data.get("entities"), list) else []

            # 如果只有旧版 entities，按名称特征拆分到 stocks/sectors
            if not stocks and not sectors and entities:
                for e in entities:
                    es = str(e)
                    if any(kw in es for kw in ("板块", "行业", "概念", "指数", "赛道", "领域")):
                        sectors.append(es)
                    else:
                        stocks.append(es)

            return ClassifyResult(
                level=level_map.get(level_str, NewsLevel.NOTABLE),
                summary=str(data.get("summary", item.summary or item.title)),
                entities=entities if entities else stocks + sectors,
                stocks=stocks,
                sectors=sectors,
                sentiment=str(data.get("sentiment", "中性")),
                reason=str(data.get("reason", "")),
            )
        except (json.JSONDecodeError, KeyError, IndexError):
            return ClassifyResult(
                level=NewsLevel.NOTABLE,
                summary=item.summary or item.title,
                entities=item.related_codes,
                stocks=[],
                sectors=[],
                sentiment="中性",
                reason="AI 解析失败",
            )

    def _rate_limit(self) -> None:
        """频率限制."""
        now = time.monotonic()
        self._call_times = [t for t in self._call_times if now - t < 60]
        if len(self._call_times) >= MAX_CALLS_PER_MINUTE:
            logger.warning("rate limit reached (%d calls/min)", MAX_CALLS_PER_MINUTE)
        self._call_times.append(now)
