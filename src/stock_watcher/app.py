"""Textual TUI dashboard for real-time stock monitoring (A-share + HK)."""

from __future__ import annotations

import asyncio
import datetime as dt
import json
import os
import shlex
import subprocess
import sys
import time
from pathlib import Path
from typing import Optional

from textual import work
from textual.app import App, ComposeResult
from textual.containers import Vertical
from textual.reactive import reactive
from textual.screen import ModalScreen
from textual.widgets import (
    DataTable,
    Footer,
    Input,
    Label,
    ListItem,
    ListView,
    RichLog,
    Static,
)
from textual.binding import Binding

from stock_watcher.config import load_config, save_watchlist, save_alerts, save_positions, AppConfig
from stock_watcher.fetcher import get_fetcher, FetchError, search_stocks, SearchResult
from stock_watcher.models import StockQuote, Market, AlertRule, AlertType, Position
from stock_watcher.scheduler import (
    PollQueue,
    TradingCalendar,
    MarketBackoffManager,
    _now_cst,
    _is_weekday,
    is_any_market_open,
)


# ---------------------------------------------------------------------------
# Color constants (A-share convention: red = up, green = down)
# ---------------------------------------------------------------------------
UP_COLOR = "#ff4444"
DOWN_COLOR = "#00cc66"
FLAT_COLOR = "#aaaaaa"
UP_BG = "#3d1a1a"
DOWN_BG = "#1a3d24"

# ---------------------------------------------------------------------------
# Status bar
# ---------------------------------------------------------------------------


class StatusBar(Static):
    """Top bar showing market status, clock, stats, and latency."""

    status: reactive[str] = reactive("")
    last_update: reactive[str] = reactive("—")
    errors: reactive[int] = reactive(0)
    latency: reactive[str] = reactive("—")
    total: reactive[int] = reactive(0)
    up_count: reactive[int] = reactive(0)
    down_count: reactive[int] = reactive(0)
    flat_count: reactive[int] = reactive(0)

    def watch_status(self, _: str) -> None:
        self.refresh()

    def watch_last_update(self, _: str) -> None:
        self.refresh()

    def watch_errors(self, _: int) -> None:
        self.refresh()

    def watch_latency(self, _: str) -> None:
        self.refresh()

    def watch_total(self, _: int) -> None:
        self.refresh()

    def watch_up_count(self, _: int) -> None:
        self.refresh()

    def watch_down_count(self, _: int) -> None:
        self.refresh()

    def watch_flat_count(self, _: int) -> None:
        self.refresh()

    def render(self) -> str:
        now = dt.datetime.now().strftime("%H:%M:%S")
        parts = [f"[bold]📊 {self.status}[/bold]"]
        parts.append(f"🕐 {now}")
        parts.append(f"⏱ {self.latency}")
        parts.append(
            f"📈 [bold]监控 {self.total} 只[/bold]  "
            f"[{UP_COLOR}]↑{self.up_count}[/]  "
            f"[{DOWN_COLOR}]↓{self.down_count}[/]  "
            f"[{FLAT_COLOR}]→{self.flat_count}[/]"
        )
        if self.errors > 0:
            parts.append(f"[bold #ffaa00]⚠ 错误 {self.errors}[/]")
        return "  │  ".join(parts)


class PortfolioBar(Static):
    """Bottom bar showing total portfolio stats."""

    total_mval: reactive[float] = reactive(0.0)
    total_pnl: reactive[float] = reactive(0.0)
    total_pnlp: reactive[float] = reactive(0.0)
    pos_count: reactive[int] = reactive(0)

    def watch_total_mval(self, _: float) -> None:
        self.refresh()

    def render(self) -> str:
        if self.pos_count == 0:
            return "  [dim]暂无持仓[/]"

        pnl = self.total_pnl
        pnlp = self.total_pnlp
        c = UP_COLOR if pnl >= 0 else DOWN_COLOR
        sign = "+" if pnl >= 0 else ""

        return (
            f"  📦 [bold]持仓 {self.pos_count} 只[/bold]  │  "
            f"💰 总市值 [bold]{self.total_mval:,.0f}[/bold] 元  │  "
            f"📈 总盈亏 [{c}]{sign}{pnl:+,.0f}[/]  "
            f"([{c}]{sign}{pnlp:.2f}%[/])"
        )


# ---------------------------------------------------------------------------
# Stock table
# ---------------------------------------------------------------------------


