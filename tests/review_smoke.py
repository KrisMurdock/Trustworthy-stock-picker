#!/usr/bin/env python3
"""Browser acceptance for the review page and the evidence-grounded advisor.

The dashboard fixture creates a real schema-6 market database. Reviewer detail
states that need a mature future path are supplied through an explicit HTTP
mock so this test stays deterministic and does not claim live returns.
"""

import argparse
import json
import re
import sys
from pathlib import Path

from playwright.sync_api import expect, sync_playwright

sys.dont_write_bytecode = True
from dashboard_smoke import LATEST, PREVIOUS, check_page_assets, service, wait_ready


ROOT = Path(__file__).resolve().parents[1]
REVISION = "a" * 64


def stream(*events):
    return "".join(json.dumps(event, ensure_ascii=False) + "\n" for event in events)


def rich_day():
    checkpoints = [
        {"signal_day": 1, "date": "2026-10-14", "close_net_pct": 0.5},
        {"signal_day": 3, "date": "2026-10-16", "close_net_pct": 1.25},
        {"signal_day": 5, "date": "2026-10-20", "close_net_pct": 2.8},
    ]
    events = [
        {
            "code": "sz000001", "name": "测试银行", "rank": 1, "recommended": True,
            "state": "mature", "signal_date": LATEST, "target_trading_date": "2026-10-14",
            "entry_date": "2026-10-14", "exit_date": "2026-10-20", "exit_reason": "target",
            "net_pct": 2.8, "max_favorable_net_pct": 3.2, "max_adverse_net_pct": -1.1,
            "checkpoints": checkpoints,
        },
        {
            "code": "sh600519", "name": "测试消费", "rank": 2, "recommended": False,
            "state": "pending", "state_reason": "horizon_not_reached", "signal_date": LATEST,
            "target_trading_date": "2026-10-14", "entry_date": "2026-10-14",
        },
    ]
    metrics = {
        "total": 2, "mature": 1, "pending": 1, "not_entered": 0, "missing": 0,
        "unsupported": 0, "entered": 2, "target": 1, "stop": 0, "stop_gap": 0,
        "timeout": 0, "positive": 1, "loss_breach": 0, "ambiguous": 0,
        "mean_net_pct": 2.8, "positive_rate": 1.0, "worst_max_adverse_net_pct": -1.1,
    }
    recommended = dict(metrics, total=1, pending=0, entered=1)
    observed = dict(metrics, total=1, mature=0, target=0, positive=0,
                    mean_net_pct=None, positive_rate=None, worst_max_adverse_net_pct=None)
    for key in ("mean_net_pct", "positive_rate", "worst_max_adverse_net_pct"):
        if observed[key] is None:
            observed.pop(key)
    return {
        "schema_version": 1, "as_of": LATEST, "target_trading_date": "2026-10-14",
        "generated_at": "2026-10-13T19:00:00+08:00", "revision": REVISION,
        "revisions": [{"revision": REVISION, "as_of": LATEST,
                       "generated_at": "2026-10-13T19:00:00+08:00", "schema_version": 2}],
        "data_as_of": "2026-10-20", "calendar_from": LATEST, "calendar_through": "2026-10-20",
        "calendar_source": "fixture", "strategy": "short_horizon_v1",
        "aggregate": {"all": metrics, "recommended": recommended, "observed": observed},
        "events": events,
    }


