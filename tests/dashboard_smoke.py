#!/usr/bin/env python3
"""Exercise the embedded dashboard in one real browser against the real CLI.

Requires Python Playwright and Chromium. No live prices, credentials, or persistent
test service are used. Pass --image IMAGE to exercise the published container.
"""

import argparse
from contextlib import contextmanager
from datetime import datetime, timedelta
import glob
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import urlparse
from zoneinfo import ZoneInfo

from playwright.sync_api import expect, sync_playwright

# Test helpers should not leave generated files in the checkout.
sys.dont_write_bytecode = True
from calculator_browser_checks import calculator_checks
from experiment_browser_checks import experiment_checks


ROOT = Path(__file__).resolve().parents[1]
LATEST = "2026-10-13"
PREVIOUS = "2026-10-12"
POOL = """schema_version: 1
stocks:
  - code: sz000001
    name: 测试银行
    sector: 银行
  - code: sh600519
    name: 测试消费
    sector: 消费
  - code: sz000002
    name: 测试缺失
    sector: 地产
"""


def run(command, **kwargs):
    return subprocess.run(command, check=True, timeout=60, **kwargs)


def available_port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def fixture(directory, empty, partial=False, gates=False):
    (directory / "data").mkdir()
    (directory / "logs").mkdir()
    (directory / "leaders.yaml").write_text(POOL)
    config = (ROOT / "config.yaml.example").read_text()
    config = config.replace("require_cifs: true", "require_cifs: false")
    config = config.replace("intraday:\n  enabled: true", "intraday:\n  enabled: false")
    config = config.replace("request_timeout: 10s", "request_timeout: 1s")
    # All other upstreams must remain disabled: BaoStock uses TCP and bypasses HTTPS_PROXY.
    config = config.replace("providers: [tencent, sina, baostock, tushare, eastmoney]", "providers: [tencent]")
    config = config.replace("poll_interval: 5m", "poll_interval: 1h")
    # Freeze saved-data UI fixtures. Even a failed refresh saves a sync status.
    # A mock calendar with no recent completed session blocks background writes
    # while allowing the real CLI to start. Collector behavior is tested separately.
    today = datetime.now(ZoneInfo("Asia/Shanghai")).date()
    closed = "".join(f"    - '{today - timedelta(days=i)}'\n" for i in range(-1, 40))
    config = re.sub(r"(?ms)^calendar:\n.*?(?=^\S|\Z)",
                    "calendar:\n  valid_from: '2020-01-01'\n  valid_through: '2099-12-31'\n  holidays:\n" + closed, config)
    (directory / "config.yaml").write_text(config)
    args = ["go", "run", "./tests/fixtures/dashboard", "-data", str(directory / "data")]
    if empty:
        args.append("-empty")
    if partial:
        args.append("-partial")
    if gates:
        args.append("-gates")
    run(args, cwd=ROOT, capture_output=True, text=True)


@contextmanager
def service(args, empty=False, partial=False, gates=False):
    with tempfile.TemporaryDirectory(prefix="stock-watcher-dashboard-") as temporary:
        directory = Path(temporary)
        fixture(directory, empty, partial, gates)
        port = available_port()
        url = f"http://127.0.0.1:{port}"
        env = os.environ.copy()
        for key in ("FEISHU_WEBHOOK", "FEISHU_SECRET"):
            env.pop(key, None)
        env.update({
            "STOCK_WATCHER_DATA_DIR": str(directory / "data"),
            "STOCK_WATCHER_LOG_DIR": str(directory / "logs"),
            "STOCK_WATCHER_LOG_COLOR": "off",
            "STOCK_WATCHER_WEB_ADDR": f"127.0.0.1:{port}",
            # Go's HTTP transport sends HTTPS requests through this closed local
            # proxy. Startup synchronization cannot reach a live provider.
            "HTTPS_PROXY": "http://127.0.0.1:9",
            "HTTP_PROXY": "http://127.0.0.1:9",
            "ALL_PROXY": "http://127.0.0.1:9",
            "NO_PROXY": "127.0.0.1,localhost",
        })
        name = f"stock-watcher-dashboard-{os.getpid()}-{'empty' if empty else 'data'}"
        process = None
        started = False
        log = (directory / "process.log").open("w+")
        try:
            if args.image:
                command = [
                    "docker", "run", "--detach", "--name", name, "--init", "--read-only",
                    "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
                    "--cpus", "1", "--memory", "256m", "--pids-limit", "64",
                    "-p", f"127.0.0.1:{port}:8080",
                    "--mount", f"type=bind,src={directory},dst=/fixture",
                ]
                container_env = {
                    "STOCK_WATCHER_DATA_DIR": "/fixture/data",
                    "STOCK_WATCHER_LOG_DIR": "/fixture/logs",
                    "STOCK_WATCHER_LOG_COLOR": "off",
                    "STOCK_WATCHER_WEB_ADDR": "0.0.0.0:8080",
                    "HTTPS_PROXY": "http://127.0.0.1:9",
                    "HTTP_PROXY": "http://127.0.0.1:9",
                    "NO_PROXY": "127.0.0.1,localhost",
                }
                for key, value in container_env.items():
                    command += ["-e", f"{key}={value}"]
                run(command + [args.image, "run", "-config", "/fixture/config.yaml"], capture_output=True, text=True)
                started = True
            else:
                process = subprocess.Popen(
                    [args.binary, "run", "-config", str(directory / "config.yaml")],
                    env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True,
                )
            yield url, directory, env
        except BaseException:
            if args.image and started:
                result = subprocess.run(["docker", "logs", "--tail", "30", name], capture_output=True, text=True, timeout=15)
                print(result.stdout + result.stderr)
            else:
                log.flush()
                log.seek(0)
                print(log.read()[-6000:])
            raise
        finally:
            if args.image and started:
                run(["docker", "stop", "--time", "10", name], capture_output=True, text=True)
                try:
                    result = run(["docker", "inspect", "--format", "{{.State.ExitCode}}", name], capture_output=True, text=True)
                    assert result.stdout.strip() == "0", "container did not stop gracefully"
                finally:
                    run(["docker", "rm", "-f", name], capture_output=True, text=True)
            if process:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
                    raise AssertionError("service ignored SIGTERM")
                assert process.returncode == 0, f"service exit {process.returncode}"
            log.close()


