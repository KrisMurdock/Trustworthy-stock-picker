"""Feishu (Lark) custom bot webhook notification sender."""

from __future__ import annotations

import datetime as dt
import hashlib
import hmac
import time
from typing import TYPE_CHECKING

import httpx

from stock_watcher.config import ChatConfig
from stock_watcher.models import StockQuote

if TYPE_CHECKING:
    from stock_watcher.models import Position


FEISHU_WEBHOOK_TIMEOUT = 10  # seconds

# ---------------------------------------------------------------------------
# HKD → CNY exchange rate (cached per session)
# ---------------------------------------------------------------------------

_hkd_cny_rate: float | None = None


async def _get_hkd_cny_rate() -> float:
    """Fetch current HKD/CNY rate from a free API, cached per process."""
    global _hkd_cny_rate
    if _hkd_cny_rate is not None:
        return _hkd_cny_rate
    try:
        async with httpx.AsyncClient(timeout=5) as c:
            r = await c.get("https://open.er-api.com/v6/latest/HKD")
            data = r.json()
            _hkd_cny_rate = float(data["rates"].get("CNY", 0.92))
    except Exception:
        _hkd_cny_rate = 0.92  # fallback
    return _hkd_cny_rate


def _is_hk(code: str) -> bool:
    return code.startswith("hk")


def _generate_sign(timestamp_seconds: int, secret: str) -> str:
    """Generate HMAC-SHA256 signature for Feishu webhook verification."""
    msg = f"{timestamp_seconds}\n{secret}"
    h = hmac.new(secret.encode("utf-8"), msg.encode("utf-8"), hashlib.sha256)
    return h.hexdigest()


def _build_card_header(title: str, color: str = "red") -> dict:
    return {"title": {"tag": "plain_text", "content": title}, "template": color}


def _color_pct(pct: float | None) -> str:
    if pct is None:
        return "—"
    color = "red" if pct >= 0 else "green"
    return f"<font color='{color}'>{pct:+.2f}%</font>"


def _color_pnl(pnl: float) -> str:
    color = "red" if pnl >= 0 else "green"
    return f"<font color='{color}'>{pnl:+.0f}</font>"


# ---------------------------------------------------------------------------
# column_set table helpers — proper Feishu card rendering
# ---------------------------------------------------------------------------

def _cell(width: str, md: str) -> dict:
    """Single column cell."""
    return {
        "tag": "column",
        "width": width,
        "elements": [{"tag": "div", "text": {"tag": "lark_md", "content": md}}],
    }


def _row(cells: list[dict]) -> dict:
    """column_set row from columns."""
    return {"tag": "column_set", "flex_mode": "none", "columns": cells}


def _pos_header_row() -> dict:
    return _row([
        _cell("80px", "**代码**"), _cell("64px", "**名称**"),
        _cell("64px", "**现价**"), _cell("72px", "**涨跌幅**"),
        _cell("48px", "**持仓**"), _cell("48px", "**可用**"),
        _cell("64px", "**成本**"), _cell("72px", "**市值**"),
        _cell("72px", "**盈亏**"),
    ])


def _wl_header_row() -> dict:
    return _row([
        _cell("80px", "**代码**"), _cell("64px", "**名称**"),
        _cell("64px", "**现价**"), _cell("72px", "**涨跌幅**"),
    ])


# ---------------------------------------------------------------------------
# Price-only card (intraday, every 1 min during trading)
# ---------------------------------------------------------------------------

async def build_price_card(
    quotes: dict[str, StockQuote],
    positions: dict[str, "Position"] | None = None,
) -> dict:
    """Compact card: code / name / price / change%, no P&L."""
    positions = positions or {}
    now_str = dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).strftime("%H:%M:%S")
    rate = await _get_hkd_cny_rate() if any(_is_hk(c) for c in quotes) else 1.0

    pos_codes = [c for c in sorted(quotes) if (p := positions.get(c)) and p.is_valid]
    wl_codes = [c for c in sorted(quotes) if c not in pos_codes]

    elements: list[dict] = []
    up = sum(1 for c in quotes.values() if c.change_pct is not None and c.change_pct >= 0)
    down = sum(1 for c in quotes.values() if c.change_pct is not None and c.change_pct < 0)

    # Header
    elements.append(_row([
        _cell("weighted", f"🔼 {up} 涨  🔽 {down} 跌  🕐 {now_str}"),
    ]))
    elements.append({"tag": "hr"})

    if pos_codes:
        elements.append({"tag": "div", "text": {"tag": "lark_md", "content": "📦 **持仓**"}})
        elements.append(_wl_header_row())
        elements.append({"tag": "hr"})
        for code in pos_codes:
            q = quotes[code]
            r = rate if _is_hk(code) else 1.0
            p_s = f"{q.price * r:.2f}" if q.price else "—"
            n_s = (q.name or "—")[:4]
            elements.append(_row([
                _cell("80px", code),
                _cell("64px", n_s),
                _cell("64px", p_s),
                _cell("72px", _color_pct(q.change_pct)),
            ]))

    if wl_codes:
        elements.append({"tag": "hr"})
        elements.append({"tag": "div", "text": {"tag": "lark_md", "content": "👀 **自选**"}})
        elements.append(_wl_header_row())
        elements.append({"tag": "hr"})
        for code in wl_codes:
            q = quotes[code]
            r = rate if _is_hk(code) else 1.0
            p_s = f"{q.price * r:.2f}" if q.price else "—"
            n_s = (q.name or "—")[:4]
            elements.append(_row([
                _cell("80px", code),
                _cell("64px", n_s),
                _cell("64px", p_s),
                _cell("72px", _color_pct(q.change_pct)),
            ]))

    return {
        "msg_type": "interactive",
        "card": {
            "header": _build_card_header("📊 实时行情", "blue"),
            "elements": elements,
        },
    }


