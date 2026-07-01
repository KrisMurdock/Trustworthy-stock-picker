"""Configuration loading, persistence, and watchlist management."""

from __future__ import annotations

import random
import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Optional

import yaml

from stock_watcher.models import AlertRule, Position


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

    # Webhook (out-bound only)
    feishu_webhook: str = ""
    feishu_secret: str = ""

    # Full bot credentials (for WebSocket event receiving + API calls)
    feishu_app_id: str = ""
    feishu_app_secret: str = ""

    @property
    def is_configured(self) -> bool:
        return bool(self.feishu_webhook)

    @property
    def can_receive(self) -> bool:
        """True when the bot can receive @messages via WebSocket."""
        return bool(self.feishu_app_id and self.feishu_app_secret)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> "ChatConfig":
        return cls(
            feishu_webhook=str(d.get("feishu_webhook", "")),
            feishu_secret=str(d.get("feishu_secret", "")),
            feishu_app_id=str(d.get("feishu_app_id", "")),
            feishu_app_secret=str(d.get("feishu_app_secret", "")),
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "feishu_webhook": self.feishu_webhook,
            "feishu_secret": self.feishu_secret,
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
            volatility_threshold=float(d.get("volatility_threshold", 3.0)),
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