def wait_ready(context, url):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        try:
            response = context.request.get(url + "/api/dashboard", timeout=1000)
            if response.status == 200:
                return response.json()
        except Exception:
            pass
        time.sleep(0.15)
    raise AssertionError("dashboard did not become ready in 20s")


def stock_codes(page):
    return page.get_by_test_id("stock-row").evaluate_all("rows => rows.map(row => row.dataset.code)")


@contextmanager
def stalled_endpoint():
    release = threading.Event()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            release.wait(15)
            try:
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Access-Control-Allow-Origin", "*")
                self.end_headers()
                self.wfile.write(b"{}")
            except (BrokenPipeError, ConnectionResetError):
                pass  # The browser's AbortController already closed the socket.

        def log_message(self, *_):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}/stalled"
    finally:
        release.set()
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)


def populated_checks(context, page, url, args, directory, env):
    payload = wait_ready(context, url)
    assert payload["schema_version"] == 1
    assert payload["as_of"] == LATEST
    assert payload["summary"]["pool_size"] == 3
    assert payload["summary"]["available"] == 2
    assert payload["summary"]["missing"] == 1
    assert payload["summary"]["selected"] == 1
    assert payload["dates"] == [LATEST, PREVIOUS]
    assert len(payload["report"]["ranking"]) == 2
    assert {row["code"] for row in payload["stocks"]} == {"sz000001", "sz000002", "sh600519"}
    missing = next(row for row in payload["stocks"] if row["code"] == "sz000002")
    assert missing["status"] == "missing" and missing["bar"] is None
    assert context.request.post(url + "/api/dashboard").status == 405
    assert context.request.get(url + "/api/reports?as_of=invalid").status == 400
    assert context.request.get(url + "/api/reports?as_of=2026-09-01").status == 404
    response = page.goto(url, wait_until="networkidle")
    assert response.status == 200
    assert page.url.rstrip("/") == url
    assert "股池观察" in page.title(), "unexpected browser title"
    expect(page.get_by_test_id("dashboard-title")).to_be_visible()
    expect(page.get_by_test_id("runtime-status")).to_contain_text("运行状态不可用")
    status_response = context.request.get(url + "/api/status")
    assert status_response.status == 200 and status_response.json()["schema_version"] == 1
    assert not status_response.json()["ready"]
    assert context.request.get(url + "/readyz").status == 503
    # The fixture deliberately closes 40 sessions to prevent background writes.
    # Exercise successful business status rendering with an explicit HTTP mock.
    healthy = {"schema_version": 1, "ready": True, "business": "syncing", "issues": [],
               "expected_date": LATEST, "available": 2, "pool_size": 3,
               "minute_enabled": False, "task": {"phase": "analyzing", "completed": 3,
               "total": 3, "updated_at": "2026-10-13T19:00:00+08:00"}}
    page.route("**/api/status", lambda route: route.fulfill(json=healthy))
    page.get_by_test_id("refresh-button").click()
    expect(page.get_by_test_id("runtime-status")).to_contain_text("分析 3/3")
    page.unroute("**/api/status")
    expect(page.get_by_test_id("snapshot-summary")).to_contain_text("3")
    expect(page.get_by_test_id("stock-row")).to_have_count(3)
    expect(page.get_by_test_id("ranking-row")).to_have_count(2)
    expect(page.get_by_test_id("ranking-row").filter(has_text="测试银行")).to_contain_text("推荐")
    expect(page.get_by_test_id("ranking-row").filter(has_text="测试消费")).to_contain_text("观察")
    expect(page.get_by_test_id("stock-row").filter(has_text="测试缺失")).to_contain_text("缺")
    expect(page.get_by_test_id("stock-row").filter(has_text="测试银行")).to_contain_text("10.25")

    # Confirm actual asset bytes match this checkout and verify cache policy,
    # rather than treating the HTML 200 as a UI assertion.
    assets = page.locator('script[src], link[rel="stylesheet"][href]').evaluate_all(
        "nodes => nodes.map(node => node.src || node.href)"
    )
    assert len(assets) >= 2
    for asset in assets:
        assert asset.startswith(url + "/"), f"unexpected external asset: {asset}"
        actual = context.request.get(asset)
        assert actual.status == 200 and len(actual.body()) > 100
        assert actual.headers.get("cache-control"), "asset cache policy missing"
        source = ROOT / "internal/dashboard/static" / Path(urlparse(asset).path).name
        assert hashlib.sha256(actual.body()).digest() == hashlib.sha256(source.read_bytes()).digest(), f"stale embedded asset: {asset}"
    actual = context.request.get(url + "/api/dashboard")
    assert "no-store" in actual.headers.get("cache-control", "")

    search = page.get_by_test_id("stock-search")
    search.fill("银行")
    expect(page.get_by_test_id("stock-row")).to_have_count(1)
    assert stock_codes(page) == ["sz000001"]
    search.fill("NO_MATCH")
    expect(page.get_by_test_id("stock-empty-state")).to_be_visible()
    expect(page.get_by_test_id("stock-row")).to_have_count(0)
    search.fill("")
    page.get_by_test_id("pool-filter").select_option(label="消费")
    expect(page.get_by_test_id("stock-row")).to_have_count(1)
    assert stock_codes(page) == ["sh600519"]
    page.get_by_test_id("pool-filter").select_option(index=0)
    page.locator("#view-synced").click()
    assert page.url == url + "/#stock-pool"
    expect(page.locator("#data-filter")).to_have_value("available")
    expect(page.get_by_test_id("stock-row")).to_have_count(2)
    assert "sz000002" not in stock_codes(page)
    search.fill("银行")
    assert stock_codes(page) == ["sz000001"]
    page.locator("#data-filter").select_option("missing")
    expect(page.get_by_test_id("stock-empty-state")).to_be_visible()
    page.locator("#clear-filters").click()
    expect(page.locator("#data-filter")).to_have_value("")
    expect(search).to_have_value("")
    expect(page.get_by_test_id("stock-row")).to_have_count(3)
    page.locator("#data-filter").select_option("missing")
    assert stock_codes(page) == ["sz000002"]
    page.locator("#data-filter").select_option("")
    page.get_by_test_id("stock-sort").select_option("close")
    expect(page.get_by_test_id("stock-row")).to_have_count(3)
    before = stock_codes(page)
    page.get_by_test_id("sort-direction").click()
    after = stock_codes(page)
    assert before != after, "sort direction did not change stock order"
    assert before.index("sz000001") < before.index("sh600519") or after.index("sz000001") < after.index("sh600519")
    assert before[-1] == after[-1] == "sz000002", "missing price must remain last"

    page.get_by_test_id("report-date-select").select_option(PREVIOUS)
    expect(page.get_by_test_id("ranking-row")).to_have_count(1)
    expect(page.get_by_test_id("ranking-row")).to_contain_text("测试消费")
    # Historical ranking selection must not silently replace the latest pool snapshot.
    expect(page.get_by_test_id("stock-row").filter(has_text="测试银行")).to_contain_text("10.25")
    page.get_by_test_id("report-date-select").select_option(LATEST)
    expect(page.get_by_test_id("ranking-row")).to_have_count(2)
    screenshots = ROOT / ".scratch" / "dashboard-verification"
    screenshots.mkdir(parents=True, exist_ok=True)
    kind = "container" if args.image else "native"
    page.screenshot(path=str(screenshots / f"{kind}-desktop.png"), full_page=True)
    page.set_viewport_size({"width": 390, "height": 844})
    expect(page.get_by_test_id("dashboard-title")).to_be_visible()
    expect(page.get_by_test_id("stock-search")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), "mobile page overflows viewport"
    page.screenshot(path=str(screenshots / f"{kind}-mobile.png"), full_page=True)
    page.set_viewport_size({"width": 1440, "height": 1000})

    if not args.image:
        missing_config = subprocess.run(
            [args.binary, "run", "-config", str(directory / "absent.yaml")],
            env=env, capture_output=True, text=True, timeout=10,
        )
        assert missing_config.returncode == 1, "missing config must fail before serving"


