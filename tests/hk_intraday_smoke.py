#!/usr/bin/env python3
"""Exercise the real HK minute CLI and browser against isolated local services."""

import argparse
from datetime import datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlparse
from urllib.request import Request, urlopen
from zoneinfo import ZoneInfo

from playwright.sync_api import sync_playwright


ROOT = Path(__file__).resolve().parents[1]
DAYS = ["2026-10-29", "2026-10-30"]
STATE = {"mode": "ok", "minute_requests": 0, "daily_mode": "off"}
TZ = ZoneInfo("Asia/Shanghai")


def minute_rows(day):
    date = datetime.fromisoformat(day).replace(tzinfo=TZ)
    rows = []
    for start, end in ((570, 720), (780, 960)):
        for minute in range(start + 5, end + 1, 5):
            stamp = date + timedelta(minutes=minute)
            rows.append(f"{stamp:%Y-%m-%d %H:%M},10,11,12,9,100,1000,1,2,3,4")
    return rows


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, payload):
        data = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        try:
            self.wfile.write(data)
        except BrokenPipeError:
            pass

    def do_GET(self):
        path = urlparse(self.path)
        query = parse_qs(path.query)
        if path.path == "/sina":
            self.reply(503, {"error": "fixture unavailable"})
        elif path.path == "/universe":
            assert query.get("fs") == ["b:DLMK0146,b:DLMK0144"]
            self.reply(200, {"rc": 0, "data": {"total": 1, "diff": [{"f12": "00700", "f14": "腾讯控股"}]}})
        elif path.path in ("/appstock/app/fqkline/get", "/appstock/app/hkfqkline/get"):
            parts = query["param"][0].split(",")
            assert parts[0] in ("hkHSI", "hk00700")
            if STATE["daily_mode"] == "deny":
                self.reply(403, {"error": "fixture denied"})
                return
            if STATE["daily_mode"] == "run":
                date = datetime.fromisoformat(parts[3]).date()
                dates = []
                while date.isoformat() >= parts[2] and len(dates) < 10:
                    if date.weekday() < 5:
                        dates.append(date.isoformat())
                    date -= timedelta(days=1)
                dates.reverse()
            else:
                assert parts[0] == "hkHSI"
                dates = [day for day in DAYS if parts[2] <= day <= parts[3]]
            rows = [[day, "100", "101", "102", "99", "1000"] for day in dates]
            field = "qfqday" if parts[5] == "qfq" else "day"
            self.reply(200, {"code": 0, "data": {parts[0]: {field: rows}}})
        elif path.path == "/minutes":
            STATE["minute_requests"] += 1
            assert query.get("secid") == ["116.00700"] and query.get("klt") == ["5"] and query.get("fqt") == ["0"]
            if STATE["mode"] == "deny":
                self.reply(403, {"error": "denied"})
                return
            if STATE["mode"] == "timeout":
                time.sleep(1.5)
                self.reply(200, {})
                return
            begin, end = query["beg"][0], query["end"][0]
            rows = [row for day in DAYS if begin <= day.replace("-", "") <= end for row in minute_rows(day)]
            if STATE["mode"] == "gap":
                rows = [row for row in rows if not row.startswith("2026-10-29 10:00")]
            self.reply(200, {"rc": 0, "data": {"code": "00700", "market": 116, "name": "腾讯控股", "klines": rows}})
        else:
            self.reply(503, {"error": "fixture unavailable"})


