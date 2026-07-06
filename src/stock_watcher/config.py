"""Configuration loading, persistence, and watchlist management."""

from __future__ import annotations

import random
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Optional

import yaml

from stock_watcher.models import AlertRule, Position


# ---------------------------------------------------------------------------
# Module-level defaults used by dataclass field factories
# ---------------------------------------------------------------------------

_DEFAULT_CRITICAL_KEYWORDS = [
    "停牌", "退市", "重大资产重组", "并购", "业绩暴雷",
    "立案调查", "强制退市", "暂停上市",
]

_DEFAULT_IGNORE_KEYWORDS = [
    "龙虎榜", "大宗交易", "融资融券",
]


# The YAML key for the watchlist
WATCHLIST_KEY = "watchlist"
ALERTS_KEY = "alerts"


# ---------------------------------------------------------------------------
# Config value objects
# ---------------------------------------------------------------------------


@dataclass
class BackoffConfig:
    """Exponential-backoff parameters for failed requests."""

    base: float = 5.0
    max_delay: float = 120.0
    multiplier: float = 2.0

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "BackoffConfig":
        return cls(
            base=float(d.get("base", 5)),
            max_delay=float(d.get("max", d.get("max_delay", 120))),
            multiplier=float(d.get("multiplier", 2)),
        )


@dataclass
class RequestConfig:
    """HTTP request configuration."""

    timeout: float = 10.0
    user_agent_pool: list[str] = field(default_factory=lambda: [
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
        "(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
    ])

    def get_random_ua(self) -> str:
        """Return a random User-Agent from the pool."""
        return random.choice(self.user_agent_pool)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "RequestConfig":
        timeout = float(d.get("timeout", 10))
        ua_pool = d.get("user_agent_pool", None)
        if ua_pool and isinstance(ua_pool, list) and len(ua_pool) > 0:
            return cls(timeout=timeout, user_agent_pool=[str(x) for x in ua_pool])
        return cls(timeout=timeout)


# ---------------------------------------------------------------------------
# ChatConfig (Feishu / DingTalk webhook)
# ---------------------------------------------------------------------------


@dataclass
class ChatConfig:
    """Chat bot configuration — webhook (push) + full bot (receive & reply)."""

    # Webhook (out-bound only) — 行情/告警/汇总推送
    feishu_webhook: str = ""
    feishu_secret: str = ""

    # 新闻推送专用 Webhook（独立频道，避免刷屏）
    news_webhook: str = ""
    news_secret: str = ""

    # Full bot credentials (for WebSocket event receiving + API calls)
    feishu_app_id: str = ""
    feishu_app_secret: str = ""

    @property
    def is_configured(self) -> bool:
        return bool(self.feishu_webhook)

    @property
    def news_configured(self) -> bool:
        return bool(self.news_webhook)

    @property
    def can_receive(self) -> bool:
        """True when the bot can receive @messages via WebSocket."""
        return bool(self.feishu_app_id and self.feishu_app_secret)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "ChatConfig":
        return cls(
            feishu_webhook=str(d.get("feishu_webhook", "")),
            feishu_secret=str(d.get("feishu_secret", "")),
            news_webhook=str(d.get("news_webhook", "")),
            news_secret=str(d.get("news_secret", "")),
            feishu_app_id=str(d.get("feishu_app_id", "")),
            feishu_app_secret=str(d.get("feishu_app_secret", "")),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "feishu_webhook": self.feishu_webhook,
            "feishu_secret": self.feishu_secret,
            "news_webhook": self.news_webhook,
            "news_secret": self.news_secret,
            "feishu_app_id": self.feishu_app_id,
            "feishu_app_secret": self.feishu_app_secret,
        }




