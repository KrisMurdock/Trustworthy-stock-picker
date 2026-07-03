"""Core data models for stock_watcher."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Optional


class Market(Enum):
    """Stock market identifier."""

    SHANGHAI = "sh"
    SHENZHEN = "sz"
    HONGKONG = "hk"
    USA = "us"

    @classmethod
    def from_code(cls, code: str) -> "Market":
        """Extract market from a prefixed stock code like 'sh000001'."""
        if not code or len(code) < 2:
            raise ValueError(f"code must have a 2-char prefix: {code!r}")
        prefix = code[:2].lower()
        try:
            return cls(prefix)
        except ValueError:
            raise ValueError(f"Unknown market prefix '{prefix}' in code {code!r}")

    @classmethod
    def is_valid_code(cls, code: str) -> bool:
        """Return True if the code has a known market prefix."""
        try:
            cls.from_code(code)
            return True
        except ValueError:
            return False


@dataclass
class Position:
    """A user's holding in a stock: cost, total quantity, and available shares."""

    cost: float = 0.0
    quantity: int = 0      # 持仓股数（总持有）
    available: int = 0      # 可用股数（可交易）

    @property
    def is_valid(self) -> bool:
        return self.cost > 0 and self.quantity > 0

    def market_value(self, price: float) -> float:
        return price * self.quantity

    def pnl(self, price: float) -> float:
        return (price - self.cost) * self.quantity

    def pnl_pct(self, price: float) -> float:
        if self.cost == 0:
            return 0.0
        return (price - self.cost) / self.cost * 100

    @classmethod
    def from_config(cls, d: dict) -> "Position":
        return cls(
            cost=float(d.get("cost", 0)),
            quantity=int(d.get("quantity", 0)),
            available=int(d.get("available", 0)),
        )

    def to_config(self) -> dict:
        return {"cost": self.cost, "quantity": self.quantity, "available": self.available}


@dataclass(frozen=True)
class StockQuote:
    """Snapshot of a stock price at a point in time.

    Equality is based on `code` only — two quotes for the same stock
    are considered equal regardless of price differences.
    """

    code: str = field(compare=True)
    name: Optional[str] = field(default=None, compare=False)
    price: Optional[float] = field(default=None, compare=False)
    change_pct: Optional[float] = field(default=None, compare=False)
    change_amount: Optional[float] = field(default=None, compare=False)
    high: Optional[float] = field(default=None, compare=False)
    low: Optional[float] = field(default=None, compare=False)

    # Extended fields (v2)
    open: Optional[float] = field(default=None, compare=False)
    volume: Optional[float] = field(default=None, compare=False)       # shares
    turnover: Optional[float] = field(default=None, compare=False)     # yuan
    bid: Optional[float] = field(default=None, compare=False)          # best bid
    ask: Optional[float] = field(default=None, compare=False)          # best ask
    bid_prices: list[float] = field(default_factory=list, compare=False)  # 买1-5
    bid_volumes: list[float] = field(default_factory=list, compare=False)  # 买1-5量
    ask_prices: list[float] = field(default_factory=list, compare=False)   # 卖1-5
    ask_volumes: list[float] = field(default_factory=list, compare=False)   # 卖1-5量
    pe: Optional[float] = field(default=None, compare=False)           # 市盈率 (HK only)
    market_cap: Optional[float] = field(default=None, compare=False)   # 总市值-亿 (HK only)

    @property
    def is_valid(self) -> bool:
        """A quote is valid only when price is available."""
        return self.price is not None and self.price > 0

    @property
    def direction(self) -> str:
        """One of 'up', 'down', 'flat'."""
        if self.change_pct is None:
            return "flat"
        if self.change_pct > 0:
            return "up"
        if self.change_pct < 0:
            return "down"
        return "flat"

    @property
    def fmt_change_pct(self) -> str:
        """Formatted change percentage string, e.g. '+1.25%' or '—'."""
        if self.change_pct is None:
            return "—"
        sign = "+" if self.change_pct > 0 else ""
        return f"{sign}{self.change_pct:.2f}%"


@dataclass
class WatchlistItem:
    """A stock entry in the user's watchlist."""

    code: str
    name: Optional[str] = None

    def __post_init__(self) -> None:
        self.code = self.code.strip().lower()

    def validate(self) -> None:
        """Raise ValueError if the stock code is invalid."""
        if not Market.is_valid_code(self.code):
            raise ValueError(f"Invalid stock code: {self.code!r}")

    def to_config_entry(self) -> str:
        """Serialize to a single config string (the code)."""
        return self.code

    @classmethod
    def from_config(cls, raw: str) -> "WatchlistItem":
        """Deserialize from a config string."""
        return cls(code=raw.strip())


# ---------------------------------------------------------------------------
# Announcement model (cninfo)
# ---------------------------------------------------------------------------


