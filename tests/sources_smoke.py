#!/usr/bin/env python3
"""Run the actual Go CLI against bounded local HTTP/TCP provider fixtures."""

import argparse
from datetime import date, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socketserver
import sqlite3
import subprocess
import tempfile
import threading
import zlib

ROOT = Path(__file__).resolve().parents[1]
SEP = "\x01"
TOKEN = "smoke-only-not-a-real-token"
MODE = {"tushare": "ok", "listing": "ok"}
REQUESTS = {"listing": 0, "sina": 0}
ASOF = "2026-10-14"
HOLIDAYS = {"2026-01-01", "2026-01-02", "2026-02-16", "2026-02-17", "2026-02-18", "2026-02-19",
            "2026-02-20", "2026-02-23", "2026-04-06", "2026-05-01", "2026-05-04", "2026-05-05",
            "2026-06-19", "2026-09-25", "2026-10-01", "2026-10-02", "2026-10-05", "2026-10-06",
            "2026-10-07"}


def bars(start, end):
    day = max(date.fromisoformat(start), date(2026, 7, 15))
    last = date.fromisoformat(end)
    rows = []
    while day <= last:
        if day.weekday() < 5 and str(day) not in HOLIDAYS:
            rows.append([str(day), 10, 11, 9, 10, 100])
        day += timedelta(days=1)
    return rows


def weekdays(start, end):
    day = date.fromisoformat(start)
    last = date.fromisoformat(end)
    rows = []
    while day <= last:
        if day.weekday() < 5 and str(day) not in HOLIDAYS:
            rows.append(str(day))
        day += timedelta(days=1)
    return rows


def iso(value):
    return f"{value[:4]}-{value[4:6]}-{value[6:8]}"


class HTTPFixture(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def send_json(self, data, status=200):
        raw = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        from urllib.parse import parse_qs, urlparse
        url = urlparse(self.path)
        if url.path.startswith("/listing/"):
            REQUESTS["listing"] += 1
            if MODE["listing"] != "ok":
                self.send_json({"error": "metadata forbidden"}, 403)
                return
            assert url.path == "/listing/688808.phtml"
            raw = ("<title>联讯仪器(688808)新股发行_新浪财经_新浪网</title>"
                   "<td>发行日期</td><td>2026-04-03</td>"
                   "<td><strong>上市日期</strong></td><td>2026-04-24</td>").encode("gb18030")
            self.send_response(200)
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        if url.path == "/tencent":
            self.send_json({"error": "upstream unavailable"}, 503)
            return
        if url.path.startswith("/sina/sh688808/"):
            REQUESTS["sina"] += 1
            if url.path.endswith("klc_kl.js"):
                raw = (ROOT / "internal/market/testdata/sh688808.sina.js").read_bytes()
            else:
                raw = b'var sh688808qfq={"total":1,"data":[{"d":"1900-01-01","f":"2"}]}\n/* fixture */'
            self.send_response(200)
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)
            return
        query = parse_qs(url.query)
        assert query["secid"] == ["1.688981"]
        rows = bars(iso(query["beg"][0]), iso(query["end"][0]))
        self.send_json({"rc": 0, "data": {"code": "688981", "market": 1, "name": "测试股票", "klines": [
            f"{day},{op},{cl},{hi},{lo},{vol},1000,1,1,1,1"
            for day, op, hi, lo, cl, vol in rows]}})

    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        assert req["token"] == TOKEN
        if MODE["tushare"] == "forbidden":
            self.send_json({"code": 2002, "msg": TOKEN})
            return
        if MODE["tushare"] == "rate":
            self.send_json({"code": -2001, "msg": TOKEN})
            return
        params = req["params"]
        if req["api_name"] == "suspend_d":
            fields, items = ["ts_code", "trade_date", "suspend_type"], []
        elif req["api_name"] == "trade_cal":
            fields = ["exchange", "cal_date", "is_open"]
            items = [["SSE", day.replace("-", ""), "1"]
                     for day in weekdays(iso(params["start_date"]), iso(params["end_date"]))]
        elif req["api_name"] == "daily":
            rows = bars(iso(params["start_date"]), iso(params["end_date"]))
            fields = ["ts_code", "trade_date", "open", "high", "low", "close", "vol"]
            items = [["688981.SH", day.replace("-", ""), op, hi, lo, cl, vol]
                     for day, op, hi, lo, cl, vol in reversed(rows)]
        else:
            assert req["api_name"] == "adj_factor"
            rows = bars(iso(params["start_date"]), iso(params["end_date"]))
            fields = ["ts_code", "trade_date", "adj_factor"]
            items = [["688981.SH", row[0].replace("-", ""), 2] for row in reversed(rows)]
        self.send_json({"code": 0, "data": {"fields": fields, "items": items}})