class StockTable(DataTable):
    """DataTable specialised for stock quote display."""

    privacy_mode: reactive[bool] = reactive(False)

    # Column keys (must match add_columns labels + on_mount)
    COL_DIR = " "
    COL_CODE = "代码"
    COL_NAME = "名称"
    COL_PRICE = "现价"
    COL_PCT = "涨跌幅"
    COL_AMOUNT = "涨跌额"
    COL_HIGH = "最高"
    COL_LOW = "最低"
    COL_QTY = "持仓量"
    COL_AVAIL = "可用"
    COL_COST = "成本价"
    COL_MVAL = "市值"
    COL_PNL = "盈亏"
    COL_PNLP = "盈亏比"
    COL_OPEN = "今开"
    COL_VOL = "成交量"
    COL_TURN = "成交额"
    COL_PE = "市盈率"
    COL_MCAP = "总市值"

    _COLUMN_LABELS: dict[str, str] = {
        " ": " ",
        "代码": "代码",
        "名称": "名称",
        "现价": "现价",
        "涨跌幅": "涨跌幅",
        "涨跌额": "涨跌额",
        "最高": "最高",
        "最低": "最低",
        "持仓量": "持仓量",
        "可用": "可用",
        "成本价": "成本价",
        "市值": "市值",
        "盈亏": "盈亏",
        "盈亏比": "盈亏比",
        "今开": "今开",
        "成交量": "成交量",
        "成交额": "成交额",
        "市盈率": "市盈率",
        "总市值": "总市值",
    }

    # Disguised labels when privacy mode is on
    _PRIVACY_LABELS: dict[str, str] = {
        " ": " ",
        "代码": "ID",
        "名称": "项目",
        "现价": "T1",
        "涨跌幅": "T2",
        "涨跌额": "T3",
        "最高": "T4",
        "最低": "T5",
        "持仓量": "T6",
        "可用": "T7",
        "成本价": "T8",
        "市值": "T9",
        "盈亏": "T10",
        "盈亏比": "T11",
        "今开": "T12",
        "成交量": "T13",
        "成交额": "T14",
        "市盈率": "T15",
        "总市值": "T16",
    }

    def __init__(self) -> None:
        super().__init__(cursor_type="row", zebra_stripes=False)
        # Raw values for sort: row_key → {col_key: float|str}
        self._raw: dict[str, dict[str, str | float | None]] = {}
        self._sort_col: str | None = None
        self._sort_reverse: bool = False
        # Codes that have active alerts
        self._alert_codes: set[str] = set()
        # Positions: code → Position
        self._positions: dict[str, "Position"] = {}
        # Privacy mode: row_key → 1-based disguise index
        self._privacy_index: dict[str, int] = {}
        # Cached quotes for row rebuild (privacy toggle, re-sort, etc.)
        self._quotes: dict[str, StockQuote] = {}

    def set_positions(self, positions: dict[str, "Position"]) -> None:
        self._positions = positions

    def watch_privacy_mode(self, value: bool) -> None:
        """Re-render all rows and column labels when privacy mode toggles."""
        # Switch column labels by matching current label text
        if value:
            src, dst = self._COLUMN_LABELS, self._PRIVACY_LABELS
        else:
            src, dst = self._PRIVACY_LABELS, self._COLUMN_LABELS
        for orig_key, src_label in src.items():
            self._update_column_label(src_label, dst[orig_key])

        # Build stable privacy index from current ordered_rows BEFORE re-rendering
        self._privacy_index = {}
        if value and self.row_count > 0:
            for i, row_key in enumerate(self.ordered_rows, start=1):
                self._privacy_index[str(row_key.key.value)] = i
        # Re-render every cached row so disguise applies to all data
        for quote in self._quotes.values():
            self.update_quote(quote)

    def on_mount(self) -> None:
        self.add_columns(
            self.COL_DIR,
            self.COL_CODE,
            self.COL_NAME,
            self.COL_PRICE,
            self.COL_PCT,
            self.COL_AMOUNT,
            self.COL_HIGH,
            self.COL_LOW,
            self.COL_QTY,
            self.COL_AVAIL,
            self.COL_COST,
            self.COL_MVAL,
            self.COL_PNL,
            self.COL_PNLP,
            self.COL_OPEN,
            self.COL_VOL,
            self.COL_TURN,
            self.COL_PE,
            self.COL_MCAP,
        )

    def update_quote(self, quote: StockQuote) -> None:
        """Insert or update a row for the given quote."""
        row_key = quote.code
        self._quotes[row_key] = quote  # cache for privacy toggle rebuild
        pos = self._positions.get(row_key)

        if self.privacy_mode:
            idx = self._privacy_index.get(row_key, self.row_count + 1)
            if row_key not in self._privacy_index:
                self._privacy_index[row_key] = idx
            pid = f"P{idx:03d}"
            iname = f"Item {idx:03d}"
            cells = [
                f" [bold {FLAT_COLOR}]→[/]",
                f"[bold]{pid}[/bold]",
                iname,
                "    —",
                "      —",
                "       —",
                "    —",
                "    —",
                "     —",
                "     —",
                "    —",
                "        —",
                "       —",
                "       —",
                "    —",
                "        —",
                "        —",
                "     —",
                "       —",
            ]
        else:
            cells = [
                self._fmt_dir(quote.direction, quote.code),
                f"[bold]{quote.code}[/bold]",
                quote.name or "—",
                self._fmt_price(quote.price),
                self._fmt_pct(quote.change_pct),
                self._fmt_amount(quote.change_amount),
                self._fmt_price(quote.high),
                self._fmt_price(quote.low),
                self._fmt_qty(pos),
                self._fmt_qty_avail(pos),
                self._fmt_price(pos.cost if pos and pos.is_valid else None),
                self._fmt_mval(pos, quote.price),
                self._fmt_pnl(pos, quote.price),
                self._fmt_pnlp(pos, quote.price),
                self._fmt_price(quote.open),
                self._fmt_volume(quote.volume),
                self._fmt_turnover(quote.turnover),
                self._fmt_pe(quote.pe),
                self._fmt_mcap(quote.market_cap),
            ]

        # Store raw values for sorting
        price = quote.price or 0.0
        self._raw[row_key] = {
            self.COL_DIR: quote.change_pct or 0.0,
            self.COL_CODE: quote.code,
            self.COL_NAME: quote.name or "",
            self.COL_PRICE: price,
            self.COL_PCT: quote.change_pct or 0.0,
            self.COL_AMOUNT: quote.change_amount or 0.0,
            self.COL_HIGH: quote.high or 0.0,
            self.COL_LOW: quote.low or 0.0,
            self.COL_QTY: pos.quantity if pos else 0,
            self.COL_AVAIL: pos.available if pos else 0,
            self.COL_COST: pos.cost if pos and pos.is_valid else 0.0,
            self.COL_MVAL: pos.market_value(price) if pos and pos.is_valid else 0.0,
            self.COL_PNL: pos.pnl(price) if pos and pos.is_valid else 0.0,
            self.COL_PNLP: pos.pnl_pct(price) if pos and pos.is_valid else 0.0,
            self.COL_OPEN: quote.open or 0.0,
            self.COL_VOL: quote.volume or 0.0,
            self.COL_TURN: quote.turnover or 0.0,
            self.COL_PE: quote.pe or 0.0,
            self.COL_MCAP: quote.market_cap or 0.0,
        }

        try:
            self.remove_row(row_key)
        except Exception:
            pass

        self.add_row(*cells, key=row_key)
        self._reapply_sort()

    def _reapply_sort(self) -> None:
        """Re-apply current sort order after row updates, if any."""
        if self._sort_col is not None:
            self.sort(
                self._sort_col,
                key=lambda row_key: self._raw.get(str(row_key), {}).get(
                    self._sort_col, 0
                ),
                reverse=self._sort_reverse,
            )

    def _toggle_sort(self, col_key: str) -> None:
        """Toggle sort on a column: asc → desc → unsort."""
        label_key = str(col_key)
        # Strip sort arrows from label_key for robustness
        label_key = label_key.rstrip(" ▲▼")
        # Restore all column labels from the active label set
        active_labels = self._PRIVACY_LABELS if self.privacy_mode else self._COLUMN_LABELS
        for col, label in active_labels.items():
            self._update_column_label(col, label)

        if self._sort_col == label_key:
            if self._sort_reverse:
                # Third click: unsort
                self._sort_col = None
                self._sort_reverse = False
                return
            else:
                # Second click: reverse
                self._sort_reverse = True
        else:
            # First click: ascending
            self._sort_col = label_key
            self._sort_reverse = False

        # Apply sort
        self.sort(
            self._sort_col,
            key=lambda row_key: self._raw.get(str(row_key), {}).get(
                self._sort_col, 0
            ),
            reverse=self._sort_reverse,
        )

        # Update sort indicator
        arrow = " ▼" if self._sort_reverse else " ▲"
        active_labels[label_key] = active_labels[label_key].rstrip(" ▲▼") + arrow
        self._update_column_label(label_key, active_labels[label_key])

    def _update_column_label(self, col_key: str, label: str) -> None:
        """Update a column header label in-place by matching current label text."""
        for column in self.columns.values():
            if str(column.label) == col_key:
                column.label = label
                return

    def remove_row_by_key(self, key: str) -> None:
        try:
            self.remove_row(key)
        except Exception:
            pass
        self._raw.pop(key, None)
        self._quotes.pop(key, None)

    def get_highlighted_key(self) -> Optional[str]:
        if self.row_count == 0:
            return None
        try:
            row = self.coordinate_to_cell_key(self.cursor_coordinate)
            return row.row_key.value if row else None
        except Exception:
            return None

    # -- formatters -------------------------------------------------------

    def _fmt_dir(self, direction: str, code: str = "") -> str:
        alert = "🔔" if code in self._alert_codes else " "
        if direction == "up":
            return f"{alert}[bold {UP_COLOR}]↑[/]"
        elif direction == "down":
            return f"{alert}[bold {DOWN_COLOR}]↓[/]"
        return f"{alert}[{FLAT_COLOR}]→[/]"

    @staticmethod
    def _fmt_price(val: Optional[float]) -> str:
        if val is None:
            return "     —"
        return f"{val:>8.2f}"

    @staticmethod
    def _fmt_pct(val: Optional[float]) -> str:
        if val is None:
            return "       —"
        if val == 0:
            return "   0.00%"
        sign = "+" if val > 0 else ""
        color = UP_COLOR if val > 0 else DOWN_COLOR
        return f"[bold {color}]{sign}{val:>6.2f}%[/]"

    @staticmethod
    def _fmt_amount(val: Optional[float]) -> str:
        if val is None:
            return "       —"
        if val == 0:
            return "    0.00"
        sign = "+" if val > 0 else ""
        color = UP_COLOR if val > 0 else DOWN_COLOR
        return f"[{color}]{sign}{val:>7.2f}[/]"

    # -- position formatters ------------------------------------------------

    @staticmethod
    def _fmt_qty(pos: Optional[Position]) -> str:
        if pos is None or not pos.is_valid:
            return "     —"
        return f"{pos.quantity:>6d}"

    @staticmethod
    def _fmt_qty_avail(pos: Optional[Position]) -> str:
        if pos is None or not pos.is_valid:
            return "     —"
        return f"{pos.available:>5d}"

    @staticmethod
    def _fmt_mval(pos: Optional[Position], price: Optional[float]) -> str:
        if pos is None or not pos.is_valid or price is None:
            return "        —"
        return f"{pos.market_value(price):>9.2f}"

    @staticmethod
    def _fmt_pnl(pos: Optional[Position], price: Optional[float]) -> str:
        if pos is None or not pos.is_valid or price is None:
            return "       —"
        v = pos.pnl(price)
        sign = "+" if v > 0 else ""
        color = UP_COLOR if v > 0 else DOWN_COLOR if v < 0 else FLAT_COLOR
        return f"[{color}]{sign}{v:>7.2f}[/]"

    @staticmethod
    def _fmt_pnlp(pos: Optional[Position], price: Optional[float]) -> str:
        if pos is None or not pos.is_valid or price is None:
            return "       —"
        v = pos.pnl_pct(price)
        sign = "+" if v > 0 else ""
        color = UP_COLOR if v > 0 else DOWN_COLOR if v < 0 else FLAT_COLOR
        return f"[{color}]{sign}{v:>6.2f}%[/]"

    @staticmethod
    def _fmt_volume(val: Optional[float]) -> str:
        if val is None:
            return "        —"
        # Format as 万/亿 for readability
        if val >= 1e8:
            return f"{val / 1e8:>7.2f}亿"
        if val >= 1e4:
            return f"{val / 1e4:>7.2f}万"
        return f"{val:>8.0f}"

    @staticmethod
    def _fmt_turnover(val: Optional[float]) -> str:
        if val is None:
            return "        —"
        return f"{val / 1e8:>7.2f}亿"

    @staticmethod
    def _fmt_pe(val: Optional[float]) -> str:
        if val is None:
            return "     —"
        return f"{val:>6.2f}"

    @staticmethod
    def _fmt_mcap(val: Optional[float]) -> str:
        if val is None:
            return "       —"
        return f"{val:>7.0f}亿"


