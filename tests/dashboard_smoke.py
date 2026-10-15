#!/usr/bin/env python3
"""Exercise the embedded dashboard in one real browser against the real CLI.

Requires Python Playwright and Chromium. No live prices, credentials, or persistent
test service are used. Pass --image IMAGE to exercise the published container.
"""

import argparse
from contextlib import contextmanager
import glob
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from urllib.parse import urlparse

from playwright.sync_api import expect, sync_playwright

# Test helpers should not leave generated files in the checkout.
sys.dont_write_bytecode = True
from calculator_browser_checks import calculator_checks


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


def fixture(directory, empty):
    (directory / "data").mkdir()
    (directory / "logs").mkdir()
    (directory / "leaders.yaml").write_text(POOL)
    config = (ROOT / "config.yaml.example").read_text()
    config = config.replace("require_cifs: true", "require_cifs: false")
    config = config.replace("request_timeout: 10s", "request_timeout: 1s")
    config = config.replace("poll_interval: 5m", "poll_interval: 1h")
    config = config.replace("valid_from: '2026-01-01'", "valid_from: '2020-01-01'")
    config = config.replace("valid_through: '2026-12-31'", "valid_through: '2099-12-31'")
    (directory / "config.yaml").write_text(config)
    args = ["go", "run", "./tests/fixtures/dashboard", "-data", str(directory / "data")]
    if empty:
        args.append("-empty")
    run(args, cwd=ROOT, capture_output=True, text=True)


@contextmanager
def service(args, empty=False):
    with tempfile.TemporaryDirectory(prefix="stock-watcher-dashboard-") as temporary:
        directory = Path(temporary)
        fixture(directory, empty)
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
            page.on("requestfailed", lambda request: problems.append(f"network: {request.url} {request.failure}") if not state["injecting"] else None)
            with service(args) as (url, directory, env):
                populated_checks(context, page, url, args, directory, env)
                calculator_checks(context, page, url, ROOT, args, state, stalled_endpoint)
                error_checks(page, url, state)
                page.goto("about:blank")
            with service(args, empty=True) as (url, _, _):
                payload = wait_ready(context, url)
                assert payload["report"] is None and payload["summary"]["available"] == 0
                page.goto(url, wait_until="networkidle")
                expect(page.get_by_test_id("empty-state")).to_be_visible()
                expect(page.get_by_test_id("stock-row")).to_have_count(3)
                expect(page.get_by_test_id("ranking-empty-state")).to_be_visible()
                page.get_by_role("link", name="仓位计算", exact=True).click()
                expect(page.get_by_role("button", name="计算仓位", exact=True)).to_be_enabled()
                page.get_by_role("button", name="载入 215 股示例").click()
                page.get_by_role("button", name="计算仓位", exact=True).click()
                expect(page.locator('[data-result="target_price"]')).to_have_text("48.61 元")
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
                      "checks": "snapshot/pool/ranking/history/follow-latest/search/filter/sort/mobile/assets/read-only API/gateway-403/503/timeout/recovery/empty/calculator/initial/add/holdings/net-target/validation/offline/no-account-upload/model-load-failure/SIGTERM"}, ensure_ascii=False))


if __name__ == "__main__":
    main()