class Gateway(BaseHTTPRequestHandler):
    upstream = ""
    api_unavailable = False

    def log_message(self, *_):
        pass

    def forward(self):
        cookie = self.headers.get("Cookie", "")
        if not any(f"hk_test_role={role}" in cookie for role in ("viewer", "administrator")):
            status = 403 if "hk_test_role=denied" in cookie else 401
            title = "Forbidden" if status == 403 else "Authentication Required"
            body = f"<!doctype html><title>{title}</title><h1>{title}</h1>".encode()
            self.send_response(status)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)
            return
        if self.api_unavailable and self.path.startswith("/api/hk-intraday?"):
            body = b'{"error":"fixture unavailable"}'
            self.send_response(503)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)
            return
        request = Request(self.upstream + self.path, method=self.command, data=b"" if self.command == "POST" else None)
        try:
            response = urlopen(request, timeout=5)
        except HTTPError as error:
            response = error
        with response:
            body = response.read()
            self.send_response(response.status)
            for key, value in response.headers.items():
                if key.lower() not in ("connection", "transfer-encoding", "content-length"):
                    self.send_header(key, value)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)

    do_GET = forward
    do_HEAD = forward
    do_POST = forward


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", nargs="?", default=str(ROOT / "bin/stock-watcher"))
    parser.add_argument("--image")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    with ThreadingHTTPServer(("127.0.0.1", 0), Provider) as provider, tempfile.TemporaryDirectory(prefix="stock-watcher-hk-minute-") as temporary:
        provider.daemon_threads = True
        threading.Thread(target=provider.serve_forever, daemon=True).start()
        root = Path(temporary)
        (root / "data").mkdir()
        (root / "logs").mkdir()
        (root / "leaders.yaml").write_text("schema_version: 1\nstocks:\n  - code: sz000001\n    name: 本地测试\n    listed_from: 2026-01-01\n")
        port = free_port()
        upstream = f"http://127.0.0.1:{provider.server_port}"
        app = f"http://127.0.0.1:{port}"
        mount = "/fixture" if args.image else str(root)
        env = {key: value for key, value in os.environ.items() if not key.startswith(("FEISHU_", "TUSHARE_", "WECHAT_", "STOCK_WATCHER_"))}
        configured = {
            "STOCK_WATCHER_DATA_DIR": f"{mount}/data",
            "STOCK_WATCHER_LOG_DIR": f"{mount}/logs",
            "STOCK_WATCHER_POOL_FILE": f"{mount}/leaders.yaml",
            "STOCK_WATCHER_LOG_COLOR": "off",
            "STOCK_WATCHER_REQUIRE_CIFS": "false",
            "STOCK_WATCHER_REQUEST_TIMEOUT": "1s",
            "STOCK_WATCHER_POLL_INTERVAL": "1h",
            "STOCK_WATCHER_WEB_ADDR": f"{'0.0.0.0' if args.image else '127.0.0.1'}:{port}",
            "STOCK_WATCHER_INTRADAY_ENABLED": "false",
            "STOCK_WATCHER_HK_INTRADAY_ENABLED": "true",
            "STOCK_WATCHER_HK_SYNC_ENABLED": "false",
            "STOCK_WATCHER_HK_INTRADAY_REQUEST_INTERVAL": "350ms",
            "STOCK_WATCHER_HK_CONNECT_ENABLED": "false",
            "STOCK_WATCHER_HK_CONNECT_PUSH_ENABLED": "false",
            "STOCK_WATCHER_STRATEGY_ENABLED": "false",
            "STOCK_WATCHER_CONTEXT_ENABLED": "false",
            "STOCK_WATCHER_PREDICTOR_ENABLED": "false",
            "STOCK_WATCHER_PREDICTOR_PUSH_ENABLED": "false",
            "STOCK_WATCHER_DAYRANGE_ENABLED": "false",
            "STOCK_WATCHER_DAYRANGE_PUSH_ENABLED": "false",
            "STOCK_WATCHER_WECHAT_ENABLED": "false",
            "STOCK_WATCHER_FEISHU_ENABLED": "false",
            "STOCK_WATCHER_MARKET_PROVIDERS": "tencent",
            "STOCK_WATCHER_MARKET_REQUEST_INTERVAL": "350ms",
            "STOCK_WATCHER_MARKET_TENCENT_ENDPOINT": f"{upstream}/daily",
            "STOCK_WATCHER_MARKET_HK_ENDPOINT": upstream,
            "STOCK_WATCHER_MARKET_HK_ENDPOINT_FALLBACK": upstream,
            "STOCK_WATCHER_MARKET_HK_UNIVERSE_ENDPOINT": f"{upstream}/sina",
            "STOCK_WATCHER_MARKET_HK_UNIVERSE_FALLBACK": f"{upstream}/universe",
            "STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT": f"{upstream}/minutes",
            "STOCK_WATCHER_CALENDAR_VALID_FROM": "2026-01-01",
            "STOCK_WATCHER_CALENDAR_VALID_THROUGH": "2026-12-31",
            "STOCK_WATCHER_CALENDAR_HOLIDAYS": "",
            "NO_PROXY": "127.0.0.1,localhost",
        }
        env.update(configured)
        container_name = f"stock-watcher-hk-minute-{os.getpid()}"

        def command(arguments, service=False, overrides=None):
            settings = {**configured, **(overrides or {})}
            if args.image:
                prefix = ["docker", "run", "--rm", "--network", "host", "--init", "--read-only", "--cap-drop", "ALL",
                          "--security-opt", "no-new-privileges", "--cpus", "1", "--memory", "256m", "--pids-limit", "64",
                          "--mount", f"type=bind,src={root},dst=/fixture"]
                for key, value in settings.items():
                    prefix.extend(["-e", f"{key}={value}"])
                if service:
                    prefix.extend(["--name", container_name])
                return prefix + [args.image] + arguments
            return [binary] + arguments

        def invoke(arguments, success=True, overrides=None):
            process = subprocess.run(command(arguments, overrides=overrides), cwd=root, env={**env, **(overrides or {})}, capture_output=True, text=True, timeout=35)
            assert (process.returncode == 0) == success, process.stdout + process.stderr
            return process

        def sync(start, end, success=True):
            return invoke(["sync-hk-minutes", "-from", start, "-through", end, "-codes", "hk00700", "-workers", "1", "-timeout", "15s"], success)

        def saved():
            with sqlite3.connect(root / "data/hk-market.db") as database:
                return database.execute("SELECT end_unix,close_micros,volume_raw FROM intraday_bar ORDER BY end_unix").fetchall()

        try:
            invoke(["init"])
            sync(DAYS[1], DAYS[1])
            with sqlite3.connect(root / "data/hk-market.db") as database:
                database.executescript("DROP TABLE intraday_bar; DROP TABLE intraday_sync; PRAGMA user_version=2;")
                database.execute("INSERT INTO daily_bar VALUES(?,?,?,?,?,?,?,?,?,?,?)", ("hk00700", "none", DAYS[1], 10000000, 11000000, 12000000, 9000000, 100, 100, "day", "tencent_hk"))
            sync(DAYS[1], DAYS[1])
            sync(DAYS[0], DAYS[1])
            baseline = saved()
            assert len(baseline) == 132
            with sqlite3.connect(root / "data/hk-market.db") as database:
                assert database.execute("PRAGMA user_version").fetchone() == (3,)
                assert database.execute("PRAGMA integrity_check").fetchone() == ("ok",)
                assert database.execute("SELECT count(*) FROM daily_bar").fetchone() == (1,)
            with sqlite3.connect(root / "data/market.db") as database:
                assert database.execute("SELECT count(*) FROM intraday_bar WHERE code LIKE 'hk%'").fetchone() == (0,)
            for mode in ("deny", "timeout"):
                STATE["mode"] = mode
                sync(DAYS[0], DAYS[1], False)
                assert saved() == baseline
            STATE["mode"] = "gap"
            sync(DAYS[0], DAYS[1], False)
            assert saved() == baseline
            STATE["mode"] = "ok"
            sync(DAYS[1], DAYS[1], False)
            with sqlite3.connect(root / "data/hk-market.db") as database:
                assert database.execute("SELECT status,reason,unresolved_from,unresolved_through FROM intraday_sync WHERE code='hk00700'").fetchone() == (
                    "partial", "history_gap_unresolved", DAYS[0], DAYS[0]
                )
            sync(DAYS[0], DAYS[1])
            with sqlite3.connect(root / "data/hk-market.db") as database:
                assert database.execute("SELECT unresolved_from,unresolved_through FROM intraday_sync WHERE code='hk00700'").fetchone() == ("", "")
            invoke(["sync-hk-minutes", "-codes", ","], False)
            invoke(["sync-hk-minutes"], False, {"STOCK_WATCHER_HK_INTRADAY_ENABLED": "false"})
            invoke(["check-config"], False, {"STOCK_WATCHER_HK_INTRADAY_VALID_THROUGH": ""})
            assert saved() == baseline

            daily_overrides = {"STOCK_WATCHER_HK_SYNC_ENABLED": "true", "STOCK_WATCHER_HK_INTRADAY_ENABLED": "false"}
            for mode, expected in (("deny", "HK Connect scheduled synchronization failed"),
                                   ("run", "HK Connect scheduled synchronization completed")):
                STATE["daily_mode"] = mode
                daily_log = root / f"daily-{mode}.log"
                with daily_log.open("w") as log:
                    process = subprocess.Popen(command(["run"], service=True, overrides=daily_overrides), cwd=root,
                                               env={**env, **daily_overrides}, stdout=log, stderr=subprocess.STDOUT)
                    try:
                        deadline = time.monotonic() + 25
                        while expected not in daily_log.read_text():
                            assert process.poll() is None and time.monotonic() < deadline, daily_log.read_text()
                            time.sleep(0.1)
                        with urlopen(app + "/healthz", timeout=1) as response:
                            assert response.status == 200
                        if mode == "run":
                            with sqlite3.connect(root / "data/hk-market.db") as database:
                                assert database.execute("SELECT status,total,failed,partial FROM sync_run ORDER BY started_at DESC LIMIT 1").fetchone() == ("complete", 1, 0, 0)
                                assert database.execute("SELECT COUNT(DISTINCT adjust) FROM daily_bar WHERE code='hk00700'").fetchone() == (2,)
                    finally:
                        if args.image:
                            subprocess.run(["docker", "stop", "--time", "10", container_name], capture_output=True, timeout=15)
                        else:
                            process.terminate()
                        process.wait(timeout=20)
                    assert process.returncode == 0, daily_log.read_text()
            STATE["daily_mode"] = "off"

            with (root / "service.log").open("w") as log:
                process = subprocess.Popen(command(["run"], service=True), cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT)
                try:
                    deadline = time.monotonic() + 20
                    while True:
                        assert process.poll() is None, (root / "service.log").read_text()
                        try:
                            with urlopen(app + "/healthz", timeout=1) as response:
                                assert response.status == 200
                            break
                        except (OSError, URLError):
                            assert time.monotonic() < deadline, (root / "service.log").read_text()
                            time.sleep(0.1)

                    deadline = time.monotonic() + 10
                    while "HK minute synchronization started" not in (root / "service.log").read_text():
                        assert process.poll() is None and time.monotonic() < deadline, (root / "service.log").read_text()
                        time.sleep(0.1)

                    Gateway.upstream = app
                    with ThreadingHTTPServer(("127.0.0.1", 0), Gateway) as gateway:
                        gateway.daemon_threads = True
                        threading.Thread(target=gateway.serve_forever, daemon=True).start()
                        gateway_url = f"http://127.0.0.1:{gateway.server_port}"
                        with sync_playwright() as playwright:
                            browser = playwright.chromium.launch(headless=True, args=["--no-sandbox"])
                            context = browser.new_context()
                            page = context.new_page()
                            page_errors = []
                            console_errors = []
                            responses = []
                            page.on("pageerror", lambda error: page_errors.append(str(error)))
                            page.on("console", lambda message: console_errors.append(message.text) if message.type == "error" else None)
                            page.on("response", lambda response: responses.append((response.url, response.status)))
                            response = page.goto(app + "/calculator", wait_until="networkidle", timeout=15000)
                            assert response.status == 200 and page.url == app + "/calculator"
                            assert page.title() == "仓位计算 · Stock Watcher"
                            assert "仓位计算" in page.locator("h1").inner_text()
                            assert page.locator("#result-tag").inner_text() == "等待输入"
                            assert not page_errors and not console_errors, (page_errors, console_errors)
                            for period, count in ((5, 132), (15, 44), (30, 22), (60, 12)):
                                result = page.evaluate("""async period => {
                                    const response = await fetch(`/api/hk-intraday?code=hk00700&period=${period}`);
                                    return {status: response.status, cache: response.headers.get('cache-control'), data: await response.json()};
                                }""", period)
                                assert result["status"] == 200 and result["cache"] == "no-store"
                                assert result["data"]["source"] == "eastmoney_hk" and len(result["data"]["bars"]) == count
                                if period == 60:
                                    ends = [bar["end"][11:16] for bar in result["data"]["bars"][-6:]]
                                    assert ends == ["10:30", "11:30", "13:30", "14:30", "15:30", "16:00"]
                                    assert result["data"]["bars"][-1]["trading_minutes"] == 30
                            assert any("/api/hk-intraday" in url and status == 200 for url, status in responses)
                            response = page.goto(app + "/api/hk-intraday?code=hk00700&period=60", wait_until="domcontentloaded")
                            assert response.status == 200 and page.url.endswith("code=hk00700&period=60")
                            assert '"period_minutes":60' in page.locator("body").inner_text()
                            response = page.goto(gateway_url + "/calculator", wait_until="domcontentloaded")
                            assert response.status == 401 and page.title() == "Authentication Required"
                            assert page.evaluate("async () => (await fetch('/api/hk-intraday?code=hk00700&period=5')).status") == 401
                            context.add_cookies([{"name": "hk_test_role", "value": "denied", "url": gateway_url}])
                            response = page.goto(gateway_url + "/calculator", wait_until="domcontentloaded")
                            assert response.status == 403 and page.title() == "Forbidden"
                            assert page.evaluate("async () => (await fetch('/api/hk-intraday?code=hk00700&period=5')).status") == 403
                            for role in ("viewer", "administrator"):
                                context.add_cookies([{"name": "hk_test_role", "value": role, "url": gateway_url}])
                                response = page.goto(gateway_url + "/calculator", wait_until="networkidle")
                                assert response.status == 200 and "仓位计算" in page.locator("h1").inner_text()
                                result = page.evaluate("""async () => {
                                    const good = await fetch('/api/hk-intraday?code=hk00700&period=5');
                                    const bad = await fetch('/api/hk-intraday?code=hk00700&period=1');
                                    const missing = await fetch('/api/hk-intraday?code=hk00005&period=5');
                                    const write = await fetch('/api/hk-intraday?code=hk00700&period=5', {method:'POST'});
                                    return [good.status,bad.status,missing.status,write.status];
                                }""")
                                assert result == [200, 400, 404, 405]
                            Gateway.api_unavailable = True
                            assert page.evaluate("async () => (await fetch('/api/hk-intraday?code=hk00700&period=5')).status") == 503
                            Gateway.api_unavailable = False
                            assert page.evaluate("async () => (await fetch('/api/hk-intraday?code=hk00700&period=5')).status") == 200
                            assert not page_errors, page_errors
                            unexpected_console = [message for message in console_errors if not any(f"status of {status}" in message for status in (400, 401, 403, 404, 405, 503))]
                            assert not unexpected_console, unexpected_console
                            context.close()
                            browser.close()
                        gateway.shutdown()
                finally:
                    if args.image:
                        subprocess.run(["docker", "stop", "--time", "10", container_name], capture_output=True, timeout=15)
                    else:
                        process.terminate()
                    process.wait(timeout=20)
                assert process.returncode == 0, (root / "service.log").read_text()
            print(json.dumps({"environment": args.image or "native", "result": "passed", "checks": "runtime daily sync, upstream 403 and recovery, schema migration, backfill, gaps, read API, browser, mock gateway, SIGTERM"}))
        finally:
            provider.shutdown()


if __name__ == "__main__":
    main()