@dataclass
class DeepSeekConfig:
    """DeepSeek API for AI-powered chat replies."""

    api_key: str = ""
    api_base: str = "https://api.deepseek.com"
    model: str = "deepseek-chat"

    @property
    def is_configured(self) -> bool:
        return bool(self.api_key)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "DeepSeekConfig":
        base = d.get("base_url") or d.get("api_base") or "https://api.deepseek.com"
        return cls(
            api_key=str(d.get("api_key", "")),
            api_base=str(base),
            model=str(d.get("model", "deepseek-chat")),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "api_key": self.api_key,
            "api_base": self.api_base,
            "model": self.model,
        }


@dataclass
class CninfoConfig:
    """巨潮资讯网公告轮询配置."""

    enabled: bool = False
    poll_interval: float = 120.0  # 公告轮询间隔（秒），默认 2 分钟
    page_size: int = 10           # 每次查询获取的公告条数
    summary_enabled: bool = True  # 是否用 DeepSeek 生成摘要
    # 重大事项关键词过滤（公告标题包含任一关键词即推送）
    major_keywords: list[str] = field(default_factory=lambda: [
        "年报", "年度报告",
        "季报", "季度报告",
        "半年报", "半年度报告",
        "业绩预告", "业绩快报",
        "重大合同", "重大资产重组",
        "减持", "增持",
        "停牌", "复牌",
        "分红", "送转", "权益分派",
        "风险警示",
    ])
    # 记录的公告 ID 持久化文件
    seen_ids_path: str = "cninfo_seen.jsonl"

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "CninfoConfig":
        return cls(
            enabled=bool(d.get("enabled", False)),
            poll_interval=float(d.get("poll_interval", 120)),
            page_size=int(d.get("page_size", 10)),
            summary_enabled=bool(d.get("summary_enabled", True)),
            major_keywords=[str(k) for k in d.get("major_keywords", [])] or cls.major_keywords,
            seen_ids_path=str(d.get("seen_ids_path", "cninfo_seen.jsonl")),
        )


@dataclass
class AppConfig:
    """Top-level application configuration."""

    poll_interval: float = 2.5
    backoff: BackoffConfig = field(default_factory=BackoffConfig)
    request: RequestConfig = field(default_factory=RequestConfig)
    watchlist: list[str] = field(default_factory=list)
    alerts: list[AlertRule] = field(default_factory=list)
    proxies: list[str] = field(default_factory=list)
    positions: dict[str, Position] = field(default_factory=dict)
    alert_sound_command: str = ""
    volatility_threshold: float = 3.0
    chat: Optional[ChatConfig] = None
    deepseek: Optional[DeepSeekConfig] = None
    cninfo: Optional[CninfoConfig] = None
    news: Optional["NewsConfig"] = None

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "AppConfig":
        pos_raw = d.get("positions", {}) or {}
        return cls(
            poll_interval=float(d.get("poll_interval", 2.5)),
            backoff=BackoffConfig.from_dict(d.get("backoff", {})),
            request=RequestConfig.from_dict(d.get("request", {})),
            watchlist=[str(x) for x in d.get("watchlist", [])],
            alerts=[AlertRule.from_config_dict(a) for a in d.get("alerts") or []],
            proxies=[str(x) for x in d.get("proxies", [])],
            positions={k: Position.from_config(v) for k, v in pos_raw.items()},
            alert_sound_command=str(d.get("alert_sound_command", "")),
            chat=ChatConfig.from_dict(d.get("chat", {})) if d.get("chat") else None,
            deepseek=DeepSeekConfig.from_dict(d.get("deepseek", {})) if d.get("deepseek") else None,
            cninfo=CninfoConfig.from_dict(d.get("cninfo", {})) if d.get("cninfo") else None,
            volatility_threshold=float(d.get("volatility_threshold", 3.0)),
            news=NewsConfig.from_dict(d.get("news", {})) if d.get("news") else None,
        )


# ---------------------------------------------------------------------------
# News aggregation config
# ---------------------------------------------------------------------------


@dataclass
class ScheduledPushConfig:
    """单个定时推送的时间与开关."""

    enabled: bool = True
    time: str = ""  # "HH:MM" format

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "ScheduledPushConfig":
        return cls(
            enabled=bool(d.get("enabled", True)),
            time=str(d.get("time", "")),
        )