def rich_event(code):
    if code == "sh600519":
        return {
            "code": code, "name": "测试消费", "rank": 2, "recommended": False,
            "state": "pending", "state_reason": "horizon_not_reached", "signal_date": LATEST,
            "target_trading_date": "2026-10-14", "entry_date": "2026-10-14",
            "report_revision": REVISION, "report_generated_at": "2026-10-13T19:00:00+08:00",
            "evidence": [{"evidence_id": "E1", "kind": "report", "source": "selection_run",
                          "as_of": LATEST, "value": {"revision": REVISION}}],
        }
    event = dict(rich_day()["events"][0])
    event.update({
        "report_revision": REVISION, "report_generated_at": "2026-10-13T19:00:00+08:00",
        "reference_plan": {
            "schema_version": 1, "reference_entry_price": 10.25, "reference_shares": 1800,
            "reference_buy_cost": 18451.0, "target_net_2_5_price": 10.52,
            "target_net_3_price": 10.58, "take_profit_price": 10.58, "stop_price": 9.94,
            "net_loss_limit_pct": 3.0, "modeled_stop_net_pct": -2.9,
            "effective_stop_goal_net_pct": -3.0, "reference_only": False,
        },
        "signal_bar": {"date": LATEST, "open": 10.0, "high": 10.4, "low": 9.9,
                       "close": 10.25, "volume_raw": 1000},
        "outcome": {"status": "target", "entry_date": "2026-10-14", "exit_date": "2026-10-20",
                    "exit_price": 10.58, "net_pct": 2.8, "holding_days": 5,
                    "max_favorable_net_pct": 3.2, "max_adverse_net_pct": -1.1,
                    "ambiguous": False, "checkpoints": event["checkpoints"]},
        "structure_guard": {"guard_status": "pass", "mode": "shadow", "data_status": "ok",
                            "signal_status": "ok", "reason": "not_vetoed", "levels": []},
        "evidence": [
            {"evidence_id": "E1", "kind": "report", "source": "selection_run", "as_of": LATEST,
             "value": {"revision": REVISION}},
            {"evidence_id": "E2", "kind": "candidate", "source": "selection_report",
             "value": {"code": code, "rank": 1}},
        ],
    })
    return event


def install_routes(page):
    day = rich_day()

    def review_route(route):
        route.fulfill(status=200, content_type="application/json", body=json.dumps(day, ensure_ascii=False))

    def event_route(route):
        query = route.request.url.split("?", 1)[-1]
        match = re.search(r"(?:^|&)code=([^&]+)", query)
        code = match.group(1) if match else "sz000001"
        route.fulfill(status=200, content_type="application/json",
                      body=json.dumps(rich_event(code), ensure_ascii=False))

    page.route(re.compile(r".*/api/review/event\?.*"), event_route)
    page.route(re.compile(r".*/api/review\?.*"), review_route)