@dataclass
class Announcement:
    """A company announcement from 巨潮资讯网."""

    id: str                          # announcement ID (unique)
    sec_code: str                    # stock code (e.g. "000001")
    sec_name: str                    # stock name (e.g. "平安银行")
    title: str                       # announcement title
    announcement_time: str           # e.g. "2025-07-01 16:30"
    adjunct_url: str = ""            # PDF download path
    org_id: str = ""                 # cninfo org ID

    @property
    def pdf_url(self) -> str:
        """Full PDF download URL."""
        if not self.adjunct_url:
            return ""
        return f"http://static.cninfo.com.cn/{self.adjunct_url}"

    @classmethod
    def from_api_item(cls, item: dict, sec_code: str, org_id: str) -> "Announcement":
        return cls(
            id=str(item.get("id") or item.get("announcementId", "")),
            sec_code=sec_code,
            sec_name=str(item.get("secName", "")),
            title=str(item.get("announcementTitle", "")),
            announcement_time=str(item.get("announcementTime", "")),
            adjunct_url=str(item.get("adjunctUrl", "")),
            org_id=org_id,
        )


# ---------------------------------------------------------------------------
# Alert rule
# ---------------------------------------------------------------------------


class AlertType(Enum):
    PRICE_ABOVE = "price_above"
    PRICE_BELOW = "price_below"
    PCT_ABOVE = "pct_above"
    PCT_BELOW = "pct_below"


@dataclass
class AlertRule:
    """A price or change-% alert for a specific stock."""

    code: str
    alert_type: AlertType
    value: float
    triggered: bool = False

    def check(self, quote: StockQuote) -> bool:
        """Return True if this alert should fire for the given quote."""
        if self.alert_type in (AlertType.PRICE_ABOVE, AlertType.PRICE_BELOW):
            if quote.price is None:
                return False
            if self.alert_type == AlertType.PRICE_ABOVE:
                return quote.price >= self.value
            else:
                return quote.price <= self.value
        elif self.alert_type in (AlertType.PCT_ABOVE, AlertType.PCT_BELOW):
            if quote.change_pct is None:
                return False
            if self.alert_type == AlertType.PCT_ABOVE:
                return quote.change_pct >= self.value
            else:
                return quote.change_pct < 0 and abs(quote.change_pct) >= self.value
        return False

    def describe(self) -> str:
        """Human-readable alert description."""
        labels = {
            AlertType.PRICE_ABOVE: "价格上破",
            AlertType.PRICE_BELOW: "价格下破",
            AlertType.PCT_ABOVE: "涨幅超",
            AlertType.PCT_BELOW: "跌幅超",
        }
        suffix = "%" if self.alert_type in (AlertType.PCT_ABOVE, AlertType.PCT_BELOW) else ""
        return f"{labels[self.alert_type]} {self.value}{suffix}"

    def to_config_dict(self) -> dict:
        return {
            "code": self.code,
            "type": self.alert_type.value,
            "value": self.value,
        }

    @classmethod
    def from_config_dict(cls, d: dict) -> "AlertRule":
        return cls(
            code=str(d["code"]).strip().lower(),
            alert_type=AlertType(d["type"]),
            value=float(d["value"]),
        )


# ---------------------------------------------------------------------------
# News item (news aggregator)
# ---------------------------------------------------------------------------


@dataclass
class NewsItem:
    """A unified news article from any source.

    Equality is based on ``id`` only — two items with the same ID
    are considered duplicates regardless of other field differences.
    """

    id: str = field(compare=True)
    source: str = field(default="", compare=False)       # e.g. "sina_roll", "sina_stock", "yahoo_search"
    title: str = field(default="", compare=False)
    summary: str = field(default="", compare=False)        # content preview, ≤ 200 chars
    url: str = field(default="", compare=False)
    published_at: str = field(default="", compare=False)   # ISO 8601 timestamp
    related_codes: list[str] = field(default_factory=list, compare=False)
    source_priority: int = field(default=2, compare=False) # 1=high, 2=medium, 3=low

    @property
    def has_codes(self) -> bool:
        """True if this news item is linked to specific stock codes."""
        return len(self.related_codes) > 0

    def relates_to(self, codes: set[str]) -> bool:
        """True if this item is relevant to any of the given stock codes."""
        if not self.related_codes:
            return True  # market-wide news, always relevant
        return bool(set(self.related_codes) & codes)


# ---------------------------------------------------------------------------
# Sector stock (sector enrichment)
# ---------------------------------------------------------------------------


@dataclass
class SectorStock:
    """A constituent stock of a concept sector with real-time quote data."""

    code: str
    name: str
    sector: str = ""
    price: float = 0.0
    change_pct: float = 0.0
    high: float = 0.0
    low: float = 0.0
    open: float = 0.0
    prev_close: float = 0.0
    volume: int = 0
    turnover: float = 0.0   # 换手率 (%)
    pe: float = 0.0          # 市盈率-动态
    pb: float = 0.0          # 市净率


# ---------------------------------------------------------------------------
# Trend score (sector enrichment)
# ---------------------------------------------------------------------------


@dataclass
class TrendScore:
    """Technical indicator scoring result for a single stock (0-100 scale)."""

    code: str
    total_score: int = 0
    ma_score: int = 0
    macd_score: int = 0
    rsi_score: int = 0
    consecutive_up: int = 0
    volume_ratio_score: int = 0
    ma5: float = 0.0
    ma10: float = 0.0
    ma20: float = 0.0
    rsi14: float = 0.0
    consecutive_up_days: int = 0
    reason: str = ""
