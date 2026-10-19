"use strict";

(() => {
  const { byId, setText, show, el, number, isNumber, displayLabel, formatTime, loadDashboard } = window.StockWatcher;
  const legacyRoutes = { "#stock-chart": "/chart", "#rankings": "/rankings", "#stock-pool": "/stocks" };
  const redirectLegacyHash = () => {
    const target = legacyRoutes[location.hash];
    if (!target) return false;
    location.replace(target);
    return true;
  };
  if (redirectLegacyHash()) return;
  window.addEventListener("hashchange", redirectLegacyHash);
  const state = { data: null, refreshing: false };

  function renderSummary(data) {
    const summary = data.summary || {};
    setText("pool-count", number(summary.pool_size));
    setText("available-count", number(summary.available));
    setText("missing-count", number(summary.missing));
    setText("eligible-count", data.report_current ? number(summary.eligible) : "—");
    setText("selected-count", data.report_current ? number(summary.selected) : "—");
    setText("coverage-value", number(summary.coverage_pct, 1));
    byId("coverage-progress").value = isNumber(summary.coverage_pct) ? Math.max(0, Math.min(100, summary.coverage_pct)) : 0;
    byId("coverage-progress").setAttribute("aria-valuetext", isNumber(summary.coverage_pct) ? `${number(summary.coverage_pct, 1)}%` : "暂无数据");
    setText("snapshot-date", data.as_of || "尚无快照");
    setText("snapshot-generated", `快照生成：${formatTime(data.generated_at)}`);
    setText("snapshot-target", `目标交易日：${data.target_trading_date || "—"}`);
    const adjustLabels = { qfq: "前复权", hfq: "后复权", none: "不复权" };
    const adjust = adjustLabels[data.adjust] || data.adjust || "—";
    setText("snapshot-adjust", `价格口径：${adjust}`);
    const notice = byId("market-notice");
    if (data.as_of && !data.report_current) {
      const link = el("a", "notice-link", data.report ? `查看 ${data.report.as_of} 的历史排名 →` : "查看日推排名 →");
      link.href = data.report ? `/rankings?as_of=${encodeURIComponent(data.report.as_of)}` : "/rankings";
      notice.replaceChildren(
        el("span", "", data.report ? "当前行情尚无对应推荐报告，最新报告日期与当前行情不一致。" : "当前行情尚无对应推荐报告。达到覆盖率及策略条件后才会生成推荐。"),
        link,
      );
      show("market-notice", true);
    } else {
      notice.replaceChildren();
      show("market-notice", false);
    }
    const sync = data.latest_sync;
    const syncLabels = { published: "最近同步成功", success: "最近同步成功", succeeded: "最近同步成功", completed: "最近同步成功", partial: "最近同步部分成功", failed: "最近同步失败", failure: "最近同步失败", running: "同步进行中", canceled: "最近同步已取消" };
    const success = sync && ["published", "success", "succeeded", "completed"].includes(sync.status);
    const syncText = sync ? `${syncLabels[sync.status] || sync.status || "同步状态未知"}${sync.as_of ? ` · ${sync.as_of}` : ""}${sync.error_kind ? ` · ${displayLabel(sync.error_kind)}` : ""}` : "尚无同步记录";
    setText("sync-status", syncText);
    byId("sync-status").title = sync?.finished_at ? `完成时间：${formatTime(sync.finished_at)}` : "";
    byId("sync-dot").className = `status-dot${sync ? success ? " ok" : " warning" : ""}`;
    show("empty-state", !data.generated_at && !data.report && !summary.available);
  }

  async function refreshRuntimeStatus() {
    try {
      const status = await window.StockWatcher.requestJSON("/api/status");
      if (status.schema_version !== 1 || !Array.isArray(status.issues)) throw new Error("运行状态格式不匹配");
      if (!status.ready) throw new Error("存储或交易日历暂不可用");
      const statusLabels = { healthy: "业务正常", degraded: "业务需关注", syncing: "同步处理中" };
      const phases = { collecting: "采集", analyzing: "分析", completed: "完成" };
      const task = status.task;
      const progress = task ? `；${phases[task.phase] || task.phase} ${task.completed}/${task.total}，最近心跳 ${formatTime(task.updated_at)}` : "；尚无任务记录";
      const minutes = status.minute_enabled ? `；分钟数据 ${status.minute_current}/${status.pool_size}（腾讯）` : "；分钟同步未启用";
      const issues = { daily_data_stale_or_incomplete: "日线未更新或覆盖不足", analysis_not_published: "分析尚未发布", task_heartbeat_stale: "任务心跳中断", notification_blocked: "消息发送受阻", notification_unknown: "消息结果未确认", notification_overdue: "消息重试延迟", minute_data_stale_or_incomplete: "分钟数据未更新或未校准" };
      const notes = status.issues.map((item) => issues[item] || (item.startsWith("task_failed_") ? "最近任务失败" : item)).join("、");
      setText("runtime-status", `${statusLabels[status.business] || "运行状态未知"} · 应有日线 ${status.expected_date}，可用 ${status.available}/${status.pool_size}${progress}${minutes}${notes ? `；${notes}` : ""}`);
    } catch (error) {
      setText("runtime-status", `运行状态不可用：${error.message}。行情显示不代表后台任务正常。`);
    }
  }

  async function refreshOverview() {
    if (state.refreshing) return;
    state.refreshing = true;
    refreshRuntimeStatus();
    byId("refresh-button").disabled = true;
    byId("retry-button").disabled = true;
    byId("refresh-button").querySelector("span").textContent = "正在刷新";
    byId("dashboard-content").setAttribute("aria-busy", "true");
    show("loading-state", !state.data);
    try {
      const data = await loadDashboard();
      state.data = data;
      renderSummary(data);
      show("dashboard-content", true);
      show("error-state", false);
      setText("live-status", `摘要已刷新，共 ${data.stocks.length} 支股票。`);
    } catch (error) {
      setText("error-title", state.data ? "刷新失败，数据已过期" : "摘要加载失败");
      setText("error-message", `${error.message}${state.data ? " 当前保留上次成功读取的摘要。" : ""}`);
      show("error-state", true);
    } finally {
      state.refreshing = false;
      byId("refresh-button").disabled = false;
      byId("retry-button").disabled = false;
      byId("refresh-button").querySelector("span").textContent = "刷新快照";
      byId("dashboard-content").setAttribute("aria-busy", "false");
      show("loading-state", false);
    }
  }

  byId("refresh-button").addEventListener("click", refreshOverview);
  byId("retry-button").addEventListener("click", refreshOverview);
  setInterval(() => { if (!document.hidden) refreshOverview(); }, 60000);
  document.addEventListener("visibilitychange", () => {
    setText("auto-refresh-label", document.hidden ? "页面隐藏，自动刷新已暂停" : "每 60 秒自动刷新");
    if (!document.hidden) refreshOverview();
  });
  refreshOverview();
})();
