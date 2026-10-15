"""Real DOM/API checks for the experiment archive; no external market data."""
import hashlib
import json
from pathlib import Path
from urllib.parse import urlparse
from playwright.sync_api import expect


def experiment_checks(context, page, url, root, args, state, stalled_endpoint):
    page.goto(url, wait_until="networkidle")
    page.get_by_role("link", name="策略实验室", exact=True).click()
    expect(page).to_have_title("策略实验室 · Stock Watcher")
    assert urlparse(page.url).path == "/experiments"
    expect(page.locator(".experiment-item")).to_have_count(2)
    payload = context.request.get(url + "/api/experiments").json()
    records = payload["experiments"]
    child = next(r for r in records if r.get("parent_id"))
    parent = next(r for r in records if not r.get("parent_id"))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#run-title")).to_have_text("上影线过滤 · 单变量对照")
    assert "id=" + child["id"] in page.url
    expect(page.locator("#run-hypothesis")).to_contain_text("30%")
    expect(page.locator("#comparison-note")).to_contain_text("口径一致")
    expect(page.locator("#comparison-body tr")).to_have_count(11)
    expect(page.locator("#run-holdout")).not_to_have_text("待划分")
    expect(page.locator("#run-next")).not_to_be_empty()
    expect(page.locator('[data-kind="hypothesis"]')).to_contain_text("待验证推断")
    expect(page.locator("#group-body tr").first).to_be_visible()
    page.locator("#group-kind").select_option("stock")
    expect(page.locator("#group-body")).to_contain_text("sh600519")
    page.locator("#trade-filter").select_option("rejected")
    expect(page.locator("#trade-body tr").first).to_contain_text("被本轮过滤")
    page.locator("#trade-body button").first.click()
    expect(page.get_by_test_id("experiment-kline")).to_be_visible()
    expect(page.locator("#evidence-status")).to_contain_text("不复权")
    assert context.request.post(url + "/api/experiments").status == 405
    assert context.request.get(url + "/api/experiments?id=bad").status == 400
    assert context.request.get(url + "/api/experiments?id=" + "f" * 32).status == 404
    assert context.request.get(url + "/api/experiment-history?id=" + child["id"] + "&code=sz999999&date=2026-01-01").status == 400
    # Archived evidence must be frozen even though the dashboard fixture has
    # a different history for the same stock and date.
    detail = context.request.get(url + "/api/experiments?id=" + child["id"]).json()
    assert detail["result"]["development"]["research"]["holdout"]["signal_dates"] == 0
    trade = detail["result"]["development"]["trades"][0]
    evidence = context.request.get(url + f'/api/experiment-history?id={child["id"]}&code={trade["code"]}&date={trade["date"]}').json()
    assert evidence["adjust"] == "none" and evidence["bars"]
    page.locator("#experiment-search").fill("NO_MATCH")
    expect(page.locator("#list-empty")).to_be_visible()
    page.locator("#experiment-search").fill("")
    page.locator("#experiment-filter").select_option("baseline")
    expect(page.locator(".experiment-item")).to_have_count(1)
    page.locator("#experiment-filter").select_option("")
    page.locator("#run-lineage button").first.click()
    expect(page.locator("#run-title")).to_have_text(parent["spec"]["title"])
    expect(page.locator("#run-children button")).to_have_count(1)
    page.locator("#run-children button").click()
    expect(page.locator("#run-title")).to_have_text(child["spec"]["title"])
    page.reload(wait_until="networkidle")
    expect(page.locator("#run-title")).to_have_text(child["spec"]["title"])
    screenshots = root / ".scratch/dashboard-verification"
    kind = "container" if args.image else "native"
    page.screenshot(path=str(screenshots / f"{kind}-experiments.png"), full_page=True)
    page.set_viewport_size({"width": 390, "height": 844})
    expect(page.locator("#run-title")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "experiment page overflow"
    page.screenshot(path=str(screenshots / f"{kind}-experiments-mobile.png"), full_page=True)
    page.set_viewport_size({"width": 1440, "height": 1000})
    for asset in ("experiments.js", "experiments.css"):
        response = context.request.get(url + "/assets/" + asset)
        assert response.status == 200 and response.headers["cache-control"] == "no-cache"
        assert hashlib.sha256(response.body()).digest() == hashlib.sha256((root / "internal/dashboard/static" / asset).read_bytes()).digest()
    state["injecting"] = True
    pattern = "**/api/experiments"
    for status, message in ((401, "授权"), (403, "授权"), (503, "暂时不可用")):
        page.route(pattern, lambda route, _request, status=status: route.fulfill(status=status, content_type="application/json", body='{"error":"unavailable"}'))
        page.locator("#refresh-experiments").click()
        expect(page.locator("#lab-error")).to_contain_text(message)
        page.unroute(pattern)
        page.locator("#lab-retry").click()
        expect(page.locator("#lab-error")).to_be_hidden()
    detail_pattern = f'**/api/experiments?id={child["id"]}'
    with stalled_endpoint() as stalled_url:
        page.route(detail_pattern, lambda route: route.continue_(url=stalled_url))
        page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
        expect(page.locator("#detail-loading")).to_be_visible()
        expect(page.locator("#experiment-detail")).to_be_hidden()
        expect(page.locator("#detail-error")).to_contain_text("超时", timeout=15000)
        page.unroute(detail_pattern)
    page.locator("#detail-retry").click()
    expect(page.locator("#run-title")).to_have_text(child["spec"]["title"])
    with stalled_endpoint() as stalled_url:
        page.route(detail_pattern, lambda route: route.continue_(url=stalled_url))
        page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
        expect(page.locator("#detail-loading")).to_be_visible()
        page.locator(f'.experiment-item[data-id="{parent["id"]}"]').click()
        expect(page.locator("#run-title")).to_have_text(parent["spec"]["title"])
        page.unroute(detail_pattern)
    # A changed engine or snapshot must suppress numeric improvements.
    different = dict(detail)
    different["code_version"] = "incompatible-version"
    page.route(detail_pattern, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(different)))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#comparison-note")).to_contain_text("口径不同")
    assert all(v == "—" for v in page.locator("#comparison-body td:last-child").all_text_contents())
    page.unroute(detail_pattern)
    no_signals = json.loads(json.dumps(detail))
    no_signals["result"]["groups"] = []
    no_signals["result"]["development"]["trades"] = []
    page.route(detail_pattern, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(no_signals)))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#group-body")).to_contain_text("没有入选信号")
    expect(page.locator("#trade-body")).to_contain_text("没有交易记录")
    expect(page.locator("#trade-next")).to_be_disabled()
    page.unroute(detail_pattern)
    failed = dict(detail)
    failed.update(status="failed", verdict="pending", result=None, error="实验数据不足，未生成结果。")
    page.route(detail_pattern, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(failed)))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#run-failure")).to_contain_text("数据不足")
    expect(page.locator("#run-results")).to_be_hidden()
    page.unroute(detail_pattern)
    page.wait_for_load_state("networkidle")
    state["injecting"] = False
    page.goto(url, wait_until="networkidle")