class BaoFixture(socketserver.StreamRequestHandler):
    def handle(self):
        self.request.settimeout(10)
        while packet := self.rfile.readline(4096):
            kind = packet[8:10].decode()
            length = int(packet[11:21])
            assert int(packet[22 + length:]) == zlib.crc32(packet[:21 + length])
            parts = packet[21:21 + length].decode().split(SEP)
            if kind == "00":
                assert parts == ["login", "anonymous", "123456", "0"]
                reply_kind = "01"
                body = SEP.join(["0", "success", "login", "anonymous"]).encode()
            elif kind == "95":
                assert parts[4] in ("sh.688981", "sh.688808")
                reply_kind = "96"
                if parts[5] == "date,code,tradestatus":
                    records = [[day, parts[4], "1"] for day in weekdays(parts[6], parts[7])]
                else:
                    records = [[day, parts[4], str(op), str(hi), str(lo), str(cl), str(vol * 100), "1", parts[9]]
                               for day, op, hi, lo, cl, vol in bars(parts[6], parts[7])]
                body = zlib.compress((SEP.join(["0", "success", parts[0], parts[1], parts[2], parts[3],
                                               json.dumps({"record": records}), *parts[4:]]) + "\n").encode())
            elif kind == "33":
                reply_kind = "34"
                records = [[day, "1"] for day in weekdays(parts[4], parts[5])]
                body = (SEP.join(["0", "success", parts[0], parts[1], parts[2], parts[3],
                                  json.dumps({"record": records}), parts[4], parts[5], "calendar_date,is_trading_day"]) + "\n").encode()
            else:
                raise AssertionError(f"unexpected BaoStock request kind {kind}")
            head = SEP.join(["00.9.10", reply_kind, f"{len(body):010d}"]).encode()
            self.wfile.write(head + body + b"\x011234<![CDATA[]]>\n")
            self.wfile.flush()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", nargs="?", default=str(ROOT / "bin/stock-watcher"))
    parser.add_argument("--image")
    args = parser.parse_args()
    with ThreadingHTTPServer(("127.0.0.1", 0), HTTPFixture) as http, socketserver.ThreadingTCPServer(("127.0.0.1", 0), BaoFixture) as tcp:
        threads = [threading.Thread(target=server.serve_forever, daemon=True) for server in (http, tcp)]
        for thread in threads:
            thread.start()
        try:
            with tempfile.TemporaryDirectory(prefix="stock-watcher-sources-") as temporary:
                root = Path(temporary)
                (root / "data").mkdir()
                (root / "logs").mkdir()
                api = f"http://127.0.0.1:{http.server_port}"
                mount = "/fixture" if args.image else str(root)
                env = os.environ.copy()
                for name in ("FEISHU_WEBHOOK", "FEISHU_SECRET", "STOCK_WATCHER_DATA_DIR", "STOCK_WATCHER_WEB_ADDR"):
                    env.pop(name, None)
                env.update({
                    "TUSHARE_TOKEN": TOKEN,
                    "STOCK_WATCHER_DATA_DIR": f"{mount}/data",
                    "STOCK_WATCHER_LOG_DIR": f"{mount}/logs",
                    "STOCK_WATCHER_LOG_COLOR": "off",
                    "STOCK_WATCHER_REQUIRE_CIFS": "false",
                    "STOCK_WATCHER_WEB_ENABLED": "false",
                    "STOCK_WATCHER_FEISHU_ENABLED": "false",
                    "STOCK_WATCHER_INTRADAY_ENABLED": "false",
                    "STOCK_WATCHER_STRATEGY_ENABLED": "false",
                    "STOCK_WATCHER_REQUEST_TIMEOUT": "1s",
                    "STOCK_WATCHER_MARKET_REQUEST_INTERVAL": "350ms",
                    "STOCK_WATCHER_POOL_FILE": f"{mount}/leaders.yaml",
                    "STOCK_WATCHER_CALENDAR_VALID_FROM": "2026-01-01",
                    "STOCK_WATCHER_CALENDAR_VALID_THROUGH": "2026-12-31",
                })

                def config(providers, code="sh688981", listed="2026-07-15", days=90, holidays=None):
                    pool = f"schema_version: 1\nstocks:\n  - code: {code}\n    name: 测试股票\n"
                    if listed:
                        pool += f"    listed_from: '{listed}'\n"
                    (root / "leaders.yaml").write_text(pool)
                    env.update({
                        "STOCK_WATCHER_MARKET_PROVIDERS": ",".join(providers),
                        "STOCK_WATCHER_DAILY_HISTORY_DAYS": str(days),
                        "STOCK_WATCHER_CALENDAR_HOLIDAYS": ",".join(holidays or []),
                        "STOCK_WATCHER_MARKET_TENCENT_ENDPOINT": f"{api}/tencent",
                        "STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS": f"127.0.0.1:{tcp.server_address[1]}",
                        "STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT": f"{api}/tushare",
                        "STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT": f"{api}/eastmoney",
                        "STOCK_WATCHER_MARKET_SINA_ENDPOINT": f"{api}/sina",
                        "STOCK_WATCHER_MARKET_LISTING_ENDPOINT": f"{api}/listing",
                    })

                def invoke(*command, fail=False):
                    if args.image:
                        prefix = ["docker", "run", "--rm", "--network", "host", "--read-only", "--cpus", "1", "--memory", "256m",
                                  "--pids-limit", "64", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                                  "--mount", f"type=bind,src={root},dst=/fixture"]
                        for key in ("TUSHARE_TOKEN", "STOCK_WATCHER_DATA_DIR", "STOCK_WATCHER_LOG_DIR",
                                    "STOCK_WATCHER_LOG_COLOR", "STOCK_WATCHER_REQUIRE_CIFS",
                                    "STOCK_WATCHER_WEB_ENABLED", "STOCK_WATCHER_FEISHU_ENABLED",
                                    "STOCK_WATCHER_INTRADAY_ENABLED", "STOCK_WATCHER_REQUEST_TIMEOUT",
                                    "STOCK_WATCHER_MARKET_REQUEST_INTERVAL", "STOCK_WATCHER_POOL_FILE",
                                    "STOCK_WATCHER_CALENDAR_VALID_FROM", "STOCK_WATCHER_CALENDAR_VALID_THROUGH",
                                    "STOCK_WATCHER_CALENDAR_HOLIDAYS", "STOCK_WATCHER_MARKET_PROVIDERS",
                                    "STOCK_WATCHER_DAILY_HISTORY_DAYS", "STOCK_WATCHER_MARKET_TENCENT_ENDPOINT",
                                    "STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS", "STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT",
                                    "STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT", "STOCK_WATCHER_MARKET_SINA_ENDPOINT",
                                    "STOCK_WATCHER_MARKET_LISTING_ENDPOINT"):
                            prefix += ["-e", f"{key}={env.get(key, '')}"]
                        prefix += [args.image]
                    else:
                        prefix = [args.binary]
                    result = subprocess.run(prefix + list(command), env=env,
                                            capture_output=True, text=True, timeout=45)
                    assert (result.returncode == 1 if fail else result.returncode == 0), result.stdout + result.stderr
                    assert TOKEN not in result.stdout + result.stderr, "credential leaked"
                    return result

                config(["tencent", "baostock"])
                invoke("init")
                invoke("status-backfill", "-as-of", ASOF)
                for providers, mode, expected in [
                    (["tencent", "baostock"], "ok", "baostock"),
                    (["tencent", "tushare"], "ok", "tushare"),
                    (["tushare", "eastmoney"], "forbidden", "eastmoney"),
                    (["tushare", "eastmoney"], "rate", "eastmoney"),
                ]:
                    MODE["tushare"] = mode
                    config(providers)
                    invoke("sync", "-as-of", ASOF)
                    with sqlite3.connect(root / "data/market.db") as db:
                        values = db.execute("SELECT DISTINCT source, volume_raw, volume_shares FROM daily_bar").fetchall()
                        assert values == [(expected, 10000.0 if expected == "baostock" else 100.0, 10000)], values
                        assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)
                env["TUSHARE_TOKEN"] = ""
                config(["tushare", "eastmoney"])
                result = invoke("check")
                assert json.loads(result.stdout.splitlines()[-1])["market_sources"] == ["eastmoney"]
                config(["tushare"])
                invoke("check-config", fail=True)
                config(["tencent"])
                invoke("sync", "-as-of", ASOF, fail=True)
                with sqlite3.connect(root / "data/market.db") as db:
                    assert db.execute("SELECT DISTINCT source FROM daily_bar").fetchall() == [("eastmoney",)]
                config(["tencent", "sina"], code="sh688808", listed="", days=180,
                       holidays=["2026-05-01", "2026-05-04", "2026-05-05", "2026-06-19", "2026-09-25",
                                 "2026-10-01", "2026-10-02", "2026-10-05", "2026-10-06", "2026-10-07"])
                invoke("status-backfill", "-as-of", ASOF)
                invoke("sync", "-as-of", ASOF)
                metadata = root / "data/metadata/listings/sh688808.json"
                listing = json.loads(metadata.read_text())
                assert listing["schema_version"] == 1 and listing["code"] == "sh688808"
                assert listing["listed_from"] == "2026-04-24" and listing["source"] == "sina_ipo"
                assert REQUESTS["listing"] == 1, REQUESTS
                # A fresh process reuses durable metadata even when its source is offline.
                MODE["listing"] = "forbidden"
                invoke("sync", "-as-of", ASOF)
                assert REQUESTS["listing"] == 1, "cached listing was fetched again"
                with sqlite3.connect(root / "data/market.db") as db:
                    rows = db.execute("SELECT adjust, count(*), min(source), min(volume_raw=volume_shares) FROM daily_bar WHERE code='sh688808' GROUP BY adjust").fetchall()
                    assert rows == [("none", 114, "sina", 1), ("qfq", 114, "sina", 1)], rows
                    raw, adjusted = db.execute("SELECT close_micros FROM daily_bar WHERE code='sh688808' AND trade_date=? ORDER BY adjust", (ASOF,)).fetchall()
                    assert abs(raw[0] - 2 * adjusted[0]) <= 10000, (raw, adjusted)
                    assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)
                # Without verified metadata, skip price downloads and retain saved bars.
                metadata.unlink()
                before = REQUESTS["sina"]
                result = invoke("sync", "-as-of", ASOF, fail=True)
                assert "listing_lookup" in result.stdout, result.stdout
                assert REQUESTS["listing"] == 3 and REQUESTS["sina"] == before, REQUESTS
                with sqlite3.connect(root / "data/market.db") as db:
                    assert db.execute("SELECT count(*) FROM daily_bar WHERE code='sh688808'").fetchone() == (228,)
                for log in (root / "logs").glob("*.log"):
                    assert TOKEN not in log.read_text(), "credential leaked to file log"
                print(json.dumps({"environment": args.image or "native", "result": "passed", "checks": "Go CLI/HTTP+TCP fallback/Sina KLC2+factors/listing preflight+durable cache+403 isolation/incremental SQLite/source units/token missing/permission/rate/all-failed preservation/no credential logging"}))
        finally:
            for server in (http, tcp):
                server.shutdown()
            for thread in threads:
                thread.join(timeout=3)


if __name__ == "__main__":
    main()
