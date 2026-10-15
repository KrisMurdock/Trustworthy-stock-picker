"""Calculator DOM, navigation, privacy and failure checks for one shared browser."""
import hashlib

from playwright.sync_api import expect


def calculator_checks(context, page, url, root, args, state, stalled_endpoint):
    page.get_by_role("link", name="仓位计算", exact=True).click()
    expect(page).to_have_url(url + "/calculator")
    expect(page).to_have_title("仓位计算 · Stock Watcher")
    calculate = page.get_by_role("button", name="计算仓位", exact=True)
    expect(calculate).to_be_enabled()
    expect(page.get_by_role("heading", name="仓位计算", exact=False)).to_be_visible()
    for asset in ("calculator.js", "strategy-model.js", "calculator.css", "style.css"):
        response = context.request.get(url + "/assets/" + asset)
        assert response.status == 200
        assert response.headers["cache-control"] == "no-cache"
        assert hashlib.sha256(response.body()).digest() == hashlib.sha256((root / "internal/dashboard/static" / asset).read_bytes()).digest()
    assert context.request.post(url + "/calculator").status == 405
    assert context.request.get(url + "/calculator").headers["cache-control"] == "no-store"

    requests = []
    record = lambda request: requests.append((request.method, request.url, request.post_data))
    page.on("request", record)
    page.get_by_role("button", name="载入 215 股示例").click()
    calculate.click()
    expect(page.locator('[data-order="add"] [data-result="shares"]')).to_have_text("最多 0 股")
    expect(page.locator('[data-order="add"]')).to_contain_text("风险额度")
    expect(page.locator('[data-result="risk_used"]')).to_have_text("492.25 元")
    expect(page.locator('[data-result="risk_remaining"]')).to_have_text("7.75 元")
    expect(page.locator('[data-result="target_price"]')).to_have_text("48.61 元")
    expect(page.locator('[data-result="loss_budget_execution_price"]')).to_have_text("44.87 元")
    assert page.url == url + "/calculator", "account values must not appear in URL"

    # Editing invalidates the previous output; malformed inputs remain editable.
    page.get_by_label("最大亏损金额（元）", exact=True).fill("NaN")
    expect(page.locator("#strategy-results")).to_be_hidden()
    calculate.click()
    expect(page.locator("#form-error")).to_be_visible()
    expect(page.get_by_label("最大亏损金额（元）", exact=True)).to_have_attribute("aria-invalid", "true")
    expect(page.get_by_label("最大亏损金额（元）", exact=True)).to_be_focused()
    page.get_by_label("最大亏损金额（元）", exact=True).fill("500")
    calculate.click()
    expect(page.locator("#form-error")).to_be_hidden()

    page.get_by_label("止盈方式", exact=True).select_option("profit")
    page.get_by_label("净止盈金额（元）", exact=True).fill("0")
    calculate.click()
    expect(page.get_by_label("净止盈金额（元）", exact=True)).to_be_focused()
    expect(page.locator("#form-error")).to_contain_text("必须大于 0")
    page.get_by_label("净止盈金额（元）", exact=True).fill("500")
    calculate.click()
    expect(page.locator('[data-result="target_profit"]')).to_have_text("500.00 元")
    expect(page.locator('[data-result="target_price"]')).to_have_text("49.52 元")

    page.get_by_label("首次建仓", exact=True).check()
    page.get_by_label("最大亏损金额（元）", exact=True).fill("1000")
    page.get_by_label("账户风险比例（%）", exact=True).fill("1")
    page.get_by_label("首次买入价（元）", exact=True).fill("47.05")
    page.get_by_label("新增每笔买入费用（元）", exact=True).fill("10")
    page.get_by_label("止盈方式", exact=True).select_option("pct")
    calculate.click()
    expect(page.locator('[data-order="initial"] [data-result="shares"]')).to_have_text("最多 223 股")
    expect(page.locator('[data-order="add"] [data-result="shares"]')).to_have_text("最多 206 股")
    expect(page.locator('#position-values [data-result="shares"]')).to_have_text("429 股")
    expect(page.locator('[data-result="cash_remaining"]')).to_have_text("60,011.85 元")
    expect(page.locator('[data-result="target_price"]')).to_have_text("48.04 元")

    screenshots = root / ".scratch" / "dashboard-verification"
    screenshots.mkdir(parents=True, exist_ok=True)
    kind = "container" if args.image else "native"
    page.screenshot(path=str(screenshots / f"calculator-{kind}-desktop.png"), full_page=True)
    page.set_viewport_size({"width": 390, "height": 844})
    calculate.scroll_into_view_if_needed()
    expect(calculate).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "calculator overflows mobile viewport"
    page.screenshot(path=str(screenshots / f"calculator-{kind}-mobile.png"), full_page=True)
    page.set_viewport_size({"width": 1440, "height": 1000})

    # All calculations remain usable without a market-data request or a network.
    context.set_offline(True)
    try:
        page.get_by_label("最大亏损金额（元）", exact=True).fill("500")
        calculate.click()
        expect(page.locator('[data-order="initial"] [data-result="shares"]')).to_have_text("最多 0 股")
        expect(page.locator('[data-order="add"]')).to_contain_text("底仓未能建立")
    finally:
        context.set_offline(False)
    assert requests == [], f"calculator sent financial data over network: {requests}"
    assert page.evaluate("localStorage.length === 0 && sessionStorage.length === 0"), "account data persisted in browser storage"
    page.remove_listener("request", record)

    # Multiple historical lots, deletion, and reset must not retain old output.
    page.get_by_role("button", name="载入 215 股示例").click()
    page.get_by_role("button", name="＋ 添加持仓批次").click()
    lots = page.locator(".holding-row")
    lots.nth(0).locator('[data-key="price"]').fill("47")
    lots.nth(0).locator('[data-key="shares"]').fill("100")
    lots.nth(0).locator('[data-key="buy_fee"]').fill("5")
    lots.nth(1).locator('[data-key="price"]').fill("46")
    lots.nth(1).locator('[data-key="shares"]').fill("100")
    lots.nth(1).locator('[data-key="buy_fee"]').fill("5")
    page.get_by_label("计划加仓价（元，可选）", exact=True).fill("")
    page.get_by_label("止盈方式", exact=True).select_option("profit")
    page.get_by_label("净止盈金额（元）", exact=True).fill("500")
    calculate.click()
    expect(page.locator('[data-result="target_price"]')).to_have_text("49.15 元")
    page.get_by_role("button", name="删除第 2 批持仓").click()
    expect(lots).to_have_count(1)
    expect(page.locator("#strategy-results")).to_be_hidden()
    page.get_by_role("button", name="清空重填").click()
    expect(page.get_by_label("账户总资产（元）", exact=True)).to_have_value("")
    expect(page.get_by_label("首次建仓", exact=True)).to_be_checked()
    calculate.click()
    expect(page.locator("#form-error")).to_contain_text("账户总资产")
    page.get_by_label("账户总资产（元）", exact=True).fill("12345")
    page.reload(wait_until="networkidle")
    expect(calculate).to_be_enabled()
    expect(page.get_by_label("账户总资产（元）", exact=True)).to_have_value("")

    # Model asset denial/unavailability must show an actionable, recoverable UI.
    state["injecting"] = True
    pattern = "**/assets/strategy-model.js"
    for status in (403, 503):
        page.route(pattern, lambda route: route.fulfill(status=status, body="unavailable"))
        page.reload(wait_until="domcontentloaded")
        expect(page.locator("#calculator-loading")).to_contain_text("计算器加载失败")
        expect(calculate).to_be_disabled()
        page.unroute(pattern)
        page.get_by_role("button", name="重新加载", exact=True).click()
        expect(calculate).to_be_enabled()
        expect(page.locator("#calculator-loading")).to_be_hidden()
    with stalled_endpoint() as stalled:
        page.route(pattern, lambda route: route.continue_(url=stalled))
        page.reload(wait_until="domcontentloaded")
        expect(page.locator("#calculator-loading")).to_contain_text("超时", timeout=13000)
        expect(calculate).to_be_disabled()
        page.unroute(pattern)
        page.goto("about:blank")
    page.goto(url + "/calculator", wait_until="networkidle")
    expect(calculate).to_be_enabled()
    expect(page.get_by_label("账户总资产（元）", exact=True)).to_have_value("")
    state["injecting"] = False
    page.get_by_role("link", name="股池观察", exact=True).click()
    expect(page).to_have_url(url + "/")