# ---------------------------------------------------------------------------
# Detail screen
# ---------------------------------------------------------------------------


class DetailModal(ModalScreen[None]):
    """Modal popup showing extended info for a single stock."""

    def __init__(self, quote: StockQuote, positions: dict, alert_history: list[str],
                 alert_rules: list):
        super().__init__()
        self._quote = quote
        self._positions = positions
        self._alert_history = alert_history
        self._alert_rules = alert_rules

    def compose(self) -> ComposeResult:
        yield RichLog(id="detail_content", highlight=True, markup=True)

    def on_mount(self) -> None:
        self._render()

    def _render(self) -> None:
        q = self._quote
        log = self.query_one("#detail_content", RichLog)
        log.clear()

        pos = self._positions.get(q.code)
        price = q.price or 0.0
        pct_str = f"[bold red]+{q.change_pct:.2f}%[/]" if (q.change_pct or 0) > 0 else (
            f"[bold green]{q.change_pct:.2f}%[/]" if q.change_pct else "—"
        )

        log.write(f"\n  [bold yellow]{q.name or q.code}[/]  {q.code}")
        log.write(f"  {'─' * 50}")
        log.write(f"  当前价  [bold]{price:.2f}[/]  {pct_str}")
        log.write(f"  今开 {q.open or '—':>8}  最高 {q.high or '—':>8}  最低 {q.low or '—':>8}")
        log.write(f"  成交量  {self._fmt_vol(q.volume)}  成交额  {self._fmt_turn(q.turnover)}")

        # A-share order book
        if q.code.startswith(("sh", "sz")):
            log.write("\n  [bold]五档盘口[/]")
            log.write(f"  {'卖5':>4}  {self._fmt_ob(q.ask_prices, 4)}  {self._fmt_ob_vol(q.ask_volumes, 4)}")
            log.write(f"  {'卖4':>4}  {self._fmt_ob(q.ask_prices, 3)}  {self._fmt_ob_vol(q.ask_volumes, 3)}")
            log.write(f"  {'卖3':>4}  {self._fmt_ob(q.ask_prices, 2)}  {self._fmt_ob_vol(q.ask_volumes, 2)}")
            log.write(f"  {'卖2':>4}  {self._fmt_ob(q.ask_prices, 1)}  {self._fmt_ob_vol(q.ask_volumes, 1)}")
            log.write(f"  {'卖1':>4}  {self._fmt_ob(q.ask_prices, 0)}  {self._fmt_ob_vol(q.ask_volumes, 0)}")
            log.write(f"  {'─' * 30}")
            log.write(f"  {'买1':>4}  {self._fmt_ob(q.bid_prices, 0)}  {self._fmt_ob_vol(q.bid_volumes, 0)}")
            log.write(f"  {'买2':>4}  {self._fmt_ob(q.bid_prices, 1)}  {self._fmt_ob_vol(q.bid_volumes, 1)}")
            log.write(f"  {'买3':>4}  {self._fmt_ob(q.bid_prices, 2)}  {self._fmt_ob_vol(q.bid_volumes, 2)}")
            log.write(f"  {'买4':>4}  {self._fmt_ob(q.bid_prices, 3)}  {self._fmt_ob_vol(q.bid_volumes, 3)}")
            log.write(f"  {'买5':>4}  {self._fmt_ob(q.bid_prices, 4)}  {self._fmt_ob_vol(q.bid_volumes, 4)}")

        # HK: PE + market cap
        if q.code.startswith("hk"):
            log.write("\n  [bold]港股数据[/]")
            log.write(f"  市盈率  {q.pe or '—':>8}  总市值  {self._fmt_mcap_val(q.market_cap)}")

        # Position
        if pos and pos.is_valid:
            log.write("\n  [bold]持仓[/]")
            log.write(f"  持仓  {pos.quantity}  可用  {pos.available}  成本  {pos.cost:.2f}  市值  {pos.market_value(price):.2f}")
            pnl = pos.pnl(price)
            pnlp = pos.pnl_pct(price)
            sign = "+" if pnl > 0 else ""
            log.write(f"  盈亏  {sign}{pnl:.2f}  ({sign}{pnlp:.2f}%)")

        # Alerts for this stock
        stock_alerts = [a for a in self._alert_rules if a.code == q.code]
        if stock_alerts:
            log.write("\n  [bold]告警规则[/]")
            for a in stock_alerts:
                status = "已触发" if a.triggered else "待触发"
                log.write(f"  {a.alert_type.value:14s}  {a.value:>8.2f}  [{status}]")

        # Recent history
        if self._alert_history:
            log.write("\n  [bold]最近告警[/]")
            for line in self._alert_history[:5]:
                try:
                    e = json.loads(line)
                except Exception:
                    continue
                if e.get("code") != q.code:
                    continue
                log.write(f"  {e.get('ts', '')[-8:] or '??:??'}  {e.get('type', '')} → {e.get('price', '—')}")

        log.write("\n  [dim]按 Enter/Esc 关闭[/dim]")

    @staticmethod
    def _fmt_vol(val: Optional[float]) -> str:
        if val is None:
            return "—"
        if val >= 1e8:
            return f"{val / 1e8:.2f}亿"
        if val >= 1e4:
            return f"{val / 1e4:.2f}万"
        return f"{val:.0f}"

    @staticmethod
    def _fmt_turn(val: Optional[float]) -> str:
        if val is None:
            return "—"
        return f"{val / 1e8:.2f}亿"

    @staticmethod
    def _fmt_mcap_val(val: Optional[float]) -> str:
        if val is None:
            return "—"
        return f"{val:.0f}亿"

    @staticmethod
    def _fmt_ob(prices: list, idx: int) -> str:
        if idx < len(prices):
            return f"{prices[idx]:>8.2f}"
        return "       —"

    @staticmethod
    def _fmt_ob_vol(vols: list, idx: int) -> str:
        if idx < len(vols):
            v = vols[idx]
            return f"{v:>6.0f}"
        return "     —"

    def on_key(self, event) -> None:
        if event.key in ("enter", "escape"):
            self.dismiss()