def check_real_page(context, page, url):
    response = page.goto(url + "/review", wait_until="networkidle")
    assert response.status == 200
    expect(page).to_have_title("复盘回看 · Stock Watcher")
    expect(page.get_by_test_id("review-page")).to_be_visible()
    assert page.locator('nav.header-nav [aria-current="page"]').inner_text() == "复盘回看"
    check_page_assets(context, page, url)
    expect(page.get_by_test_id("market-context-panel")).to_be_visible()
    expect(page.get_by_test_id("market-context-body")).to_be_visible()
    expect(page.get_by_test_id("market-context-indices").locator("article")).to_have_count(5)
    expect(page.get_by_test_id("market-signal-panel")).to_be_visible()
    expect(page.get_by_test_id("market-signal-cards").locator("[data-testid='market-signal-card']")).to_have_count(2)
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("上证指数")
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("创业板指")
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("样本外")
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("周一")
    expect(page.locator("[data-testid='market-signal-session']")).to_have_count(4)
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("上午")
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("下午")
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("留出")
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("高置信")
    expect(page.get_by_test_id("market-signal-panel")).to_contain_text("已启用韩国 KOSPI/KOSDAQ")
    expect(page.get_by_test_id("market-context-board-list")).to_contain_text("半导体")
    expect(page.get_by_test_id("market-context-board-list")).to_contain_text("存储芯片")
    expect(page.get_by_test_id("market-context-count")).to_contain_text("3 / 3")
    page.get_by_test_id("market-context-search").fill("存储")
    expect(page.get_by_test_id("market-context-board-list")).to_contain_text("存储芯片")
    expect(page.get_by_test_id("market-context-board-list")).not_to_contain_text("半导体")
    page.get_by_test_id("market-context-search").fill("")
    expect(page.get_by_test_id("review-date-select")).to_have_value(LATEST)
    expect(page.locator("#review-content")).to_be_visible()
    expect(page.get_by_test_id("review-aggregate")).to_be_visible()
    expect(page.get_by_test_id("review-event-row")).to_have_count(2)
    expect(page.get_by_test_id("review-event-row").first).to_contain_text("不支持复盘")
    page.get_by_test_id("review-event-row").first.click()
    expect(page.get_by_test_id("review-detail")).to_be_visible()
    expect(page.locator("#review-detail-body")).to_contain_text("旧报告")
    expect(page.locator("#review-evidence-E1")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1")


def check_mature_detail(page, url):
    install_routes(page)
    page.get_by_test_id("review-refresh").click()
    expect(page.get_by_test_id("review-event-row")).to_have_count(2)
    expect(page.get_by_test_id("review-event-row").first).to_contain_text("已成熟")
    expect(page.locator("#review-event-count")).to_have_text("2")
    page.get_by_test_id("review-event-row").first.click()
    expect(page.locator("#review-detail-body")).to_contain_text("参考计划")
    expect(page.locator("#review-detail-body")).to_contain_text("T+1")
    expect(page.locator(".review-checkpoint")).to_have_count(3)
    expect(page.locator(".review-checkpoint").nth(0)).to_contain_text("+0.50%")
    expect(page.locator(".review-checkpoint").nth(2)).to_contain_text("+2.80%")
    expect(page.locator("#review-detail-body")).to_contain_text("结构门控")
    expect(page.locator("#review-evidence-E1")).to_be_visible()
    expect(page.locator("#review-evidence-E2")).to_be_visible()
    assert "code=sz000001" in page.url

    page.get_by_test_id("review-event-row").nth(1).click()
    expect(page.locator("#review-detail-body")).to_contain_text("未成熟")
    assert "code=sh600519" in page.url


def check_unconfigured_ai(page, url):
    page.get_by_test_id("master-open").click()
    expect(page.get_by_test_id("master-drawer")).to_be_visible()
    expect(page.get_by_test_id("master-role-select").locator("option")).to_have_count(8)
    expect(page.locator("#master-ai-status")).to_contain_text("尚未配置")
    expect(page.get_by_test_id("master-input")).to_be_disabled()
    expect(page.get_by_test_id("master-send")).to_be_disabled()
    expect(page.locator("#review-analysis-button")).to_be_disabled()

    page.get_by_test_id("llm-settings-open").click()
    expect(page.get_by_test_id("llm-settings-dialog")).to_be_visible()
    expect(page.get_by_test_id("llm-base-url")).to_have_value("https://api.openai.com/v1")
    expect(page.get_by_test_id("llm-api-key")).to_have_value("")
    page.get_by_test_id("llm-settings-close").click()
    page.get_by_test_id("master-close").click()
    expect(page.get_by_test_id("master-drawer")).to_be_hidden()


def configure_mock_ai(page):
    config = {
        "configured": True, "enabled": True, "base_url": "https://mock.example/v1",
        "model": "mock-model", "timeout": "60s", "api_key_present": True,
        "api_key_suffix": "1234", "write_enabled": True, "write_mode": "all",
        "locked": {}, "fields": {},
    }
    page.route(re.compile(r".*/api/llm/config$"), lambda route: route.fulfill(
        status=200, content_type="application/json", body=json.dumps(config)))


def check_streaming_ai(page):
    success = stream(
        {"type": "intent", "intents": ["stock_evidence"], "summary": "分析单股证据"},
        {"type": "plan", "steps": ["读取冻结事件", "提取检查点"], "summary": "读取冻结事件；提取检查点"},
        {"type": "evidence", "evidence": [{"evidence_id": "E1", "kind": "report",
                                           "source": "selection_run", "value": {}}],
         "evidence_ids": ["E1"], "count": 1, "summary": "已收集 1 条本地证据"},
        {"type": "prompt", "evidence_count": 1, "trimmed": False,
         "prompt": {"version": "advisor-review-v1", "role": "buffett", "mode": "review",
                    "evidence_count": 1, "history_turns": 0}},
        {"type": "delta", "delta": "先看信号日证据 [E1]。", "text": "先看信号日证据 [E1]。"},
        {"type": "delta", "delta": "再核对未知引用 [E9]。", "text": "再核对未知引用 [E9]。"},
        {"type": "citation", "valid": ["E1"], "invalid": ["E9"], "zero_reference": False,
         "citation": {"valid": ["E1"], "invalid": ["E9"], "zero": False, "total": 2}},
        {"type": "done", "intents": ["stock_evidence"],
         "done": {"intents": ["stock_evidence"], "answer_runes": 22},
         "partial": False},
    )
    failure = stream(
        {"type": "intent", "intents": ["stock_evidence"], "summary": "分析单股证据"},
        {"type": "error", "code": "llm_request_failed", "message": "模型请求失败，请稍后重试",
         "error": {"code": "llm_request_failed", "message": "模型请求失败，请稍后重试"}},
    )
    calls = {"chat": 0}

    def chat_route(route):
        calls["chat"] += 1
        route.fulfill(status=200, content_type="application/x-ndjson",
                      body=success if calls["chat"] == 1 else failure)

    page.route(re.compile(r".*/api/master/chat$"), chat_route)
    page.get_by_test_id("master-open").click()
    expect(page.locator("#master-ai-status")).to_contain_text("AI 已配置")
    expect(page.get_by_test_id("master-input")).to_be_enabled()
    page.get_by_test_id("master-input").fill("这只股票为什么没有进入正式推荐？")
    page.get_by_test_id("master-send").click()
    expect(page.locator("#master-answer")).to_contain_text("先看信号日证据")
    expect(page.locator(".citation-link")).to_have_count(1)
    expect(page.locator(".citation-invalid")).to_have_count(1)
    expect(page.locator("#master-stream-status")).to_contain_text("回答完成")
    chain = page.get_by_test_id("master-chain")
    assert chain.evaluate("node => node.open") is False, "done chain did not collapse"
    assert page.locator("#master-answer").get_by_text("未引用本地证据").count() == 0

    page.get_by_test_id("master-input").fill("再解释一次")
    page.get_by_test_id("master-send").click()
    expect(page.locator("#master-stream-status")).to_contain_text("模型请求失败")
    chain = page.get_by_test_id("master-chain").last
    assert chain.evaluate("node => node.open") is True, "failed chain stayed collapsed"
    page.get_by_test_id("master-close").click()
    expect(page.get_by_test_id("master-drawer")).to_be_hidden()


def check_analysis_stream(page):
    body = stream(
        {"type": "intent", "intents": ["report_query"], "summary": "分析报告聚合"},
        {"type": "plan", "steps": ["读取报告"], "summary": "读取报告"},
        {"type": "evidence", "evidence": [], "evidence_ids": [], "count": 0, "summary": "未查询数据库"},
        {"type": "prompt", "evidence_count": 0, "trimmed": False,
         "prompt": {"version": "advisor-review-v1", "mode": "review", "evidence_count": 0}},
        {"type": "delta", "delta": "报告层面的归因。", "text": "报告层面的归因。"},
        {"type": "citation", "valid": [], "invalid": [], "zero_reference": True,
         "citation": {"valid": [], "invalid": [], "zero": True, "total": 0}},
        {"type": "done", "intents": ["report_query"],
         "done": {"intents": ["report_query"], "answer_runes": 7}, "partial": False},
    )
    page.route(re.compile(r".*/api/review/analysis$"), lambda route: route.fulfill(
        status=200, content_type="application/x-ndjson", body=body))
    page.get_by_test_id("review-analysis-button").click()
    expect(page.locator("#review-analysis-answer")).to_contain_text("报告层面的归因")
    expect(page.locator("#review-analysis-citations")).to_contain_text("未引用本地证据")
    expect(page.locator("#review-analysis-state")).to_contain_text("已完成")


def check_mobile(page, url):
    page.set_viewport_size({"width": 390, "height": 844})
    page.goto(url + "/review", wait_until="networkidle")
    expect(page.get_by_test_id("review-page")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1")
    page.get_by_test_id("master-open").click()
    expect(page.get_by_test_id("master-drawer")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1")
    page.get_by_test_id("master-close").click()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary", nargs="?", help="native stock-watcher binary")
    parser.add_argument("--image", help="container image to exercise instead of the binary")
    args = parser.parse_args()
    if not args.binary and not args.image:
        parser.error("provide a binary path or --image")
    with service(args) as (url, _directory, _env):
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch()
            context = browser.new_context(viewport={"width": 1440, "height": 1000})
            page = context.new_page()
            wait_ready(context, url)
            errors = []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.on("console", lambda message: errors.append(message.text) if message.type == "error" else None)
            try:
                check_real_page(context, page, url)
                check_mature_detail(page, url)
                check_unconfigured_ai(page, url)
                configure_mock_ai(page)
                check_streaming_ai(page)
                check_analysis_stream(page)
                check_mobile(page, url)
                assert not errors, "browser errors: " + " | ".join(errors)
                print("review browser smoke passed")
            except BaseException:
                screenshots = ROOT / ".scratch" / "review-verification"
                screenshots.mkdir(parents=True, exist_ok=True)
                page.screenshot(path=str(screenshots / "review-failure.png"), full_page=True)
                raise
            finally:
                context.close()
                browser.close()


if __name__ == "__main__":
    main()