@dataclass
class SectorKeywordsConfig:
    """板块关键词映射配置."""

    keywords: dict[str, list[str]] = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "SectorKeywordsConfig":
        if not d:
            return cls()
        result: dict[str, list[str]] = {}
        for k, v in d.items():
            result[str(k)] = [str(x) for x in v] if isinstance(v, list) else []
        return cls(keywords=result)


@dataclass
class TrendScoringConfig:
    """技术指标打分权重配置."""

    ma_weight: int = 30
    macd_weight: int = 25
    rsi_weight: int = 20
    consecutive_up_weight: int = 15
    volume_ratio_weight: int = 10
    lookback_days: int = 60
    min_score_threshold: int = 50
    top_n_candidates: int = 10
    top_n_final: int = 5
    max_stocks_per_card: int = 8

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "TrendScoringConfig":
        if not d:
            return cls()
        return cls(
            ma_weight=int(d.get("ma_weight", 30)),
            macd_weight=int(d.get("macd_weight", 25)),
            rsi_weight=int(d.get("rsi_weight", 20)),
            consecutive_up_weight=int(d.get("consecutive_up_weight", 15)),
            volume_ratio_weight=int(d.get("volume_ratio_weight", 10)),
            lookback_days=int(d.get("lookback_days", 60)),
            min_score_threshold=int(d.get("min_score_threshold", 50)),
            top_n_candidates=int(d.get("top_n_candidates", 10)),
            top_n_final=int(d.get("top_n_final", 5)),
            max_stocks_per_card=int(d.get("max_stocks_per_card", 8)),
        )


@dataclass
class NewsConfig:
    """新闻聚合与推送配置."""

    poll_interval: float = 300.0  # 秒
    # 新闻源开关
    sources: dict[str, bool] = field(default_factory=lambda: {
        "eastmoney_stock": True,
        "eastmoney_flash": True,
        "sina_flash": True,
        "cls_telegraph": True,
        "yahoo_news": True,
    })
    # 板块关键词映射
    sector_keywords: SectorKeywordsConfig = field(default_factory=SectorKeywordsConfig)
    # 技术指标打分权重
    trend_scoring: TrendScoringConfig = field(default_factory=TrendScoringConfig)
    # 重要性分类关键词
    critical_keywords: list[str] = field(default_factory=lambda: _DEFAULT_CRITICAL_KEYWORDS.copy())
    ignore_keywords: list[str] = field(default_factory=lambda: _DEFAULT_IGNORE_KEYWORDS.copy())
    # 定时推送
    scheduled_push: dict[str, ScheduledPushConfig] = field(default_factory=lambda: {
        "morning_briefing": ScheduledPushConfig(time="09:00"),
        "market_open": ScheduledPushConfig(time="09:25"),
        "a_share_close": ScheduledPushConfig(time="15:05"),
        "hk_close": ScheduledPushConfig(time="16:15"),
        "evening_briefing": ScheduledPushConfig(time="20:00"),
    })
    seen_ids_path: str = "news_seen.jsonl"

    @property
    def enabled_sources(self) -> list[str]:
        return [k for k, v in self.sources.items() if v]

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "NewsConfig":
        sources = {
            "eastmoney_stock": bool(d.get("sources", {}).get("eastmoney_stock", True)),
            "eastmoney_flash": bool(d.get("sources", {}).get("eastmoney_flash", True)),
            "sina_flash": bool(d.get("sources", {}).get("sina_flash", True)),
            "cls_telegraph": bool(d.get("sources", {}).get("cls_telegraph", True)),
            "yahoo_news": bool(d.get("sources", {}).get("yahoo_news", True)),
        }
        sp_raw = d.get("scheduled_push", {}) or {}
        scheduled_push = {
            key: ScheduledPushConfig.from_dict(sp_raw.get(key, {}))
            for key in ["morning_briefing", "market_open", "a_share_close", "hk_close", "evening_briefing"]
        }
        return cls(
            poll_interval=float(d.get("poll_interval", 300.0)),
            sources=sources,
            sector_keywords=SectorKeywordsConfig.from_dict(d.get("sector_keywords", {})),
            trend_scoring=TrendScoringConfig.from_dict(d.get("trend_scoring", {})),
            critical_keywords=[str(k) for k in d.get("critical_keywords", [])] or _DEFAULT_CRITICAL_KEYWORDS,
            ignore_keywords=[str(k) for k in d.get("ignore_keywords", [])] or _DEFAULT_IGNORE_KEYWORDS,
            scheduled_push=scheduled_push,
            seen_ids_path=str(d.get("seen_ids_path", "news_seen.jsonl")),
        )