class SettingsModal(ModalScreen[None]):
    """Settings dialog with editable fields."""

    def __init__(self, cfg: "AppConfig", config_path: Path):
        super().__init__()
        self._cfg = cfg
        self._config_path = config_path
        self._inputs: dict[str, Input] = {}

    def compose(self) -> ComposeResult:
        yield RichLog(id="settings_log", highlight=True, markup=True)

    def on_mount(self) -> None:
        log = self.query_one("#settings_log", RichLog)
        log.clear()
        log.write("\n  [bold yellow]设置[/]  (编辑 config.yaml 后自动生效)")
        log.write(f"  {'─' * 40}")
        log.write(f"  轮询间隔(秒)       {self._cfg.poll_interval}")
        log.write(f"  请求超时(秒)       {self._cfg.request.timeout}")
        log.write(f"  退避基数(秒)       {self._cfg.backoff.base}")
        log.write(f"  退避上限(秒)       {self._cfg.backoff.max_delay}")
        log.write(f"  退避乘数           {self._cfg.backoff.multiplier}")
        log.write(f"  告警音效命令       {self._cfg.alert_sound_command or '(空)'}")
        log.write("\n  [dim]按 Esc 关闭，编辑 config.yaml 后自动热加载[/dim]")

    def on_key(self, event) -> None:
        if event.key in ("enter", "escape"):
            self.dismiss()


# ---------------------------------------------------------------------------
# Main application
# ---------------------------------------------------------------------------