def chart_checks(page, url, state, args):
    history = page.context.request.get(url + "/api/history?code=sz000001").json()
    assert len(history["bars"]) == 80 and history["adjust"] == "qfq"
    expect(page.locator("#chart-stock")).to_have_value("sz000001")
    expect(page.locator("#chart-meta")).to_contain_text("腾讯")
    expect(page.locator("#chart-meta")).to_contain_text("成交量单位：手")
    snapshot = page.context.request.get(url + "/api/dashboard").json()
    for stock in snapshot["stocks"]:
        if stock["bar"]:
            scale = 1 if stock["source"] == "baostock" else 100
            assert stock["bar"]["volume_shares"] == stock["bar"]["volume"] * scale
    expect(page.locator('#stock-body td[title$="（股）"]')).to_have_count(2)
    expect(page.get_by_test_id("kline-candle")).to_have_count(60)
    expect(page.get_by_test_id("kline-volume")).to_have_count(60)
    for period in (5, 10, 20):
        expect(page.get_by_test_id(f"kline-ma{period}")).to_have_count(1)
        expected = sum(bar["close"] for bar in history["bars"][-period:]) / period
        expect(page.locator(f"#chart-ma{period}")).to_contain_text(f"{expected:.2f}")
    # Actual SVG geometry reflects OHLC; fixtures include rising and falling bodies.
    candle = page.get_by_test_id("kline-candle").last
    assert float(candle.get_attribute("height")) > 1
    page.locator("#chart-range").select_option("0")
    expect(page.get_by_test_id("kline-candle")).to_have_count(80)
    page.locator("#chart-day").fill("0")
    expect(page.locator("#chart-detail")).to_contain_text(history["bars"][0]["date"])
    expect(page.locator("#chart-ma20")).to_have_text("MA20 —")
    page.locator("#chart-day").press("ArrowRight")
    expect(page.locator("#chart-detail")).to_contain_text(history["bars"][1]["date"])
    page.locator("#chart-range").select_option("60")
    expect(page.get_by_test_id("kline-candle")).to_have_count(60)
    page.locator("#chart-day").fill("0")
    expect(page.locator("#chart-ma20")).to_have_text("MA20 10.00")
    page.locator("#chart-range").select_option("120")
    expect(page.get_by_test_id("kline-candle")).to_have_count(80)
    page.locator("#chart-range").select_option("60")
    expect(page.locator("#chart-detail")).to_contain_text(LATEST)
    page.get_by_role("link", name="个股 K 线", exact=True).click()
    assert page.url == url + "/#stock-chart"
    raw = page.context.request.get(url + "/api/history?code=sz000001&adjust=none").json()
    assert raw["adjust"] == "none" and len(raw["bars"]) == 2700 and raw["bars"][0]["date"] < "2016-10-13"
    assert raw["bars"][-1]["close"] != history["bars"][-1]["close"]
    with page.expect_response(lambda r: "/api/history?code=sz000001&adjust=none" in r.url and r.status == 200):
        page.locator("#chart-adjust").select_option("none")
    expect(page.locator("#chart-detail")).to_contain_text("20.50")
    expect(page.locator("#chart-meta")).to_contain_text(raw["bars"][0]["date"])
    expect(page.locator("#chart-meta")).to_contain_text("2700 根已保存日线")
    expect(page.locator("#chart-basis-note")).to_contain_text("除权除息")
    page.locator("#chart-range").select_option("250")
    expect(page.get_by_test_id("kline-candle")).to_have_count(250)
    page.locator("#chart-range").select_option("0")
    expect(page.get_by_test_id("kline-candle")).to_have_count(2700)
    assert page.get_by_test_id("kline-candle").last.evaluate("e => e.getBoundingClientRect().width") > 4
    assert page.locator("#chart-plot").evaluate("e => e.scrollLeft > 0")
    kind = "container" if args.image else "native"
    page.locator("#stock-chart").screenshot(path=str(ROOT / ".scratch/dashboard-verification" / f"{kind}-raw-history.png"))
    page.locator("#chart-day").focus()
    page.locator("#chart-day").fill("0")
    expect(page.locator("#chart-detail")).to_contain_text(raw["bars"][0]["date"])
    assert page.locator("#chart-plot").evaluate("e => e.scrollLeft === 0")
    page.set_viewport_size({"width": 390, "height": 844})
    expect(page.locator("#chart-adjust")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), "long archive overflows mobile page"
    page.locator("#stock-chart").screenshot(path=str(ROOT / ".scratch/dashboard-verification" / f"{kind}-raw-mobile.png"))
    page.set_viewport_size({"width": 1440, "height": 1000})
    page.get_by_test_id("refresh-button").click()
    expect(page.locator("#chart-adjust")).to_have_value("none")
    expect(page.get_by_test_id("kline-candle")).to_have_count(2700)
    page.locator("#chart-adjust").select_option("qfq")
    expect(page.get_by_test_id("kline-candle")).to_have_count(80)
    expect(page.locator("#chart-detail")).to_contain_text("10.25")
    page.locator("#chart-range").select_option("60")
    page.get_by_test_id("stock-row").filter(has_text="测试消费").get_by_role("button").click()
    expect(page.locator("#chart-stock")).to_have_value("sh600519")
    expect(page.locator("#chart-meta")).to_contain_text("BaoStock")
    expect(page.locator("#chart-meta")).to_contain_text("成交量单位：股")
    expect(page.locator("#chart-detail")).to_contain_text("198.00")
    expect(page.locator("#chart-plot .chart-fall .chart-candle")).to_have_count(60)
    for source, label in (("tushare", "Tushare"), ("eastmoney", "东方财富"), ("sina", "新浪")):
        page.route("**/api/history?code=sz000001", lambda route, *, source=source: route.fulfill(
            status=200, content_type="application/json", body=json.dumps({**history, "source": source})))
        page.locator("#chart-stock").select_option("sz000001")
        expect(page.locator("#chart-meta")).to_contain_text(label)
        expect(page.locator("#chart-meta")).to_contain_text("成交量单位：股" if source == "sina" else "成交量单位：手")
        expect(page.get_by_test_id("kline-candle")).to_have_count(60)
        page.unroute("**/api/history?code=sz000001")
        page.locator("#chart-stock").select_option("sh600519")
        expect(page.locator("#chart-meta")).to_contain_text("BaoStock")
    page.locator("#chart-stock").select_option("sz000002")
    expect(page.locator("#chart-error")).to_contain_text("暂无通过校验")
    expect(page.get_by_test_id("kline-candle")).to_have_count(0)
    expect(page.locator("#chart-body")).to_be_hidden()
    assert page.context.request.get(url + "/api/history?code=sz000002").status == 404
    assert page.context.request.get(url + "/api/history?code=sz999999").status == 404
    assert page.context.request.get(url + "/api/history?code=sz000001&code=sh600519").status == 400
    assert page.context.request.post(url + "/api/history?code=sz000001").status == 405
    state["injecting"] = True
    pattern = "**/api/history?*"
    for status, message in ((403, "授权"), (503, "暂时不可用")):
        page.route(pattern, lambda route, *, status=status: route.fulfill(status=status, content_type="application/json", body='{"error":"market data is temporarily unavailable"}'))
        page.locator("#chart-stock").select_option("sz000001")
        expect(page.locator("#chart-error")).to_contain_text(message)
        expect(page.get_by_test_id("kline-candle")).to_have_count(0)
        page.unroute(pattern)
        page.locator("#chart-retry").click()
        expect(page.get_by_test_id("kline-candle")).to_have_count(60)
        page.locator("#chart-stock").select_option("sz000002")
    with stalled_endpoint() as stalled_url:
        page.route(pattern, lambda route: route.continue_(url=stalled_url))
        page.locator("#chart-stock").select_option("sz000001")
        expect(page.locator("#chart-loading")).to_be_visible()
        expect(page.locator("#chart-error")).to_contain_text("超时", timeout=15000)
        expect(page.get_by_test_id("kline-candle")).to_have_count(0)
        page.unroute(pattern)
    page.locator("#chart-retry").click()
    expect(page.get_by_test_id("kline-candle")).to_have_count(60)
    # Abort an in-flight selection; a late response cannot repaint a different stock.
    with stalled_endpoint() as stalled_url:
        delayed = "**/api/history?code=sh600519"
        page.route(delayed, lambda route: route.continue_(url=stalled_url))
        page.locator("#chart-stock").select_option("sh600519")
        expect(page.locator("#chart-loading")).to_be_visible()
        page.locator("#chart-stock").select_option("sz000001")
        expect(page.locator("#chart-detail")).to_contain_text("10.25")
        page.unroute(delayed)
    raw_pattern = "**/api/history?code=sz000001&adjust=none"
    for status, payload, message in ((404, {"error": "history not found"}, "暂无通过校验"), (200, history, "日线数据不完整")):
        page.route(raw_pattern, lambda route, *, status=status, payload=payload: route.fulfill(status=status, content_type="application/json", body=json.dumps(payload)))
        page.locator("#chart-adjust").select_option("none")
        expect(page.locator("#chart-error")).to_contain_text(message)
        expect(page.get_by_test_id("kline-candle")).to_have_count(0)
        page.unroute(raw_pattern)
        page.locator("#chart-adjust").select_option("qfq")
        expect(page.locator("#chart-detail")).to_contain_text("10.25")
    expect(page.locator("#chart-title")).to_contain_text("测试银行")
    # Changing price basis cancels an older response for the same stock too.
    with stalled_endpoint() as stalled_url:
        delayed = "**/api/history?code=sz000001&adjust=none"
        page.route(delayed, lambda route: route.continue_(url=stalled_url))
        page.locator("#chart-adjust").select_option("none")
        expect(page.locator("#chart-loading")).to_be_visible()
        page.locator("#chart-adjust").select_option("qfq")
        expect(page.locator("#chart-detail")).to_contain_text("10.25")
        page.unroute(delayed)
    page.route(pattern, lambda route: route.abort())
    page.locator("#chart-stock").select_option("sh600519")
    expect(page.locator("#chart-error")).to_be_visible()
    page.unroute(pattern)
    page.locator("#chart-stock").select_option("sz000001")
    expect(page.locator("#chart-detail")).to_contain_text("10.25")
    page.wait_for_load_state("networkidle")
    state["injecting"] = False