# ---------------------------------------------------------------------------
# I/O functions
# ---------------------------------------------------------------------------


def load_config(path: Path) -> AppConfig:
    """Load configuration from a YAML file.

    Raises FileNotFoundError if the file does not exist.
    Positions are loaded from a separate positions.yaml next to the config.
    """
    if not path.exists():
        raise FileNotFoundError(f"Config file not found: {path}")

    with open(path, "r", encoding="utf-8") as fh:
        raw = yaml.safe_load(fh) or {}

    # Merge positions from positions.yaml if it exists
    pos_path = path.with_name("positions.yaml")
    if pos_path.exists():
        with open(pos_path, "r", encoding="utf-8") as fh:
            pos_data = yaml.safe_load(fh) or {}
        if isinstance(pos_data, dict):
            raw["positions"] = pos_data

    return AppConfig.from_dict(raw)


def _save_yaml_list(path: Path, key: str, item_texts: list[str]) -> None:
    """Replace a top-level YAML list key in-place, preserving all comments.

    Reads the file as text, finds the ``key:`` block (from the key line
    through its indented list items), and replaces just that block.
    If *item_texts* is empty, writes ``key: []`` on one line.
    Each entry in *item_texts* is one complete list item — use newlines
    within an entry to produce multi-line dict items.
    """
    if item_texts:
        items_block = "\n".join(f"- {t}" for t in item_texts)
        new_block = f"{key}:\n{items_block}\n"
    else:
        new_block = f"{key}: []\n"

    if path.exists():
        text = path.read_text(encoding="utf-8")
    else:
        text = ""

    # Match a top-level YAML key (no leading whitespace) followed by its
    # content — list items (starting with "-") and their continuation
    # lines (indented).  Stops before the next top-level key, comment, or
    # an empty line.
    pattern = re.compile(
        rf"^{re.escape(key)}:.*(?:\n(?:[ \t]+\S.*|-.*))*",
        re.MULTILINE,
    )

    if pattern.search(text):
        text = pattern.sub(new_block, text)
    else:
        # Key does not exist yet: append at end, with a blank separating
        # line when the file doesn't already end with one.
        if text and not text.endswith("\n"):
            text += "\n"
        text += "\n" + new_block

    path.write_text(text, encoding="utf-8")


def save_watchlist(path: Path, watchlist: list[str]) -> None:
    """Persist the watchlist, keeping all comments and other settings intact."""
    _save_yaml_list(path, WATCHLIST_KEY, list(watchlist))


def save_alerts(path: Path, alerts: list[AlertRule]) -> None:
    """Persist the alerts, keeping all comments and other settings intact."""
    items: list[str] = []
    for rule in alerts:
        d = rule.to_config_dict()
        items.append(f"code: {d['code']}\n  type: {d['type']}\n  value: {d['value']}")
    _save_yaml_list(path, ALERTS_KEY, items)


def save_positions(path: Path, positions: dict[str, "Position"]) -> None:
    """Persist positions to a standalone positions.yaml next to config."""
    pos_path = path.with_name("positions.yaml")

    data: dict[str, dict[str, float | int]] = {}
    for code, pos in positions.items():
        data[code] = pos.to_config()  # type: ignore[assignment]

    with open(pos_path, "w", encoding="utf-8") as fh:
        yaml.safe_dump(data, fh, default_flow_style=False, allow_unicode=True)