class StockWatcherApp(App):
    """Real-time stock price monitor for A-share and HK markets."""

    CSS = """
    Screen {
        background: #0d1117;
    }

    StatusBar {
        dock: top;
        height: 1;
        background: #161b22;
        color: #c9d1d9;
        padding: 0 1;
    }

    PortfolioBar {
        dock: bottom;
        height: 1;
        background: #161b22;
        color: #c9d1d9;
        padding: 0 1;
    }

    StockTable {
        height: 1fr;
        border: solid #30363d;
        background: #0d1117;
    }

    StockTable > .datatable--header {
        background: #161b22;
        color: #8b949e;
        text-style: bold;
    }

    StockTable > .datatable--cursor {
        background: #1f3a5f;
        color: #e6edf3;
    }

    Footer {
        background: #161b22;
        color: #8b949e;
    }

    Footer > .footer--key {
        background: #21262d;
        color: #e3b341;
        text-style: bold;
    }

    Footer > .footer--description {
        color: #c9d1d9;
    }

    #prompt_container {
        dock: bottom;
        height: 3;
        background: #161b22;
        padding: 0 1;
        border-top: solid #30363d;
        visibility: hidden;
    }

    #prompt_container.visible {
        visibility: visible;
    }

    #prompt_label {
        height: 1;
        color: #c9d1d9;
    }

    #prompt_input {
        height: 1;
        margin: 0 0 1 0;
    }

    #search_list {
        dock: bottom;
        height: auto;
        max-height: 12;
        background: #161b22;
        border: solid #58a6ff;
        visibility: hidden;
    }

    #search_list.visible {
        visibility: visible;
    }

    #search_list > .listview--item {
        color: #c9d1d9;
    }

    #search_list > .listview--item.highlight {
        background: #1f3a5f;
    }
    """

    BINDINGS = [
        Binding("a", "add_stock", "添加"),
        Binding("d", "delete_stock", "删除"),
        Binding("t", "set_alert", "告警"),
        Binding("p", "set_position", "持仓"),
        Binding("v", "view_alerts", "告警列表"),
        Binding("h", "alert_history", "告警历史"),
        Binding("s", "settings", "设置"),
        Binding("e", "export_csv", "导出CSV"),
        Binding("r", "manual_refresh", "刷新"),
        Binding("x", "toggle_privacy", "隐私"),
        Binding("ctrl+n", "reload_config", "热加载"),
        Binding("q", "quit", "退出"),
        Binding("escape", "cancel_prompt", "取消", show=False),
        Binding("enter", "show_detail", "详情", show=False),
    ]

    config_path: Path = Path("config.yaml")
    alert_history_path: Path = Path("alert_history.jsonl")

    def __init__(self, config_path: Optional[Path] = None):
        super().__init__()
        if config_path:
            self.config_path = Path(config_path)

    def compose(self) -> ComposeResult:
        yield StatusBar()
        yield StockTable()
        yield PortfolioBar()
        yield Footer()
        yield Vertical(
            Label("输入股票代码或名称（如 sh000001 / 腾讯），回车确认：", id="prompt_label"),
            Input(id="prompt_input", placeholder="sh000001"),
            id="prompt_container",
        )
        yield ListView(id="search_list")

    def on_mount(self) -> None:
        try:
            self._cfg = load_config(self.config_path)
            self._config_mtime = os.path.getmtime(self.config_path)
        except FileNotFoundError:
            self.notify(f"配置文件未找到: {self.config_path}", severity="error")
            self.exit()
            return

        self._positions_mtime = 0.0
        pos_path = self.config_path.with_name("positions.yaml")
        try:
            self._positions_mtime = os.path.getmtime(pos_path)
        except OSError:
            pass

        self._calendar = TradingCalendar()
        self._queue = PollQueue(list(self._cfg.watchlist))
        self._backoff = MarketBackoffManager(self._cfg.backoff)
        self._request_cfg = self._cfg.request
        self._poll_interval = self._cfg.poll_interval

        self._table: StockTable = self.query_one(StockTable)
        self._status_bar: StatusBar = self.query_one(StatusBar)
        self._prompt_container: Vertical = self.query_one("#prompt_container")
        self._prompt_input: Input = self.query_one("#prompt_input")
        self._prompt_label: Label = self.query_one("#prompt_label")
        self._search_list: ListView = self.query_one("#search_list")

        # Holds the latest quote for every stock (code → StockQuote)
        self._latest_quotes: dict[str, StockQuote] = {}

        # Alert rules loaded from config
        self._alerts: list[AlertRule] = list(self._cfg.alerts)
        self._positions: dict[str, Position] = dict(self._cfg.positions)
        self._chat_cfg = self._cfg.chat
        self._deepseek_cfg = self._cfg.deepseek
        self._update_alert_codes()
        self._table.set_positions(self._positions)

        self._prompt_mode: Optional[str] = None
        self._alert_target: Optional[str] = None  # stock code for alert setup
        self._alert_viewing: bool = False
        self._alert_list_items: list[ListItem] = []
        self._search_results: list[SearchResult] = []
        self._stopped = False
        self._privacy_mode: bool = False

        self._poll_loop()
        self._daily_summary_loop()
        self._bot_loop()

    # ------------------------------------------------------------------
    # Bot server — background worker entry point
    # ------------------------------------------------------------------

    async def _bot_loop(self) -> None:
        """Background worker that polls Feishu for @mentions & replies via DeepSeek."""
        if not self._chat_cfg or not self._chat_cfg.can_receive:
            return

        from stock_watcher.bot_server import run_bot_server
        from stock_watcher.chat_sender import _get_hkd_cny_rate

        self._bot_stop = asyncio.Event()

        def _get_quotes():
            return dict(self._latest_quotes)

        def _get_positions():
            return dict(self._positions)

        async def _get_rate():
            return await _get_hkd_cny_rate()

        def _notify(msg: str):
            self.call_from_thread(self.notify, msg)

        await run_bot_server(
            self._chat_cfg,
            self._deepseek_cfg,
            _get_quotes,
            _get_positions,
            _get_rate,
            _notify,
            self._bot_stop,
        )

    # ------------------------------------------------------------------
    # Column sort
    # ------------------------------------------------------------------

    def on_data_table_header_selected(self, event: DataTable.HeaderSelected) -> None:
        """Handle click on a column header → toggle sort."""
        if event.data_table is self._table:
            col = event.column_key.value
            if col is not None:
                self._table._toggle_sort(col)
        event.stop()

    # ------------------------------------------------------------------
    # Detail panel
    # ------------------------------------------------------------------

    def action_toggle_privacy(self) -> None:
        """Toggle privacy mode — disguise all stock data as generic items."""
        self._privacy_mode = not self._privacy_mode
        self._table.privacy_mode = self._privacy_mode
        self._update_status()
        mode_text = "🔒 隐私模式 开" if self._privacy_mode else "🔓 隐私模式 关"
        self.notify(mode_text, severity="information")

    def action_show_detail(self) -> None:
        """Show detail modal for the highlighted stock."""
        if self._privacy_mode:
            return  # blocked in privacy mode
        key = self._table.get_highlighted_key()
        if key is None:
            return
        quote = self._latest_quotes.get(key)
        if quote is None:
            self.notify("暂无该股票行情数据", severity="warning")
            return

        history_lines = self._read_alert_history(last=50)
        self.push_screen(DetailModal(quote, self._positions, history_lines, self._alerts))

    def action_settings(self) -> None:
        """Show settings readout."""
        self.push_screen(SettingsModal(self._cfg, self.config_path))

    def action_export_csv(self) -> None:
        """Export current table data to CSV."""
        import csv
        now = dt.datetime.now().strftime("%Y%m%d_%H%M%S")
        path = Path(f"export_{now}.csv")
        try:
            with open(path, "w", newline="", encoding="utf-8-sig") as f:
                writer = csv.writer(f)
                writer.writerow(["代码", "名称", "现价", "涨跌幅", "涨跌额", "今开", "最高", "最低",
                                 "成交量", "成交额", "市盈率", "总市值"])
                for code in self._queue.all_codes:
                    q = self._latest_quotes.get(code)
                    if q is None:
                        continue
                    writer.writerow([
                        q.code, q.name or "", q.price or "", q.change_pct or "",
                        q.change_amount or "", q.open or "", q.high or "", q.low or "",
                        q.volume or "", q.turnover or "", q.pe or "", q.market_cap or "",
                    ])
            self.notify(f"已导出: {path}")
        except OSError as e:
            self.notify(f"导出失败: {e}", severity="error")

    # ------------------------------------------------------------------
    # Key binding gate
    # ------------------------------------------------------------------

    def check_action(self, action: str, parameters: tuple[object, ...]) -> bool | None:
        if self._alert_viewing:
            if action in ("delete_alert", "cancel_prompt", "view_alerts", "show_detail"):
                return True
            return False
        if self._prompt_mode is not None or self._search_list.has_class("visible"):
            if action in ("add_stock", "delete_stock", "set_alert", "set_position",
                          "manual_refresh", "quit"):
                return False
        return True

    # ------------------------------------------------------------------
    # Polling
    # ------------------------------------------------------------------

    @work(exclusive=False)
    async def _poll_loop(self) -> None:
        while not self._stopped:
            # Check for config file changes
            self._reload_config_if_changed()

            code = self._queue.next()

            if code is None:
                await asyncio.sleep(1)
                continue

            if not self._calendar.is_trading(code):
                await asyncio.sleep(0.1)
                continue

            backoff = self._backoff.get(code)
            if backoff.is_backed_off:
                delay = backoff.get_delay()
                self._update_status()
                await asyncio.sleep(delay)

            await self._fetch_one(code)
            await asyncio.sleep(self._poll_interval)

    async def _fetch_one(self, code: str) -> None:
        t0 = time.monotonic()
        backoff = self._backoff.get(code)
        try:
            fetcher = get_fetcher(code, self._request_cfg)
            quote = await fetcher.fetch(code)
        except FetchError as e:
            if e.is_retryable:
                backoff.backoff()
            self._status_bar.errors = backoff.consecutive_failures
            self._update_status()
            return

        elapsed_ms = (time.monotonic() - t0) * 1000
        backoff.reset()
        self._status_bar.errors = 0
        self._status_bar.latency = f"{elapsed_ms:.0f}ms"

        if quote.is_valid:
            self._latest_quotes[code] = quote
            self._table.update_quote(quote)
            # Check alerts
            self._check_alerts(quote)
        else:
            cells = ["[#555555]→[/]", code, "—", "     —", "       —", "       —", "     —", "     —"]
            try:
                self._table.remove_row(code)
            except Exception:
                pass
            self._table.add_row(*cells, key=code)

        self._update_status()

    # ------------------------------------------------------------------
    # Daily summary email
    # ------------------------------------------------------------------

    @work(exclusive=False)
    async def _daily_summary_loop(self) -> None:
        """Push summaries on a schedule: every 3 min during trading, every 1 hr closed."""
        last_push: float = 0.0  # monotonic timestamp of last push

        while not self._stopped:
            await asyncio.sleep(30)

            if not self._latest_quotes:
                continue

            now = _now_cst()
            if not _is_weekday(now):
                continue

            # Determine push interval based on market status
            any_open = is_any_market_open(now)
            interval = 180 if any_open else 3600  # 3 min vs 1 hour

            elapsed = time.monotonic() - last_push
            if elapsed < interval:
                continue

            # Push summary via Feishu
            if self._chat_cfg and self._chat_cfg.is_configured:
                from stock_watcher.chat_sender import build_summary_card, send_feishu_card

                card = await build_summary_card(self._latest_quotes, self._positions)
                ok = await send_feishu_card(self._chat_cfg, card)
                if ok:
                    last_push = time.monotonic()

    def _update_status(self) -> None:
        sb = self._status_bar
        sb.status = self._calendar.status_string()
        if self._backoff.any_backed_off:
            sb.status += f" 退避{self._backoff.max_delay:.0f}s"

        if self._privacy_mode:
            sb.status = "工作中"
            sb.total = 0
            sb.up_count = 0
            sb.down_count = 0
            sb.flat_count = 0
            # Also hide portfolio data
            try:
                pb = self.query_one(PortfolioBar)
                pb.total_mval = 0.0
                pb.total_pnl = 0.0
                pb.total_pnlp = 0.0
                pb.pos_count = 0
            except Exception:
                pass
            return

        # Stats
        quotes = self._latest_quotes.values()
        sb.total = len(self._queue.all_codes)
        sb.up_count = sum(1 for q in quotes if q.direction == "up")
        sb.down_count = sum(1 for q in quotes if q.direction == "down")
        sb.flat_count = sb.total - sb.up_count - sb.down_count

        # Portfolio stats
        try:
            pb = self.query_one(PortfolioBar)
        except Exception:
            pb = None
        if pb is not None:
            total_mval = 0.0
            total_pnl = 0.0
            total_cost = 0.0
            pos_count = 0
            for code, pos in self._positions.items():
                if not pos.is_valid:
                    continue
                q = self._latest_quotes.get(code)
                price = q.price if (q and q.price) else None
                if price is None:
                    continue
                pos_count += 1
                total_mval += pos.market_value(price)
                total_pnl += pos.pnl(price)
                total_cost += pos.cost * pos.quantity
            pb.total_mval = total_mval
            pb.total_pnl = total_pnl
            pb.total_pnlp = (total_pnl / total_cost * 100) if total_cost > 0 else 0.0
            pb.pos_count = pos_count

    # ------------------------------------------------------------------
    # Config hot-reload
    # ------------------------------------------------------------------

    def _reload_config_if_changed(self) -> None:
        """Reload config.yaml and positions.yaml if modified externally."""
        try:
            cfg_mtime = os.path.getmtime(self.config_path)
        except OSError:
            cfg_mtime = 0.0

        pos_path = self.config_path.with_name("positions.yaml")
        try:
            pos_mtime = os.path.getmtime(pos_path)
        except OSError:
            pos_mtime = 0.0

        if cfg_mtime <= self._config_mtime and pos_mtime <= self._positions_mtime:
            return

        self._config_mtime = cfg_mtime
        self._positions_mtime = pos_mtime

        try:
            new_cfg = load_config(self.config_path)
        except Exception:
            self.notify("配置文件变更但解析失败，保留当前配置", severity="warning")
            return

        # Sync watchlist: add new codes, remove deleted codes
        new_watchlist = set(new_cfg.watchlist)
        old_watchlist = set(self._queue.all_codes)

        added = new_watchlist - old_watchlist
        removed = old_watchlist - new_watchlist

        for code in sorted(removed):
            self._queue.remove(code)
            self._table.remove_row_by_key(code)
            self._latest_quotes.pop(code, None)
            self._alerts = [a for a in self._alerts if a.code != code]

        for code in sorted(added):
            if Market.is_valid_code(code):
                self._queue.add(code)
                asyncio.create_task(self._fetch_one(code))

        # Sync alerts
        self._alerts = list(new_cfg.alerts)
        self._update_alert_codes()

        # Sync positions (from positions.yaml, loaded by load_config)
        old_positions = dict(self._positions)
        self._positions = dict(new_cfg.positions)
        self._table.set_positions(self._positions)

        if self._positions != old_positions:
            self._update_status()  # refresh portfolio bar immediately

        # Sync poll interval and backoff
        self._poll_interval = new_cfg.poll_interval
        self._backoff = MarketBackoffManager(new_cfg.backoff)
        self._cfg = new_cfg  # keep settings panel in sync
        self._chat_cfg = new_cfg.chat

        if added or removed:
            names = []
            if added:
                names.append(f"+{len(added)}只")
            if removed:
                names.append(f"-{len(removed)}只")
            self.notify(f"配置文件已更新（{'，'.join(names)}）")

        self._update_status()

    # ------------------------------------------------------------------
    # Config reload
    # ------------------------------------------------------------------

    def action_reload_config(self) -> None:
        """Force reload config.yaml, bypassing mtime check."""
        self._config_mtime = 0.0  # force _reload_config_if_changed to re-read
        self._reload_config_if_changed()

    # ------------------------------------------------------------------
    # Manual refresh
    # ------------------------------------------------------------------

    @work(exclusive=False)
    async def action_manual_refresh(self) -> None:
        self._status_bar.status = "手动刷新..."
        for code in self._queue.all_codes:
            await self._fetch_one(code)
            await asyncio.sleep(1)
        self._update_status()

    # ------------------------------------------------------------------
    # Add stock
    # ------------------------------------------------------------------

    def action_add_stock(self) -> None:
        if self._search_list.has_class("visible"):
            self._hide_search_list()
            return

        self._prompt_mode = "add"
        self._prompt_container.add_class("visible")
        self._prompt_input.value = ""
        self._prompt_input.focus()

    async def _on_add_submit(self, raw: str) -> None:
        text = raw.strip().lower()
        if not text:
            return
        if self._search_list.has_class("visible"):
            return

        if Market.is_valid_code(text):
            self._add_stock_by_code(text)
            self.action_cancel_prompt()
            return

        await self._search_and_show(text)

    def _add_stock_by_code(self, code: str) -> None:
        if self._queue.has(code):
            self.notify(f"已在监控列表中: {code}", severity="warning")
            return

        self._queue.add(code)
        save_watchlist(self.config_path, self._queue.all_codes)
        asyncio.create_task(self._fetch_one(code))
        self.notify(f"已添加: {code}")
        self._update_status()

    async def _search_and_show(self, query: str) -> None:
        self._prompt_container.remove_class("visible")

        results = await search_stocks(query, self._request_cfg)

        if not results:
            self.notify(f"未找到与 '{query}' 相关的股票", severity="warning")
            self.action_cancel_prompt()
            return

        if len(results) == 1:
            r = results[0]
            self.notify(f"找到: {r.name} ({r.code})，正在添加...")
            self._add_stock_by_code(r.code)
            self.action_cancel_prompt()
            return

        self._search_results = results
        self._search_list.clear()
        for r in results:
            self._search_list.append(
                ListItem(Label(f"  {r.code}  │  {r.name}  │  {r.market.upper()}"))
            )
        self._search_list.add_class("visible")
        self._search_list.focus()

    def on_list_view_selected(self, event: ListView.Selected) -> None:
        if self._alert_viewing:
            return  # No auto-add in alert view mode
        if not self._search_list.has_class("visible"):
            return

        idx = self._search_list.index
        if idx is not None and 0 <= idx < len(self._search_results):
            self._add_stock_by_code(self._search_results[idx].code)

        self._hide_search_list()
        self.action_cancel_prompt()

    def _hide_search_list(self) -> None:
        self._search_list.remove_class("visible")
        self._search_list.clear()
        self._search_results = []

    # ------------------------------------------------------------------
    # Delete stock
    # ------------------------------------------------------------------

    def action_delete_stock(self) -> None:
        key = self._table.get_highlighted_key()
        if key is None:
            self.notify("没有选中的股票", severity="warning")
            return

        self._table.remove_row_by_key(key)
        self._queue.remove(key)
        self._latest_quotes.pop(key, None)
        # Also remove alerts for this stock
        self._alerts = [a for a in self._alerts if a.code != key]
        self._positions.pop(key, None)
        save_watchlist(self.config_path, self._queue.all_codes)
        save_alerts(self.config_path, self._alerts)
        save_positions(self.config_path, self._positions)
        self._update_alert_codes()
        self.notify(f"已删除: {key}")
        self._update_status()

    # ------------------------------------------------------------------
    # Position
    # ------------------------------------------------------------------

    def action_set_position(self) -> None:
        """Set cost and quantity for the highlighted stock."""
        key = self._table.get_highlighted_key()
        if key is None:
            self.notify("没有选中的股票", severity="warning")
            return

        existing = self._positions.get(key)
        hint = ""
        if existing and existing.is_valid:
            hint = f"（当前: 成本{existing.cost} 数量{existing.quantity}）"

        self._prompt_mode = "position"
        self._alert_target = key
        self._prompt_container.add_class("visible")
        self._prompt_input.value = ""
        self._prompt_label.update(
            f"为 [bold]{key}[/bold] 设置持仓 {hint}\n"
            f"格式: [bold]成本 数量[/bold]  例: [bold]420 200[/bold]（买入均价420元，持仓200股）\n"
            f"留空并回车可删除持仓"
        )
        self._prompt_input.focus()

    async def _on_position_submit(self, raw: str) -> None:
        """Parse and save position from user input."""
        code = self._alert_target
        if not code:
            return

        raw = raw.strip()
        if not raw:
            # Delete position
            self._positions.pop(code, None)
            save_positions(self.config_path, self._positions)
            self._table.set_positions(self._positions)
            self.notify(f"已删除 {code} 的持仓")
            return

        parts = raw.split()
        if len(parts) < 2:
            self.notify("格式: 成本 数量（如 420 200）", severity="error")
            return

        try:
            cost = float(parts[0])
            qty = int(parts[1])
        except ValueError:
            self.notify("成本和数量必须是数字", severity="error")
            return

        if cost <= 0 or qty <= 0:
            self.notify("成本和数量必须大于0", severity="error")
            return

        self._positions[code] = Position(cost=cost, quantity=qty)
        save_positions(self.config_path, self._positions)
        self._table.set_positions(self._positions)
        self.notify(f"已设置 {code} 持仓: 成本{cost} 数量{qty}")

    # ------------------------------------------------------------------
    # Alerts
    # ------------------------------------------------------------------

    def action_set_alert(self) -> None:
        """Set a price or change-% alert for the highlighted stock."""
        key = self._table.get_highlighted_key()
        if key is None:
            self.notify("没有选中的股票", severity="warning")
            return

        self._prompt_mode = "alert"
        self._alert_target = key
        self._prompt_container.add_class("visible")
        self._prompt_input.value = ""
        self._prompt_label.update(
            f"为 [bold]{key}[/bold] 设置告警。格式: [bold]类型 数值[/bold]"
            f"  类型: [bold]pa[/bold]=价格上破 [bold]pb[/bold]=价格下破 [bold]ca[/bold]=涨幅超 [bold]cb[/bold]=跌幅超"
            f"  例: [bold]pa 450[/bold] 表示价格突破450时告警"
        )
        self._prompt_input.focus()

    def action_view_alerts(self) -> None:
        """Show a popup listing all current alert rules."""
        items: list[ListItem] = []
        for alert in self._alerts:
            quote = self._latest_quotes.get(alert.code)
            name = quote.name if quote else alert.code
            status = "已触发" if alert.triggered else "待触发"
            label = f"{alert.code:12s} {name:10s}  {alert.alert_type.value:14s}  {alert.value:>8.2f}  [{status}]"
            items.append(ListItem(Label(label)))
        if not items:
            self.notify("暂无告警规则", severity="information")
            return

        self._alert_list_items = items
        self._alert_viewing = True
        self._search_list.clear()
        self._search_list.extend(items)
        self._search_list.add_class("visible")
        self.notify("按 d 删除选中告警，Esc 关闭", timeout=3)

    def action_delete_alert(self) -> None:
        """Delete the alert selected in the alert view popup."""
        if not self._alert_viewing:
            return
        try:
            idx = self._search_list.index
            if idx is not None and 0 <= idx < len(self._alerts):
                removed = self._alerts.pop(idx)
                self.notify(f"已删除告警: {removed.code} {removed.describe()}")
                save_alerts(self.config_path, self._alerts)
                self._update_alert_codes()
                # Refresh the list
                self._alert_viewing = False
                self.action_view_alerts()
        except Exception:
            pass

    def _update_alert_codes(self) -> None:
        """Sync the set of codes-with-alerts to the table."""
        codes = {a.code for a in self._alerts}
        self._table._alert_codes = codes

    def _check_alerts(self, quote: StockQuote) -> None:
        """Check all alert rules against a new quote and fire if triggered."""
        for alert in self._alerts:
            if alert.code != quote.code:
                continue

            if alert.triggered:
                # Check if condition has cleared (for re-arm)
                if not alert.check(quote):
                    alert.triggered = False
                continue

            if alert.check(quote):
                alert.triggered = True
                self._fire_alert(alert, quote)

    # ------------------------------------------------------------------
    # Alert history
    # ------------------------------------------------------------------

    def _log_alert_history(self, alert: AlertRule, quote: StockQuote) -> None:
        """Append an alert firing to the persistent JSONL log."""
        entry = {
            "ts": dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).isoformat(),
            "code": alert.code,
            "name": quote.name or "",
            "type": alert.alert_type.value,
            "threshold": alert.value,
            "price": quote.price,
            "change_pct": quote.change_pct,
        }
        try:
            with open(self.alert_history_path, "a", encoding="utf-8") as f:
                f.write(json.dumps(entry, ensure_ascii=False) + "\n")
        except OSError:
            pass
        self._rotate_alert_history()

    def _rotate_alert_history(self, max_lines: int = 10000, keep: int = 5000) -> None:
        """Keep alert_history.jsonl bounded by rotating when it exceeds max_lines."""
        try:
            path = self.alert_history_path
            if not path.exists():
                return
            lines = path.read_text(encoding="utf-8").rstrip("\n").split("\n")
            if len(lines) > max_lines:
                path.write_text("\n".join(lines[-keep:]) + "\n", encoding="utf-8")
        except OSError:
            pass

    def _read_alert_history(self, last: int = 200) -> list[str]:
        """Read the most recent alert history lines (newest first)."""
        try:
            path = self.alert_history_path
            if not path.exists():
                return []
            lines = path.read_text(encoding="utf-8").rstrip("\n").split("\n")
            return lines[-last:][::-1]  # newest first
        except OSError:
            return []

    def action_alert_history(self) -> None:
        """Show recent alert history in a popup."""
        lines = self._read_alert_history()
        if not lines:
            self.notify("暂无告警历史", severity="information")
            return

        items: list[ListItem] = []
        for line in lines:
            try:
                e = json.loads(line)
            except json.JSONDecodeError:
                continue
            price_str = f"{e['price']:.2f}" if e.get("price") else "—"
            pct_str = f"{e['change_pct']:+.2f}%" if e.get("change_pct") is not None else ""
            label = (
                f"{e.get('ts', '')[-8:] or '??:??:??'}  "
                f"{e.get('code', '?'):12s}  "
                f"{e.get('type', '?'):14s}  "
                f"{e.get('threshold', 0):>8.2f}  →  "
                f"{price_str}  {pct_str}"
            )
            items.append(ListItem(Label(label)))

        self._alert_viewing = True
        self._search_list.clear()
        self._search_list.extend(items)
        self._search_list.add_class("visible")
        self.notify("告警历史（最新200条），Esc 关闭", timeout=3)

    def _fire_alert(self, alert: AlertRule, quote: StockQuote) -> None:
        """Fire an alert: TUI notification + system notification + sound + email."""
        msg = f"🚨 {quote.name or alert.code} {alert.describe()}！现价 {quote.price:.2f}"
        self.notify(msg, severity="warning", timeout=10)

        # System notification
        try:
            subprocess.run(
                ["notify-send", "📈 Stock Watcher 告警", msg],
                timeout=2,
                capture_output=True,
            )
        except Exception:
            pass

        # Terminal bell
        try:
            sys.stdout.write("\a")
            sys.stdout.flush()
        except Exception:
            pass

        # OSC escape sequences
        try:
            sys.stdout.write(f"\x1b]9;{msg}\x07")
            sys.stdout.write(f"\x1b]99;;{msg}\x07")
            sys.stdout.flush()
        except Exception:
            pass

        # Custom sound command
        cmd = self._cfg.alert_sound_command.strip()
        if cmd:
            try:
                subprocess.Popen(
                    shlex.split(cmd),
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
            except Exception:
                pass

        # Feishu webhook notification (non-blocking)
        if self._chat_cfg and self._chat_cfg.is_configured:
            from stock_watcher.chat_sender import build_alert_card, send_feishu_card

            card = build_alert_card(
                alert.code,
                quote.name or alert.code,
                alert.describe(),
                quote.price or 0,
                quote.change_pct,
            )
            asyncio.create_task(send_feishu_card(self._chat_cfg, card))

        # Log to alert history
        self._log_alert_history(alert, quote)

    async def _on_alert_submit(self, raw: str) -> None:
        """Parse alert specification from user input."""
        parts = raw.strip().lower().split()
        if len(parts) < 2:
            self.notify("格式错误。例: pa 450 或 ca 5", severity="error")
            return

        type_map = {
            "pa": AlertType.PRICE_ABOVE,
            "pb": AlertType.PRICE_BELOW,
            "ca": AlertType.PCT_ABOVE,
            "cb": AlertType.PCT_BELOW,
        }

        alert_type = type_map.get(parts[0])
        if alert_type is None:
            self.notify(f"未知告警类型: {parts[0]}。可用: pa/pb/ca/cb", severity="error")
            return

        try:
            value = float(parts[1])
        except ValueError:
            self.notify(f"数值无效: {parts[1]}", severity="error")
            return

        code = self._alert_target
        # Remove any existing alert of same type for same stock
        self._alerts = [a for a in self._alerts if not (a.code == code and a.alert_type == alert_type)]
        self._alerts.append(AlertRule(code=code, alert_type=alert_type, value=value))
        save_alerts(self.config_path, self._alerts)
        self._update_alert_codes()

        desc = self._alerts[-1].describe()
        self.notify(f"已为 {code} 设置告警: {desc}")

    # ------------------------------------------------------------------
    # Prompt handling
    # ------------------------------------------------------------------

    def action_cancel_prompt(self) -> None:
        # Close alert view if open
        if self._alert_viewing:
            self._alert_viewing = False
            self._hide_search_list()
            self.set_focus(None)
            return

        self._prompt_mode = None
        self._alert_target = None
        self._prompt_container.remove_class("visible")
        self._hide_search_list()
        # Restore default prompt label
        self._prompt_label.update(
            "输入股票代码或名称（如 sh000001 / 腾讯），回车确认："
        )
        self.set_focus(None)

    def on_input_submitted(self, event: Input.Submitted) -> None:
        if self._prompt_mode == "add":
            asyncio.create_task(self._on_add_submit(event.value))
        elif self._prompt_mode == "alert":
            asyncio.create_task(self._on_alert_submit(event.value))
            self.action_cancel_prompt()
        elif self._prompt_mode == "position":
            asyncio.create_task(self._on_position_submit(event.value))
            self.action_cancel_prompt()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


def main(config_path: Optional[str] = None) -> None:
    path = Path(config_path) if config_path else Path("config.yaml")
    if not path.is_absolute():
        path = Path.cwd() / path
    app = StockWatcherApp(config_path=path)
    app.run()


if __name__ == "__main__":
    import sys
    config_arg = sys.argv[1] if len(sys.argv) > 1 else None
    main(config_arg)
