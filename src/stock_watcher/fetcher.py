"""HTTP fetchers for Sina (A-share) and Tencent (HK) real-time quote APIs."""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Optional

import httpx

from stock_watcher.config import RequestConfig
from stock_watcher.http_client import get_shared_client
from stock_watcher.models import Market, StockQuote


# ---------------------------------------------------------------------------
# FetchError
# ---------------------------------------------------------------------------


class FetchError(Exception):
    """Raised when a stock quote fetch fails."""

    def __init__(
        self,
        message: str,
        *,
        code: Optional[str] = None,
        status_code: Optional[int] = None,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.status_code = status_code

    @property
    def is_retryable(self) -> bool:
        """Return True if the error is transient and worth retrying."""
        if self.status_code is None:
            # timeout / network error → retryable
            return True
        # 429, 5xx → retryable; 4xx (except 429) → not retryable
        return self.status_code == 429 or self.status_code >= 500


# ---------------------------------------------------------------------------
# Response parsers
# ---------------------------------------------------------------------------


def parse_sina_response(code: str, text: str) -> StockQuote:
    """Parse a Sina Finance real-time quote response.

    Format: ``var hq_str_<code>="name,open,prev_close,price,high,low,...";``

    Field indices (0-based):
      0: name
      1: today open
      2: previous close
      3: current price
      4: today high
      5: today low
      6: bid (竞买价)
      7: ask (竞卖价)
      8: volume (成交量, shares)
      9: turnover (成交额, yuan)
      10–19: bid volumes + prices (买1量,买1价,…,买5量,买5价)
      20–29: ask volumes + prices (卖1量,卖1价,…,卖5量,卖5价)

    change_pct and change_amount are derived from price vs previous close.
    """
    try:
        text = text.strip()
        if not text:
            return StockQuote(code=code)

        # Extract the quoted CSV portion
        start = text.index('="') + 2
        end = text.index('";', start) if '";' in text else len(text)
        quoted = text[start:end]

        fields = quoted.split(",")
        if len(fields) < 6:
            return StockQuote(code=code)

        name = fields[0]
        open_ = _parse_float(fields[1])
        prev_close = _parse_float(fields[2])
        price = _parse_float(fields[3])
        high = _parse_float(fields[4])
        low = _parse_float(fields[5])
        bid = _parse_float(fields[6]) if len(fields) > 6 else None
        ask = _parse_float(fields[7]) if len(fields) > 7 else None
        volume = _parse_float(fields[8]) if len(fields) > 8 else None
        turnover = _parse_float(fields[9]) if len(fields) > 9 else None

        # 5-level order book: fields[10..19] bid, fields[20..29] ask
        bid_volumes: list[float] = []
        bid_prices: list[float] = []
        ask_volumes: list[float] = []
        ask_prices: list[float] = []
        if len(fields) >= 30:
            for i in range(5):
                bv = _parse_float(fields[10 + i * 2])
                bp = _parse_float(fields[11 + i * 2])
                av = _parse_float(fields[20 + i * 2])
                ap = _parse_float(fields[21 + i * 2])
                if bv is not None:
                    bid_volumes.append(bv)
                if bp is not None:
                    bid_prices.append(bp)
                if av is not None:
                    ask_volumes.append(av)
                if ap is not None:
                    ask_prices.append(ap)

        change_pct = _calc_pct(price, prev_close)
        change_amount = _calc_amount(price, prev_close)

        return StockQuote(
            code=code,
            name=name,
            price=price,
            change_pct=change_pct,
            change_amount=change_amount,
            high=high,
            low=low,
            open=open_,
            volume=volume,
            turnover=turnover,
            bid=bid,
            ask=ask,
            bid_prices=bid_prices,
            bid_volumes=bid_volumes,
            ask_prices=ask_prices,
            ask_volumes=ask_volumes,
        )
    except (ValueError, IndexError):
        return StockQuote(code=code)


def parse_tencent_response(code: str, text: str) -> StockQuote:
    """Parse a Tencent Finance real-time quote response for HK stocks.

    Format: ``v_<code>="<fields separated by ~>"``

    Field indices (0-based, ~ delimited):
      1: name
      3: current price
      4: yesterday close
      5: open
      6: volume (shares)
      31: change amount
      32: change percent
      33: high
      34: low
      37: turnover (成交额, HKD)
      39: PE ratio (市盈率)
      44: market cap (总市值, 亿)

    Falls back to deriving change from price vs yesterday close
    when direct fields are unavailable.
    """
    try:
        text = text.strip()
        if not text:
            return StockQuote(code=code)

        # Extract the quoted ~-delimited portion
        start = text.index('="') + 2
        end = text.index('";', start) if '";' in text else len(text)
        quoted = text[start:end]

        fields = quoted.split("~")
        if len(fields) < 35:
            return StockQuote(code=code)

        name = fields[1]
        price = _parse_float(fields[3])
        prev_close = _parse_float(fields[4])
        open_ = _parse_float(fields[5]) if len(fields) > 5 else None
        volume = _parse_float(fields[6]) if len(fields) > 6 else None
        high = _parse_float(fields[33])
        low = _parse_float(fields[34])
        change_amount = _parse_float(fields[31]) if len(fields) > 31 else None
        change_pct = _parse_float(fields[32]) if len(fields) > 32 else None
        turnover = _parse_float(fields[37]) if len(fields) > 37 else None
        pe = _parse_float(fields[39]) if len(fields) > 39 else None
        market_cap = _parse_float(fields[44]) if len(fields) > 44 else None

        # Fallback: derive from price vs prev close if direct fields are None
        if change_pct is None:
            change_pct = _calc_pct(price, prev_close)
        if change_amount is None:
            change_amount = _calc_amount(price, prev_close)

        return StockQuote(
            code=code,
            name=name,
            price=price,
            change_pct=change_pct,
            change_amount=change_amount,
            high=high,
            low=low,
            open=open_,
            volume=volume,
            turnover=turnover,
            pe=pe,
            market_cap=market_cap,
        )
    except (ValueError, IndexError):
        return StockQuote(code=code)


def parse_sina_us_response(code: str, text: str) -> StockQuote:
    """Parse Sina Finance real-time quote response for US stocks (gb_ prefix).

    Example response::

        var hq_str_gb_aapl="苹果,281.74,-0.72,..."

    Field indices (0-based, comma-delimited):
      0: name
      1: price
      2: change_pct (%)
      3: datetime
      4: change_amount
      5: high (day)
      6: open
      7: low (day)
      8: 52-week high
      9: 52-week low
    """
    text = text.strip()
    if not text:
        return StockQuote(code=code)

    if "=" not in text:
        return StockQuote(code=code)

    # Extract value inside quotes after "=
    _prefix, raw = text.split("=", 1)
    raw = raw.strip().strip('"')

    if not raw:
        return StockQuote(code=code)

    fields = raw.split(",")
    if len(fields) < 8:
        return StockQuote(code=code)

    try:
        price = _parse_float(fields[1])
        change_pct = _parse_float(fields[2])
        change_amount = _parse_float(fields[4])
        high = _parse_float(fields[5])
        low = _parse_float(fields[7])
    except (ValueError, IndexError):
        return StockQuote(code=code)

    return StockQuote(
        code=code,
        name=fields[0].strip() or None,
        price=price,
        change_pct=change_pct,
        change_amount=change_amount,
        high=high,
        low=low,
    )


# ---------------------------------------------------------------------------
# Fetcher base & implementations
# ---------------------------------------------------------------------------


class Fetcher(ABC):
    """Abstract base for stock quote fetchers."""

    def __init__(self, request_config: RequestConfig) -> None:
        self._config = request_config

    @abstractmethod
    async def fetch(self, code: str) -> StockQuote:
        """Fetch a quote for the given stock code."""


class SinaFetcher(Fetcher):
    """Fetcher for A-share + US stocks using Sina Finance API.

    A-share:  ``https://hq.sinajs.cn/list=sh000001``
    US:       ``https://hq.sinajs.cn/list=gb_aapl``
    """

    BASE_URL = "https://hq.sinajs.cn/list={code}"

    def _api_code(self, code: str) -> str:
        """Translate internal code to Sina API format.

        ``usaapl`` → ``gb_aapl``,  everything else passed through.
        """
        if code.startswith("us"):
            return f"gb_{code[2:].lower()}"
        return code

    async def fetch(self, code: str) -> StockQuote:
        api_code = self._api_code(code)
        url = self.BASE_URL.format(code=api_code)
        client = get_shared_client()
        try:
            headers = {
                "User-Agent": self._config.get_random_ua(),
                "Referer": "https://finance.sina.com.cn/",
            }
            resp = await client.get(url, headers=headers)
            if resp.status_code != 200:
                raise FetchError(
                    f"HTTP {resp.status_code}", code=code, status_code=resp.status_code
                )
            if code.startswith("us"):
                return parse_sina_us_response(code, resp.text)
            return parse_sina_response(code, resp.text)
        except httpx.TimeoutException:
            raise FetchError("timeout", code=code)
        except FetchError:
            raise
        finally:
            pass  # shared client, do not close


class TencentFetcher(Fetcher):
    """Fetcher for HK stocks using Tencent Finance API."""

    BASE_URL = "https://qt.gtimg.cn/q={code}"

    async def fetch(self, code: str) -> StockQuote:
        url = self.BASE_URL.format(code=code)
        client = get_shared_client()
        try:
            headers = {
                "User-Agent": self._config.get_random_ua(),
                "Referer": "https://gu.qq.com/",
            }
            resp = await client.get(url, headers=headers)
            if resp.status_code != 200:
                raise FetchError(
                    f"HTTP {resp.status_code}", code=code, status_code=resp.status_code
                )
            return parse_tencent_response(code, resp.text)
        except httpx.TimeoutException:
            raise FetchError("timeout", code=code)
        except FetchError:
            raise
        finally:
            pass  # shared client, do not close


def get_fetcher(code: str, request_config: Optional[RequestConfig] = None) -> Fetcher:
    """Factory: return the correct Fetcher for a given stock code."""
    if request_config is None:
        request_config = RequestConfig()

    market = Market.from_code(code)
    if market in (Market.SHANGHAI, Market.SHENZHEN, Market.USA):
        return SinaFetcher(request_config)
    elif market == Market.HONGKONG:
        return TencentFetcher(request_config)
    raise ValueError(f"Unknown market prefix for code {code!r}")


# ---------------------------------------------------------------------------
# Stock search (by name or partial code)
# ---------------------------------------------------------------------------


@dataclass
class SearchResult:
    """A single result from a stock name/code search."""

    code: str
    name: str
    market: str  # "sh", "sz", "hk"


async def search_stocks(
    query: str,
    request_config: Optional[RequestConfig] = None,
) -> list[SearchResult]:
    """Search stocks by name or partial code using the Sina suggest API.

    Returns up to ~10 matching stocks with their codes and market prefixes.
    """
    if request_config is None:
        request_config = RequestConfig()

    url = f"https://suggest3.sinajs.cn/suggest/type=11,12,31,14,15&key={query}"

    client = get_shared_client()
    try:
        headers = {
            "User-Agent": request_config.get_random_ua(),
            "Referer": "https://finance.sina.com.cn/",
        }
        resp = await client.get(url, headers=headers)
        if resp.status_code != 200:
            return []
        return _parse_suggest_response(resp.text)
    except httpx.TimeoutException:
        return []
    finally:
        pass  # shared client, do not close


def _parse_suggest_response(text: str) -> list[SearchResult]:
    """Parse the Sina suggest API response.

    Format: ``var suggestvalue="name1,type1,code1,full_code1;..."``

    Market codes from Sina:
      11 = Shanghai A-share  → prefix "sh"
      12 = Shenzhen A-share → prefix "sz"
      31 = HK stock          → prefix "hk"
    """
    results: list[SearchResult] = []
    try:
        text = text.strip()
        if not text:
            return results

        # Extract quoted portion
        start = text.index('="') + 2
        end = text.index('";', start) if '";' in text else len(text)
        quoted = text[start:end]

        if not quoted:
            return results

        for entry in quoted.split(";"):
            parts = entry.split(",")
            if len(parts) < 3:
                continue
            # Field layout (verified against live API):
            #   [0] display code (may be prefixed code or name)
            #   [1] market type (11=sh, 12=sz, 31=hk)
            #   [2] raw code (no prefix, e.g. "00700")
            #   [3] code (prefixed for A-share, raw for HK)
            #   [4] name
            name = parts[4].strip() if len(parts) > 4 else parts[0].strip()
            market_id = parts[1].strip()
            raw_code = parts[2].strip()

            prefix = _sina_market_to_prefix(market_id)
            if prefix is None:
                continue

            code = f"{prefix}{raw_code}"
            results.append(SearchResult(code=code, name=name, market=prefix))

    except (ValueError, IndexError):
        pass

    return results


def _sina_market_to_prefix(market_id: str) -> Optional[str]:
    """Map Sina suggest market ID to our stock code prefix."""
    mapping = {
        "11": "sh",
        "12": "sz",
        "31": "hk",
    }
    return mapping.get(market_id)


def _parse_float(s: str) -> Optional[float]:
    """Parse a string to float; return None on failure."""
    if not s or s.strip() == "":
        return None
    try:
        return float(s.strip())
    except ValueError:
        return None


def _calc_pct(price: Optional[float], prev_close: Optional[float]) -> Optional[float]:
    """Compute change percentage from price and previous close."""
    if price is None or price == 0 or prev_close is None or prev_close == 0:
        return None
    return round((price - prev_close) / prev_close * 100, 2)


def _calc_amount(price: Optional[float], prev_close: Optional[float]) -> Optional[float]:
    """Compute change amount from price and previous close."""
    if price is None or price == 0 or prev_close is None or prev_close == 0:
        return None
    return round(price - prev_close, 2)
