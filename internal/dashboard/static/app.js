"use strict";

(() => {
  const byId = (id) => document.getElementById(id);
  const state = { data: null, report: null, reportDate: "", followLatest: true, sort: "code", direction: 1, refreshing: false, reportRequest: 0, reportController: null };
  const chart = new window.StockChart(requestJSON);
  const statusLabels = { available: "已同步 · 未发布推荐", selected: "已入选", eligible: "有效数据", filtered: "未通过筛选", excluded: "暂不参与", missing: "数据缺失" };
  const labels = {
    missing_data: "尚无行情数据", invalid_or_stale_data: "数据无效或已过期", no_history: "暂无历史数据",
    stale_or_suspended: "数据滞后或停牌", no_trading_volume: "当日无成交量", insufficient_or_invalid_history: "历史数据不足或无效",
    special_treatment: "风险警示股票", no_bullish_alignment: "均线未形成多头排列", insufficient_volume_or_macd: "量能或 MACD 未达条件",
    historical_net_loss_limit_breached: "历史规则存在净亏损达到或超过3%的样本", overheat_or_weakness: "过热或趋势偏弱", score_below_threshold: "趋势分未达门槛", strategy_evidence_or_risk_gate: "历史证据或风险条件未达标",
    rising_ma20: "20 日均线向上", stronger_than_pool: "近 5 日强于股池", strong_close: "收盘处于当日高位",
    insufficient_or_unprofitable_evidence: "匹配规则未通过历史检验", historical_net_loss_limit_breached: "历史单笔净亏损超限", extended_price: "短期涨幅较大", large_daily_range: "日内波动较大",
    upper_wick: "上影线较长", cash_insufficient: "参考资金不足", holdout_validation_not_passed: "独立样本验证未通过",
    baseline: "股池基准", pullback: "趋势回踩", momentum: "趋势动量", breakout: "突破形态", volume_recovery: "量能恢复",
    coverage: "覆盖率未达推荐门槛，已保存有效行情", network: "行情网络请求失败", timeout: "行情请求超时", canceled: "同步已取消", storage: "存储不可用", sync: "同步异常",
    http_403: "行情访问被拒绝", http_429: "行情请求受限", qfq_adjustment_missing: "缺少前复权数据", calendar_window: "交易日历覆盖不足", window_gap: "历史数据存在缺口",
  };
  const number = (value, digits = 0) => typeof value === "number" && Number.isFinite(value) ? value.toLocaleString("zh-CN", { minimumFractionDigits: digits, maximumFractionDigits: digits }) : "—";
  const isNumber = (value) => typeof value === "number" && Number.isFinite(value);
  const setText = (id, value) => { byId(id).textContent = value; };
  const show = (id, visible) => { byId(id).hidden = !visible; };
  const displayLabel = (value) => {
    if (typeof value !== "string" || !value) return "—";
    if (labels[value]) return labels[value];
    const prefix = "historical_rule_";
    return value.startsWith(prefix) ? `历史规则支持：${labels[value.slice(prefix.length)] || value.slice(prefix.length)}` : value;
  };
  const formatTime = (value) => {
    if (!value) return "—";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(date).replaceAll("/", "-") + " 北京时间";
  };
  const el = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };
  const list = (values) => Array.isArray(values) ? values : [];
  const volume = (stock) => stock.bar?.volume_shares ?? stock.bar?.volume_raw ?? stock.bar?.volume;
  function formattedVolume(value) {
    if (!isNumber(value)) return "—";
    if (Math.abs(value) >= 100000000) return number(value / 100000000, 2) + " 亿";
    if (Math.abs(value) >= 10000) return number(value / 10000, 2) + " 万";
    return number(value);
  }
  async function requestJSON(url, signal) {
    const controller = new AbortController();
    const forwardAbort = () => controller.abort();
    if (signal?.aborted) controller.abort();
    signal?.addEventListener("abort", forwardAbort, { once: true });
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, 10000);
    try {
      const response = await fetch(url, { signal: controller.signal, cache: "no-store", headers: { Accept: "application/json" } });
      let payload;
      try { payload = await response.json(); } catch (error) {
        if (controller.signal.aborted) throw error;
        throw new Error("服务返回了无法读取的数据，请稍后重试。");
      }
      if (!response.ok) {
        if (response.status === 401 || response.status === 403) throw new Error("当前访问未获授权，请检查访问权限或网关配置。");
        if (response.status === 504) throw new Error("服务读取行情数据超时，请稍后重试。");
        if (payload?.error === "market data is temporarily unavailable") throw new Error("行情数据暂时不可用，请稍后重试。");
        if (payload?.error === "history not found") throw new Error("该股票本次暂无通过校验的日线，请查看股池中的数据缺失原因，等待下次同步。");
        if (payload?.error === "minute history not found") throw new Error("该股票暂无分钟数据，请等待分钟同步完成。");
        if (payload?.error === "report not found") throw new Error("所选日期的报告不存在，请刷新快照后重试。");
        throw new Error(typeof payload?.error === "string" ? payload.error : `请求失败（HTTP ${response.status}）`);
      }
      return payload;
    } catch (error) {
      if (timedOut) throw new Error("请求超时（10 秒），请检查服务状态后重试。");
      if (error.name === "AbortError") throw error;
      if (error instanceof TypeError) throw new Error("无法连接服务，请检查网络连接后重试。");
      throw error;
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", forwardAbort);
    }
  }
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
    setText("stocks-footer", `${data.as_of || "等待同步"} · ${adjust}日线 · 成交量已统一换算为股`);
    show("market-notice", !!data.as_of && !data.report_current);
    setText("market-notice", `当前行情尚无对应推荐报告。${data.report ? `下方保留 ${data.report.as_of} 的历史报告，请以报告日期为准。` : "达到覆盖率及策略条件后才会生成推荐。"}`);
    const sync = data.latest_sync;
    const syncLabels = { published: "最近同步成功", success: "最近同步成功", succeeded: "最近同步成功", completed: "最近同步成功", partial: "最近同步部分成功", failed: "最近同步失败", failure: "最近同步失败", running: "同步进行中", canceled: "最近同步已取消" };
    const success = sync && ["published", "success", "succeeded", "completed"].includes(sync.status);
    const syncText = sync ? `${syncLabels[sync.status] || sync.status || "同步状态未知"}${sync.as_of ? ` · ${sync.as_of}` : ""}${sync.error_kind ? ` · ${displayLabel(sync.error_kind)}` : ""}` : "尚无同步记录";
    setText("sync-status", syncText);
    byId("sync-status").title = sync?.finished_at ? `完成时间：${formatTime(sync.finished_at)}` : "";
    byId("sync-dot").className = `status-dot${sync ? success ? " ok" : " warning" : ""}`;
    show("empty-state", !data.generated_at && !data.report && !summary.available);
  }
  function updateSectorOptions(stocks) {
    const select = byId("pool-filter");
    const current = select.value;
    const sectors = [...new Set(stocks.map((stock) => stock.sector || "未分类"))].sort((a, b) => a.localeCompare(b, "zh-CN"));
    select.replaceChildren(el("option", "", "全部行业"));
    select.firstChild.value = "";
    for (const sector of sectors) {
      const option = el("option", "", sector);
      option.value = sector;
      select.append(option);
    }
    select.value = sectors.includes(current) ? current : "";
  }
  function stockValue(stock, field) {
    if (field === "close") return stock.bar?.close;
    if (field === "volume") return volume(stock);
    if (["code", "name", "change_pct"].includes(field)) return stock[field];
    return stock.metrics?.[field];
  }
  function compareStocks(a, b) {
    const av = stockValue(a, state.sort);
    const bv = stockValue(b, state.sort);
    const missingA = av === null || av === undefined || (typeof av === "number" && !Number.isFinite(av));
    const missingB = bv === null || bv === undefined || (typeof bv === "number" && !Number.isFinite(bv));
    if (missingA !== missingB) return missingA ? 1 : -1;
    const difference = missingA ? 0 : typeof av === "number" && typeof bv === "number" ? av - bv : String(av).localeCompare(String(bv), "zh-CN");
    return difference * state.direction || String(a.code || "").localeCompare(String(b.code || ""));
  }
  function stockIdentity(stock) {
    const cell = el("td");
    const button = el("button", "stock-name stock-link", stock.name || "未命名股票");
    button.type = "button";
    button.setAttribute("aria-label", `查看${stock.name || stock.code}的日K`);
    button.addEventListener("click", () => chart.select(stock.code, true));
    cell.append(button, el("span", "stock-code", stock.code || "—"));
    return cell;
  }
  function renderStocks() {
    if (!state.data) return;
    const stocks = list(state.data.stocks);
    const query = byId("stock-search").value.trim().toLocaleLowerCase();
    const sector = byId("pool-filter").value;
    const status = byId("data-filter").value;
    const visible = stocks.filter((stock) => (!query || `${stock.name || ""} ${stock.code || ""}`.toLocaleLowerCase().includes(query)) && (!sector || (stock.sector || "未分类") === sector) && (!status || (status === "available" ? !!stock.bar : !stock.bar))).sort(compareStocks);
    const fragment = document.createDocumentFragment();
    for (const stock of visible) {
      const row = el("tr");
      row.dataset.testid = "stock-row";
      row.dataset.code = stock.code;
      row.append(stockIdentity(stock), el("td", "muted", stock.sector || "未分类"), el("td", "numeric", number(stock.bar?.close, 2)));
      const change = stock.change_pct;
      row.append(el("td", `numeric ${isNumber(change) ? change > 0 ? "rise" : change < 0 ? "fall" : "muted" : "muted"}`, isNumber(change) ? `${change > 0 ? "+" : ""}${number(change, 2)}%` : "—"));
      const volumeCell = el("td", "numeric", formattedVolume(volume(stock)));
      if (isNumber(volume(stock))) volumeCell.title = `${number(volume(stock), 2)}（${isNumber(stock.bar?.volume_shares) ? "股" : "数据源原始单位"}）`;
      row.append(volumeCell);
      for (const field of ["ma5", "ma10", "ma20", "rsi14", "volume_ratio", "score"]) row.append(el("td", `numeric${field === "score" ? " score-value" : ""}`, number(stock.metrics?.[field], field === "score" ? 0 : 2)));
      const statusCell = el("td");
      const status = Object.hasOwn(statusLabels, stock.status) ? stock.status : "missing";
      statusCell.append(el("span", `tag tag-${status}`, statusLabels[status]));
      if (stock.reason) statusCell.append(el("span", "state-detail", displayLabel(stock.reason)));
      if (stock.last_date && stock.last_date !== state.data.as_of) statusCell.append(el("span", "state-detail", `末次数据 ${stock.last_date}`));
      row.append(statusCell);
      fragment.append(row);
    }
    byId("stock-body").replaceChildren(fragment);
    setText("stock-count", number(stocks.length));
    setText("stock-visible-count", `显示 ${number(visible.length)} / ${number(stocks.length)} 支`);
    show("stock-empty-state", visible.length === 0);
    byId("stock-sort").value = state.sort;
    const directionText = state.direction === 1 ? "升序" : "降序";
    setText("sort-direction", state.direction === 1 ? "↑" : "↓");
    byId("sort-direction").setAttribute("aria-label", `当前${directionText}，点击改为${state.direction === 1 ? "降序" : "升序"}`);
    for (const header of document.querySelectorAll("th[data-sort]")) {
      const active = header.dataset.sort === state.sort;
      if (active) header.setAttribute("aria-sort", state.direction === 1 ? "ascending" : "descending");
      else header.removeAttribute("aria-sort");
      header.querySelector(".sort-marker").textContent = active ? state.direction === 1 ? "↑" : "↓" : "";
    }
  }
  function reasonCell(values, risks = false) {
    const cell = el("td");
    const items = list(values);
    if (!items.length) { cell.append(el("span", "muted", "—")); return cell; }
    const container = el("ul", risks ? "reason-list risk-list" : "reason-list");
    for (const value of items) container.append(el("li", "", displayLabel(value)));
    cell.append(container);
    return cell;
  }
  function renderReport(report) {
    state.report = report;
    show("report-loading", false);
    show("report-error-state", false);
    show("report-meta", !!report);
    show("report-notice", false);
    byId("ranking-body").replaceChildren();
    byId("report-diagnostics").replaceChildren();
    show("report-diagnostics", false);
    if (!report) {
      setText("ranking-count", "—");
      setText("ranking-description", "查看已保存的每日机会排名与推荐依据。");
      setText("ranking-footer", "排名以已保存的报告为准");
      byId("ranking-empty-state").querySelector("strong").textContent = "暂无每日排名";
      byId("ranking-empty-state").querySelector("p").textContent = "同步完成并生成报告后，可在这里回看每日筛选结果。";
      show("ranking-empty-state", true);
      show("ranking-table-wrap", false);
      return;
    }
    const modern = report.schema_version >= 2;
    const diagnosis = report.diagnostics?.schema_version === 1 ? report.diagnostics : null;
    const entries = modern ? list(report.ranking) : list(report.selected);
    const recommended = modern ? entries.filter((entry) => entry.recommended === true).length : entries.length;
    const selected = new Set(list(report.selected).map((entry) => entry.code));
    setText("ranking-count", number(entries.length));
    setText("ranking-score-label", modern ? "机会分" : "趋势分");
    setText("ranking-description", modern ? "机会分用于排序，不代表成功概率。推荐状态与入选推送分别展示。" : "历史趋势报告：按报告中的入选顺序展示，分数为趋势分。");
    setText("ranking-footer", modern ? `机会排名 ${number(entries.length)} 支 · 推荐 ${number(recommended)} 支 · 入选推送 ${number(selected.size)} 支` : `历史趋势排名 · 入选推送 ${number(entries.length)} 支`);
    byId("report-meta").replaceChildren(el("span", "", `报告日期 ${report.as_of || "—"}`), el("span", "", `目标交易日 ${report.target_trading_date || "—"}`), el("span", "", `生成于 ${formatTime(report.generated_at)}`));
    if (report.selection_enabled === false) {
      setText("report-notice", "自动筛选已关闭：当前报告不生成入选推送名单，机会排名仅供观察。");
      show("report-notice", true);
    } else if (recommended === 0) {
      setText("report-notice", entries.length ? "本日暂无推荐，以下排名仍可用于观察。" : "本日没有符合条件的推荐股票。");
      show("report-notice", true);
    }
    if (modern && diagnosis) {
      const panel = byId("report-diagnostics");
      const validation = diagnosis.validation;
      panel.append(el("strong", "", validation.summary));
      const failures = el("ul", "reason-list");
      for (const failure of list(validation.failures)) failures.append(el("li", "", failure.message));
      panel.append(failures, el("p", "muted", "历史风险准入要求：规则历史每笔净收益 > -3%。止损触发目标与历史准入分别校验；下方统计针对全股池规则样本。"));
      show("report-diagnostics", true);
    }
    show("ranking-empty-state", !entries.length);
    show("ranking-table-wrap", entries.length > 0);
    byId("ranking-empty-state").querySelector("strong").textContent = "本日暂无排名";
    byId("ranking-empty-state").querySelector("p").textContent = "报告中未保存可展示的排名记录，可切换其他日期查看。";
    const fragment = document.createDocumentFragment();
    entries.forEach((entry, index) => {
      const row = el("tr");
      row.dataset.testid = "ranking-row";
      row.dataset.code = entry.code;
      const rank = modern ? entry.rank : index + 1;
      const rankCell = el("td");
      rankCell.append(el("span", `rank-badge${isNumber(rank) && rank <= 3 ? " rank-top" : ""}`, number(rank)));
      row.append(rankCell, stockIdentity(entry), el("td", "numeric score-value", number(modern ? entry.opportunity_score : entry.metrics?.score, modern ? 1 : 0)));
      const recommendationCell = el("td");
      if (modern) recommendationCell.append(el("span", `tag ${entry.recommended === true ? "tag-recommended" : "tag-observe"}`, entry.recommended === true ? "推荐" : "观察"));
      else recommendationCell.append(el("span", "tag tag-selected", "入选推送"));
      if (modern && selected.has(entry.code)) recommendationCell.append(el("span", "state-detail", "入选推送"));
      row.append(recommendationCell);
      const reasons = reasonCell(entry.reasons);
      if (modern && list(entry.matched_rules).length) reasons.append(el("p", "rule-caption", `匹配规则：${entry.matched_rules.map(displayLabel).join(" / ")}`));
      const detail = diagnosis?.candidates?.[entry.code];
      const riskCell = reasonCell(detail ? detail.risks : entry.risks, true);
      if (detail) {
        for (const rule of list(detail.rules)) {
          const disclosure = el("details", "gate-detail");
          disclosure.append(el("summary", "", `${rule.label} · ${rule.status === "passed" ? "历史检验通过" : rule.status === "failed" ? "历史检验未通过" : "统计缺失"}`));
          disclosure.append(el("p", "rule-caption", rule.summary));
          const checks = el("ul", "reason-list");
          for (const failure of list(rule.failures)) checks.append(el("li", "", failure.message));
          disclosure.append(checks);
          riskCell.append(disclosure);
        }
      }
      row.append(reasons, riskCell);
      fragment.append(row);
    });
    byId("ranking-body").replaceChildren(fragment);
  }
  async function loadReport(date) {
    state.reportController?.abort();
    const requestID = ++state.reportRequest;
    state.reportDate = date;
    state.reportController = new AbortController();
    if (!date) { renderReport(null); return; }
    if (date === state.data?.report?.as_of) { renderReport(state.data.report); return; }
    renderReport(null);
    show("ranking-empty-state", false);
    show("report-loading", true);
    try {
      const report = await requestJSON(`/api/reports?as_of=${encodeURIComponent(date)}`, state.reportController.signal);
      if (requestID !== state.reportRequest) return;
      if (!report || report.as_of !== date || ![1, 2].includes(report.schema_version)) throw new Error("报告日期或版本不匹配，请重新加载。");
      renderReport(report);
    } catch (error) {
      if (requestID !== state.reportRequest || error.name === "AbortError") return;
      setText("report-error-message", error.message);
      show("report-error-state", true);
    } finally {
      if (requestID === state.reportRequest) show("report-loading", false);
    }
  }
  function updateReportDates(data) {
    const dates = [...new Set([...list(data.dates), data.report?.as_of].filter((date) => typeof date === "string" && /^\d{4}-\d{2}-\d{2}$/.test(date)))].sort().reverse();
    const latestDate = data.report?.as_of || dates[0] || "";
    if (!dates.includes(state.reportDate)) state.followLatest = true;
    const selectedDate = state.followLatest ? latestDate : state.reportDate;
    const select = byId("report-date-select");
    select.replaceChildren();
    for (const date of dates) {
      const option = el("option", "", date);
      option.value = date;
      select.append(option);
    }
    if (!dates.length) { const option = el("option", "", "暂无报告"); option.value = ""; select.append(option); }
    select.disabled = !dates.length;
    select.value = selectedDate;
    loadReport(selectedDate);
  }
  async function refreshRuntimeStatus() {
    try {
      const status = await requestJSON("/api/status");
      if (status.schema_version !== 1 || !Array.isArray(status.issues)) throw new Error("运行状态格式不匹配");
      if (!status.ready) throw new Error("存储或交易日历暂不可用");
      const labels = {healthy: "业务正常", degraded: "业务需关注", syncing: "同步处理中"};
      const phases = {collecting: "采集", analyzing: "分析", completed: "完成"};
      const task = status.task;
      const progress = task ? `；${phases[task.phase] || task.phase} ${task.completed}/${task.total}，最近心跳 ${formatTime(task.updated_at)}` : "；尚无任务记录";
      const minutes = status.minute_enabled ? `；分钟数据 ${status.minute_current}/${status.pool_size}（腾讯）` : "；分钟同步未启用";
      const issues = {daily_data_stale_or_incomplete: "日线未更新或覆盖不足", analysis_not_published: "分析尚未发布", task_heartbeat_stale: "任务心跳中断", notification_blocked: "消息发送受阻", notification_unknown: "消息结果未确认", notification_overdue: "消息重试延迟", minute_data_stale_or_incomplete: "分钟数据未更新或未校准"};
      const notes = status.issues.map(item => issues[item] || (item.startsWith("task_failed_") ? "最近任务失败" : item)).join("、");
      setText("runtime-status", `${labels[status.business] || "运行状态未知"} · 应有日线 ${status.expected_date}，可用 ${status.available}/${status.pool_size}${progress}${minutes}${notes ? `；${notes}` : ""}`);
    } catch (error) {
      setText("runtime-status", `运行状态不可用：${error.message}。行情显示不代表后台任务正常。`);
    }
  }
  async function refreshDashboard() {
    if (state.refreshing) return;
    state.refreshing = true;
    refreshRuntimeStatus();
    byId("refresh-button").disabled = true;
    byId("retry-button").disabled = true;
    byId("refresh-button").querySelector("span").textContent = "正在刷新";
    byId("dashboard-content").setAttribute("aria-busy", "true");
    show("loading-state", !state.data);
    try {
      const data = await requestJSON("/api/dashboard");
      if (!data || data.schema_version !== 1 || !data.summary || !Array.isArray(data.stocks)) throw new Error("快照格式不匹配，请检查服务版本。");
      state.data = data;
      renderSummary(data);
      updateSectorOptions(data.stocks);
      renderStocks();
      chart.update(data.stocks, data.adjust);
      updateReportDates(data);
      show("dashboard-content", true);
      show("error-state", false);
      setText("live-status", `快照已刷新，共 ${data.stocks.length} 支股票。`);
    } catch (error) {
      setText("error-title", state.data ? "刷新失败，数据已过期" : "快照加载失败");
      setText("error-message", `${error.message}${state.data ? " 当前保留上次成功读取的快照。" : ""}`);
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
  byId("refresh-button").addEventListener("click", refreshDashboard);
  byId("retry-button").addEventListener("click", refreshDashboard);
  byId("report-retry-button").addEventListener("click", () => loadReport(state.reportDate));
  byId("report-date-select").addEventListener("change", (event) => {
    const latestDate = state.data?.report?.as_of || list(state.data?.dates)[0] || "";
    state.followLatest = event.target.value === latestDate;
    loadReport(event.target.value);
  });
  byId("stock-search").addEventListener("input", renderStocks);
  byId("pool-filter").addEventListener("change", renderStocks);
  byId("data-filter").addEventListener("change", renderStocks);
  byId("view-synced").addEventListener("click", () => {
    byId("stock-search").value = "";
    byId("pool-filter").value = "";
    byId("data-filter").value = "available";
    renderStocks();
    byId("stock-search").focus({ preventScroll: true });
  });
  byId("stock-sort").addEventListener("change", (event) => { state.sort = event.target.value; state.direction = ["code", "name"].includes(state.sort) ? 1 : -1; renderStocks(); });
  byId("sort-direction").addEventListener("click", () => { state.direction *= -1; renderStocks(); });
  byId("clear-filters").addEventListener("click", () => { byId("stock-search").value = ""; byId("pool-filter").value = ""; byId("data-filter").value = ""; renderStocks(); byId("stock-search").focus(); });
  for (const button of document.querySelectorAll("[data-column]")) button.addEventListener("click", () => {
    const field = button.dataset.column;
    state.direction = state.sort === field ? -state.direction : field === "code" ? 1 : -1;
    state.sort = field;
    renderStocks();
  });
  setInterval(() => { if (!document.hidden) refreshDashboard(); }, 60000);
  document.addEventListener("visibilitychange", () => {
    setText("auto-refresh-label", document.hidden ? "页面隐藏，自动刷新已暂停" : "每 60 秒自动刷新");
    if (!document.hidden) refreshDashboard();
  });
  refreshDashboard();
})();