def error_checks(page, url, state):
    pattern = "**/api/dashboard"
    fresh = page.context.request.get(url + "/api/dashboard").json()
    fresh["as_of"] = "2026-10-14"
    fresh["report"]["as_of"] = "2026-10-14"
    fresh["report"]["target_trading_date"] = "2026-10-15"
    fresh["dates"].insert(0, "2026-10-14")
    page.route(pattern, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(fresh)))
    page.get_by_test_id("refresh-button").click()
    expect(page.get_by_test_id("report-date-select")).to_have_value("2026-10-14")
    page.get_by_test_id("report-date-select").select_option(PREVIOUS)
    expect(page.get_by_test_id("ranking-row")).to_have_count(1)
    page.get_by_test_id("refresh-button").click()
    expect(page.get_by_test_id("report-date-select")).to_have_value(PREVIOUS)
    expect(page.get_by_test_id("ranking-row")).to_have_count(1)
    page.get_by_test_id("report-date-select").select_option("2026-10-14")
    page.unroute(pattern)
    page.get_by_test_id("refresh-button").click()
    expect(page.get_by_test_id("report-date-select")).to_have_value(LATEST)
    expect(page.get_by_test_id("ranking-row")).to_have_count(2)

    state["injecting"] = True
    for status in (403, 503):
        # 403 models a gateway denying access; the app has no login or roles.
        page.route(pattern, lambda route: route.fulfill(status=status, content_type="application/json", body='{"error":"test_unavailable"}'))
        page.get_by_test_id("refresh-button").click()
        expect(page.get_by_test_id("error-state")).to_be_visible(timeout=5000)
        if status == 403:
            expect(page.get_by_test_id("error-state")).to_contain_text("授权")
        expect(page.get_by_test_id("stock-row")).to_have_count(3)
        page.unroute(pattern)
        page.get_by_test_id("retry-button").click()
        expect(page.get_by_test_id("error-state")).to_be_hidden()
        expect(page.get_by_test_id("stock-row")).to_have_count(3)

    # A real HTTP connection stalls until the browser's own timeout fires.
    with stalled_endpoint() as stalled_url:
        page.route(pattern, lambda route: route.continue_(url=stalled_url))
        page.get_by_test_id("refresh-button").click()
        expect(page.get_by_test_id("error-state")).to_be_visible(timeout=15000)
        expect(page.get_by_test_id("error-state")).to_contain_text("超时")
        expect(page.get_by_test_id("stock-row")).to_have_count(3)
        page.unroute(pattern)
    page.get_by_test_id("retry-button").click()
    expect(page.get_by_test_id("error-state")).to_be_hidden()

    history_pattern = "**/api/reports?*"
    page.route(history_pattern, lambda route: route.fulfill(status=503, content_type="application/json", body='{"error":"test_unavailable"}'))
    page.get_by_test_id("report-date-select").select_option(PREVIOUS)
    expect(page.get_by_test_id("report-error-state")).to_be_visible()
    page.unroute(history_pattern)
    page.get_by_test_id("report-retry-button").click()
    expect(page.get_by_test_id("report-error-state")).to_be_hidden()
    expect(page.get_by_test_id("ranking-row")).to_have_count(1)
    page.wait_for_load_state("networkidle")
    state["injecting"] = False


