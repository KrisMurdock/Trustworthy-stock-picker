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
    expect(page.locator(".experiment-item")).to_have_count(3)
    payload = context.request.get(url + "/api/experiments").json()
    records = payload["experiments"]
    child = next(r for r in records if r["spec"].get("max_upper_wick") is not None)
    parent = next(r for r in records if not r.get("parent_id"))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#run-title")).to_have_text("上影线过滤 · 单变量对照")
    assert "id=" + child["id"] in page.url
    expect(page.locator("#run-hypothesis")).to_contain_text("30%")
    expect(page.locator("#comparison-note")).to_contain_text("口径一致")
    expect(page.locator("#comparison-body tr")).to_have_count(25)
    expect(page.locator("#run-holdout")).not_to_have_text("待划分")
    expect(page.locator("#run-next")).not_to_be_empty()
    expect(page.locator("#validation-rules")).to_contain_text("最高价触达 2.5%／3% 或以上")
    expect(page.locator("#validation-rules")).to_contain_text("不要求收盘价达标")
    page.locator("#validation-rules summary").click()
    expect(page.locator("#validation-rules")).to_contain_text("买入当日受 T+1 限制")
    page.locator("#validation-rules summary").click()
    expect(page.locator("#comparison-body")).to_contain_text("首次触达 3% 平均天数")
    expect(page.locator("#risk-title")).to_contain_text("已完成样本未超限")
    expect(page.locator("#risk-summary")).to_contain_text("等于 3% 也不通过")
    expect(page.locator("#comparison-body")).to_contain_text("净盈利 ≥2.5%")
    expect(page.locator("#comparison-body")).to_contain_text("平均持仓")
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
    next_day = next(r for r in records if r["spec"].get("next_day_rule") == "baseline")
    page.locator(f'.experiment-item[data-id="{next_day["id"]}"]').click()
    expect(page.locator("#run-title")).to_have_text("次日退出基线")
    expect(page.locator("#next-day-mode")).to_contain_text("次日退出实验")
    expect(page.locator("#next-day-title")).to_contain_text("≥80%")
    expect(page.locator("#comparison-note")).to_contain_text("口径不同")
    actual = context.request.get(url + "/api/experiments?id=" + next_day["id"]).json()
    stats = actual["current_review"]["stats"]
    assert actual["result"]["development"]["research"]["parameters"]["max_holding_days"] == 1
    for short, count, rate in (("25", "next_day_2_5_count", "next_day_2_5_rate"), ("30", "next_day_3_count", "next_day_3_rate")):
        expect(page.locator("#next-day-" + short)).to_contain_text(f'{stats[rate] * 100:.2f}%')
        expect(page.locator("#next-day-" + short)).to_contain_text(f'{stats[count]} / {stats["samples"]}')
        page.locator("#trade-filter").select_option("next" + short)
        expect(page.locator("#trade-page")).to_contain_text(f'{stats[count]} 条')
    page.set_viewport_size({"width": 390, "height": 844})
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "next-day page overflow"
    page.screenshot(path=str(screenshots / f"{kind}-next-day.png"), full_page=True)
    page.set_viewport_size({"width": 1440, "height": 1000})
    page.locator("#trade-filter").select_option("selected")
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#next-day-mode")).to_contain_text("原持仓期限档案")
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
    legacy = json.loads(json.dumps(detail))
    legacy.update(engine="development_top_n_v1", verdict="promising")
    legacy["current_review"]["risk"].update(status="breached", breaches=1, exact_count=True)
    legacy["current_review"]["stats"].update(net_loss_limit_breaches=1, worst_resolved_net_pct=-3.2)
    bad_trade = legacy["result"]["development"]["trades"][0]
    bad_trade["selected"] = True
    bad_trade["outcome"].update(status="stop_gap", net_pct=-3.2)
    page.route(detail_pattern, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(legacy)))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#run-tags")).to_contain_text("风险不达标")
    expect(page.locator("#run-tags")).not_to_contain_text("开发候选")
    expect(page.locator("#risk-original")).to_contain_text("档案原结论：开发候选")
    page.locator("#show-breaches").click()
    expect(page.locator("#trade-filter")).to_have_value("breaches")
    expect(page.locator("#trade-body")).to_contain_text("-3.200%")
    page.unroute(detail_pattern)
    # A daily high can reach the target while the exit loses money. The UI
    # must show both facts, and must not convert that observation into profit.
    touched = json.loads(json.dumps(detail))
    sample = touched["result"]["development"]["trades"][0]
    sample["selected"] = True
    sample["outcome"].update(status="stop", net_pct=-2.8, high_touch={"schema_version":1,"observed_days":1,"max_net_pct":3.2,"first_2_5_day":1,"first_3_day":1,"exit_day_order_unknown":True,"first_2_5_order_unknown":True,"first_3_order_unknown":True})
    page.route(detail_pattern, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(touched)))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    page.locator("#trade-filter").select_option("touch30")
    expect(page.locator(f'#trade-body tr[data-trade="{sample["id"]}"]')).to_have_count(0)
    page.locator("#trade-filter").select_option("selected")
    row = page.locator(f'#trade-body tr[data-trade="{sample["id"]}"]')
    expect(row).to_contain_text("-2.800%")
    expect(row).to_contain_text("3.200%")
    expect(row).to_contain_text("顺序不明")
    page.locator("#trade-filter").select_option("next30")
    expect(page.locator(f'#trade-body tr[data-trade="{sample["id"]}"]')).to_have_count(0)
    page.locator("#trade-filter").select_option("selected")
    row.get_by_role("button", name="查看 K 线").click()
    expect(page.locator("#evidence-detail")).to_contain_text("价格触达不要求收盘达标")
    expect(page.locator("#evidence-detail")).to_contain_text("首次触达 2.5% / 3%：1（顺序不明） / 1（顺序不明）")
    page.unroute(detail_pattern)
    no_signals = json.loads(json.dumps(detail))
    no_signals["current_review"]["risk"].update(status="unverified", resolved=0)
    no_signals["current_review"]["stats"].update(samples=0, resolved=0, high_touch_samples=0)
    no_signals["current_review"]["next_day_verdict"] = "risk_unverified"
    no_signals["result"]["groups"] = []
    no_signals["result"]["development"]["trades"] = []
    page.route(detail_pattern, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(no_signals)))
    page.locator(f'.experiment-item[data-id="{child["id"]}"]').click()
    expect(page.locator("#group-body")).to_contain_text("没有入选信号")
    expect(page.locator("#trade-body")).to_contain_text("没有交易记录")
    expect(page.locator("#trade-next")).to_be_disabled()
    expect(page.locator("#next-day-25")).to_contain_text("暂无可核实样本")
    expect(page.locator("#next-day-verdict")).to_contain_text("风险待核实")
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