# ---------------------------------------------------------------------------
# Alert card
# ---------------------------------------------------------------------------

def build_alert_card(
    code: str, name: str, rule_desc: str, price: float,
    change_pct: float | None = None,
) -> dict:
    now_str = dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).strftime("%Y-%m-%d %H:%M:%S")
    color = "red" if (change_pct or 0) >= 0 else "green"
    pct_str = _color_pct(change_pct)

    elements = [
        _row([
            _cell("weighted", f"📌 代码\n**{code}**"),
            _cell("weighted", f"💰 现价\n<font color='{color}'>**{price:.2f}**</font>"),
        ]),
        _row([
            _cell("weighted", f"📛 名称\n**{name}**"),
            _cell("weighted", f"📈 涨跌幅\n{pct_str}"),
        ]),
        _row([
            _cell("weighted", f"🎯 条件\n**{rule_desc}**"),
            _cell("weighted", f"🕐 时间\n{now_str}"),
        ]),
    ]

    return {
        "msg_type": "interactive",
        "card": {"header": _build_card_header("🚨 股票告警触发", "red"), "elements": elements},
    }


# ---------------------------------------------------------------------------
# Summary card — column_set table layout
# ---------------------------------------------------------------------------

async def build_summary_card(
    quotes: dict[str, StockQuote],
    positions: dict[str, "Position"] | None = None,
) -> dict:
    positions = positions or {}
    now_str = dt.datetime.now(dt.timezone(dt.timedelta(hours=8))).strftime("%Y-%m-%d %H:%M")
    rate = await _get_hkd_cny_rate() if any(_is_hk(c) for c in quotes) else 1.0

    pos_codes = [c for c in sorted(quotes)
                 if (p := positions.get(c)) and p.is_valid]
    wl_codes = [c for c in sorted(quotes) if c not in pos_codes]

    elements: list[dict] = []
    total_mval = 0.0

    # ── Section 1: 持仓信息 ──
    if pos_codes:
        elements.append({"tag": "div", "text": {"tag": "lark_md", "content": "📦 **持仓信息**"}})
        elements.append(_pos_header_row())
        elements.append({"tag": "hr"})

        for code in pos_codes:
            q = quotes[code]
            p = positions[code]
            is_hk = _is_hk(code)
            r = rate if is_hk else 1.0

            pct_s = _color_pct(q.change_pct)
            raw_price = q.price or 0
            price_s = f"{raw_price * r:.2f}" if q.price else "—"
            name_s = (q.name or "—")[:4]
            qty_s = str(p.quantity)
            av_s = str(p.available)
            cst_s = f"{p.cost * r:.3f}"

            if q.price:
                mval = p.market_value(raw_price) * r
                mv_s = f"{mval:,.0f}"
                pnl_val = p.pnl(raw_price) * r
                pnl_s = _color_pnl(pnl_val)
                total_mval += mval
            else:
                mv_s = "—"
                pnl_s = "—"

            elements.append(_row([
                _cell("80px", code),
                _cell("64px", name_s),
                _cell("64px", price_s),
                _cell("72px", pct_s),
                _cell("48px", qty_s),
                _cell("48px", av_s),
                _cell("64px", cst_s),
                _cell("72px", mv_s),
                _cell("72px", pnl_s),
            ]))

        total_pnl = sum(
            positions[c].pnl(quotes[c].price or 0) * (rate if _is_hk(c) else 1.0)
            for c in pos_codes
        )
        elements.append({"tag": "hr"})
        elements.append({"tag": "div", "text": {"tag": "lark_md",
            "content": f"💰 持仓总市值：**{total_mval:,.0f}** 元　　📈 持仓总盈亏：{_color_pnl(total_pnl)}"}})

    # ── Section 2: 自选盯盘 ──
    if wl_codes:
        elements.append({"tag": "hr"})
        elements.append({"tag": "div", "text": {"tag": "lark_md", "content": "👀 **自选盯盘**"}})
        elements.append(_wl_header_row())
        elements.append({"tag": "hr"})

        for code in wl_codes:
            q = quotes[code]
            pct_s = _color_pct(q.change_pct)
            price_s = f"{q.price:.2f}" if q.price else "—"
            name_s = (q.name or "—")[:4]
            elements.append(_row([
                _cell("80px", code),
                _cell("64px", name_s),
                _cell("64px", price_s),
                _cell("72px", pct_s),
            ]))

    elements.append({"tag": "hr"})
    elements.append({"tag": "div", "text": {"tag": "lark_md",
        "content": f"🕐 更新时间：{now_str}"}})

    return {
        "msg_type": "interactive",
        "card": {
            "header": _build_card_header(f"📊 持仓汇总 — {now_str[:10]}", "blue"),
            "elements": elements,
        },
    }


# ---------------------------------------------------------------------------
# Send
# ---------------------------------------------------------------------------

async def send_feishu_card(cfg: ChatConfig, card: dict) -> bool:
    """Send a card message to a Feishu webhook.  Returns True on success."""
    if not cfg.is_configured:
        return False

    payload: dict = {"msg_type": card["msg_type"], "card": card["card"]}
    ts = int(time.time())
    if cfg.feishu_secret:
        payload["timestamp"] = str(ts)
        payload["sign"] = _generate_sign(ts, cfg.feishu_secret)

    try:
        async with httpx.AsyncClient(timeout=FEISHU_WEBHOOK_TIMEOUT) as client:
            resp = await client.post(
                cfg.feishu_webhook, json=payload,
                headers={"Content-Type": "application/json"},
            )
        if resp.status_code == 200:
            body = resp.json()
            return body.get("StatusCode") == 0 or body.get("code") == 0
        return False
    except Exception:
        return False