def minute_checks(page, url, state, args):
    # The preceding chart checks hover historical candles. A DOM redraw under
    # that pointer can select an old bar before the latest-bar assertion.
    page.mouse.move(0, 0)
    page.locator("#chart-stock").select_option("sz000001")
    page.locator("#chart-range").select_option("0")
    assert page.locator('#chart-period option[value="1"]').count() == 0
    for period in (5, 15, 30, 60):
        data = page.context.request.get(f"{url}/api/intraday?code=sz000001&period={period}").json()
        assert data["base_period_minutes"] == 5 and data["adjust"] == "none"
        assert data["sync"]["calibrated_date"] == "2026-10-13"
        assert data["bars"][-1]["final"] is False
        with page.expect_response(lambda r: f"/api/intraday?code=sz000001&period={period}" in r.url and r.status == 200):
            page.locator("#chart-period").select_option(str(period))
        expect(page.get_by_test_id("kline-candle")).to_have_count(len(data["bars"]))
        expect(page.get_by_test_id("kline-volume")).to_have_count(len(data["bars"]))
        expect(page.locator("#chart-title")).to_contain_text(f"{period} 分钟 K")
        expect(page.locator("#chart-adjust")).to_be_disabled()
        expect(page.locator("#chart-adjust")).to_have_value("none")
        expect(page.locator("#chart-detail")).to_contain_text("形成中")
        expect(page.locator("#chart-plot .chart-forming")).to_have_count(1)
        expect(page.locator("#chart-sync-note")).to_contain_text("2026-10-13")
        expect(page.locator("#chart-meta")).to_contain_text("不复权")
        for n in (5, 10, 20):
            average = sum(b["close"] for b in data["bars"][-n:]) / n
            shown = float(page.locator(f"#chart-ma{n}").inner_text().split()[1])
            assert abs(shown - average) <= 0.005001, (period, n, shown, average)
    kind = "container" if args.image else "native"
    page.locator("#stock-chart").screenshot(path=str(ROOT / ".scratch/dashboard-verification" / f"{kind}-minutes.png"))
    page.set_viewport_size({"width":390,"height":844})
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1")
    expect(page.locator("#chart-period")).to_be_visible()
    page.set_viewport_size({"width":1440,"height":1000})
    # Minute history is visible even when the stock has no usable daily data.
    page.locator("#chart-stock").select_option("sz000002")
    expect(page.locator("#chart-detail")).to_contain_text("11.50")
    expect(page.locator("#chart-error")).to_be_hidden()
    state["injecting"] = True
    page.locator("#chart-stock").select_option("sh600519")
    expect(page.locator("#chart-error")).to_contain_text("暂无分钟数据")
    expect(page.get_by_test_id("kline-candle")).to_have_count(0)
    pattern="**/api/intraday?*"
    for sync_status, message in (("failed", "同步失败"), ("stale", "延迟"), ("partial", "缺口")):
        payload = {**data, "sync": {**data["sync"], "status": sync_status}}
        page.route(pattern, lambda route, *, payload=payload: route.fulfill(status=200,content_type="application/json",body=json.dumps(payload)))
        page.locator("#chart-stock").select_option("sz000001")
        expect(page.locator("#chart-sync-note")).to_contain_text(message)
        expect(page.get_by_test_id("kline-candle")).to_have_count(len(data["bars"]))
        page.unroute(pattern)
    for payload, message in (({**data,"bars":[]},"暂无该周期"),({**data,"period_minutes":1},"分钟数据不完整")):
        page.route(pattern, lambda route, *, payload=payload: route.fulfill(status=200,content_type="application/json",body=json.dumps(payload)))
        page.locator("#chart-stock").select_option("sz000001")
        expect(page.locator("#chart-error")).to_contain_text(message)
        expect(page.get_by_test_id("kline-candle")).to_have_count(0)
        page.unroute(pattern)
    for status, message in ((403,"授权"),(503,"暂时不可用")):
        page.route(pattern,lambda route,*,status=status:route.fulfill(status=status,content_type="application/json",body='{"error":"market data is temporarily unavailable"}'))
        page.locator("#chart-stock").select_option("sz000001")
        expect(page.locator("#chart-error")).to_contain_text(message)
        expect(page.get_by_test_id("kline-candle")).to_have_count(0)
        page.unroute(pattern)
        page.locator("#chart-retry").click()
        expect(page.locator("#chart-detail")).to_contain_text("11.50")
    with stalled_endpoint() as stalled_url:
        page.route(pattern,lambda route:route.continue_(url=stalled_url))
        page.locator("#chart-period").select_option("5")
        expect(page.locator("#chart-error")).to_contain_text("超时",timeout=15000)
        expect(page.get_by_test_id("kline-candle")).to_have_count(0)
        page.unroute(pattern)
    page.locator("#chart-retry").click()
    expect(page.locator("#chart-detail")).to_contain_text("11.50")
    with stalled_endpoint() as stalled_url:
        page.route("**/api/intraday?*period=15",lambda route:route.continue_(url=stalled_url))
        page.locator("#chart-period").select_option("15")
        expect(page.locator("#chart-loading")).to_be_visible()
        page.locator("#chart-period").select_option("day")
        expect(page.locator("#chart-detail")).to_contain_text("10.25")
        page.unroute("**/api/intraday?*period=15")
    expect(page.locator("#chart-adjust")).to_be_enabled()
    expect(page.locator("#chart-adjust")).to_have_value("qfq")
    for query, status in (("code=sz000001&period=1",400),("code=sz000001&period=05",400),("code=sz000001&period=5&adjust=qfq",400),("code=sh600000&period=5",404)):
        assert page.context.request.get(url+"/api/intraday?"+query).status==status
    assert page.context.request.post(url+"/api/intraday?code=sz000001&period=5").status==405
    state["injecting"] = False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", nargs="?", default=str(ROOT / "bin" / "stock-watcher"))
    parser.add_argument("--image", help="test a built runtime image instead of the native binary")
    parser.add_argument("--browser", default=os.environ.get("PLAYWRIGHT_CHROMIUM_EXECUTABLE"))
    args = parser.parse_args()
    args.binary = str(Path(args.binary).resolve())
    browser_path = args.browser
    if not browser_path:
        candidates = sorted(glob.glob(str(Path.home() / ".cache/ms-playwright/chromium-*/chrome-linux64/chrome")))
        browser_path = candidates[-1] if candidates else None
    problems = []
    state = {"injecting": False}
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True, executable_path=browser_path, args=["--no-sandbox"])
        try:
            context = browser.new_context(viewport={"width": 1440, "height": 1000})
            context.set_default_timeout(6000)
            page = context.new_page()
            page.on("pageerror", lambda error: problems.append(f"pageerror: {error}"))
            page.on("console", lambda message: problems.append(f"console: {message.text}") if message.type == "error" and not state["injecting"] else None)
            # Switching stock/report intentionally cancels the preceding fetch.
            # DOM assertions below verify the selected result wins the race.
            def network_failure(request):
                canceled_selection = request.failure == "net::ERR_ABORTED" and urlparse(request.url).path in ("/api/history", "/api/reports", "/api/experiments", "/api/experiment-history", "/api/intraday")
                if not state["injecting"] and not canceled_selection:
                    problems.append(f"network: {request.url} {request.failure}")
            page.on("requestfailed", network_failure)
            with service(args) as (url, directory, env):
                populated_checks(context, page, url, args, directory, env)
                chart_checks(page, url, state, args)
                minute_checks(page, url, state, args)
                experiment_checks(context, page, url, ROOT, args, state, stalled_endpoint)
                calculator_checks(context, page, url, ROOT, args, state, stalled_endpoint)
                error_checks(page, url, state)
                page.goto("about:blank")
            with service(args, gates=True) as (url, _, _):
                payload = wait_ready(context, url)
                saved = context.request.get(url + "/api/reports?as_of=" + LATEST).json()
                diagnosis = saved["diagnostics"]
                assert diagnosis == payload["report"]["diagnostics"]
                assert diagnosis["schema_version"] == 1
                assert diagnosis["validation"]["status"] == "no_recommendations"
                assert not saved["selected"] and not any(row["recommended"] for row in saved["ranking"])
                page.goto(url, wait_until="networkidle")
                assert page.url.rstrip("/") == url and "股池观察" in page.title()
                panel = page.locator("#report-diagnostics")
                expect(panel).to_contain_text("无合格推荐样本")
                expect(panel).to_contain_text("入场样本 0 / 要求 ≥30")
                expect(panel).to_contain_text("不同信号日 0 / 要求 ≥10")
                expect(panel).not_to_contain_text("达标率0.00%")
                failed = page.get_by_test_id("ranking-row").filter(has_text="测试银行")
                expect(failed).to_contain_text("历史单笔净亏损超限")
                failed.locator("summary").click()
                expect(failed.locator("details[open]")).to_contain_text("209笔")
                expect(failed.locator("details[open]")).to_contain_text("-15.38%")
                expect(failed.locator("details[open]")).to_contain_text("要求每笔净收益严格高于 -3.00%")
                expect(page.get_by_test_id("ranking-row").filter(has_text="测试消费")).to_contain_text("未触发买入规则")
                expect(page.locator("#ranking-body")).not_to_contain_text("historical_net_loss_limit_breached")
                expect(page.locator("#ranking-body")).not_to_contain_text("历史证据不足")
                kind = "container" if args.image else "native"
                panel.screenshot(path=str(ROOT / ".scratch/dashboard-verification" / f"{kind}-gate-diagnostics.png"))
                page.set_viewport_size({"width": 390, "height": 844})
                expect(panel).to_be_visible()
                assert page.evaluate("document.documentElement.scrollWidth <= innerWidth"), "diagnostics overflow mobile viewport"
                page.locator("#report-date-select").select_option(PREVIOUS)
                expect(panel).to_be_hidden()
                page.locator("#report-date-select").select_option(LATEST)
                expect(panel).to_contain_text("无合格推荐样本")
                page.set_viewport_size({"width": 1440, "height": 1000})
                page.goto("about:blank")
            with service(args, empty=True) as (url, _, _):
                payload = wait_ready(context, url)
                assert payload["report"] is None and payload["summary"]["available"] == 0
                page.goto(url, wait_until="networkidle")
                expect(page.get_by_test_id("empty-state")).to_be_visible()
                expect(page.get_by_test_id("stock-row")).to_have_count(3)
                expect(page.get_by_test_id("ranking-empty-state")).to_be_visible()
                page.get_by_role("link", name="策略实验室", exact=True).click()
                expect(page.locator("#lab-empty")).to_be_visible()
                expect(page.locator("#lab-workspace")).to_be_hidden()
                page.get_by_role("link", name="仓位计算", exact=True).click()
                expect(page.get_by_role("button", name="计算仓位", exact=True)).to_be_enabled()
                page.get_by_role("button", name="载入 215 股示例").click()
                page.get_by_role("button", name="计算仓位", exact=True).click()
                expect(page.locator('[data-result="target_price"]')).to_have_text("48.61 元")
                page.goto("about:blank")
            with service(args, partial=True) as (url, _, _):
                payload = wait_ready(context, url)
                assert payload["report"] is None and payload["summary"]["available"] == 2
                assert payload["latest_sync"]["status"] == "failed" and not payload["report_current"]
                page.goto(url, wait_until="networkidle")
                expect(page.get_by_test_id("kline-candle")).to_have_count(60)
                expect(page.locator("#market-notice")).to_contain_text("尚无对应推荐报告")
                expect(page.locator("#eligible-count")).to_have_text("—")
                expect(page.get_by_test_id("ranking-row")).to_have_count(0)
                expect(page.get_by_test_id("empty-state")).to_be_hidden()
                expect(page.get_by_test_id("ranking-empty-state")).to_be_visible()
                page.locator("#chart-adjust").select_option("none")
                expect(page.locator("#chart-meta")).to_contain_text("2700 根已保存日线")
                expect(page.locator("#chart-detail")).to_contain_text("20.50")
                page.goto("about:blank")
            assert not problems, "Unexpected browser errors: " + json.dumps(problems, ensure_ascii=False)
            context.close()
        finally:
            browser.close()
    if args.image:
        artifact = run(["docker", "image", "inspect", "--format", "{{.Id}}", args.image], capture_output=True, text=True).stdout.strip()
    else:
        artifact = hashlib.sha256(Path(args.binary).read_bytes()).hexdigest()
    print(json.dumps({"result": "passed", "mode": "container" if args.image else "native", "artifact": artifact,
                      "identity": "anonymous (no application authentication)",
                      "checks": "rule-gate-diagnostics/measured-thresholds/zero-sample-unavailable/legacy-history/mobile/minute-5-15-30-60/forming/calibrated/status/independent-daily/volume-MA/experiments/lineage/comparison/snapshot-evidence/empty/errors/cancel/raw-2700-bars/price-basis-switch/cancel/mismatched-basis/raw-missing/synced-filter/candles/volume/MA/stock-switch/isolation/partial-coverage/snapshot/pool/ranking/history/follow-latest/search/filter/sort/mobile/assets/read-only API/gateway-403/503/timeout/recovery/empty/calculator/initial/add/holdings/net-target/validation/offline/no-account-upload/model-load-failure/SIGTERM"}, ensure_ascii=False))


if __name__ == "__main__":
    main()
