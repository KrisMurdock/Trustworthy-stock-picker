"use strict";

(() => {
  const { byId, setText, show, el, list, number, isNumber, displayLabel, formatTime, stockIdentity, loadDashboard, requestJSON } = window.StockWatcher;
  const state = { data: null, report: null, reportDate: "", followLatest: true, refreshing: false, reportRequest: 0, reportController: null, predictor: null };
  const requested = new URLSearchParams(location.search).get("as_of");
  if (/^\d{4}-\d{2}-\d{2}$/.test(requested || "")) {
    state.reportDate = requested;
    state.followLatest = false;
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
    setText("ranking-description", modern ? "机会分用于排序，不代表成功概率。延续分为独立的上涨日次日观察指标，暂不计入机会分；推荐状态与入选推送分别展示。" : "历史趋势报告：按报告中的入选顺序展示，分数为趋势分。");
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
      const scoreCell = el("td", "numeric score-value", number(modern ? entry.opportunity_score : entry.metrics?.score, modern ? 1 : 0));
      if (modern && entry.continuation) {
        const continuation = entry.continuation;
        let label = "延续分样本不足";
        if (continuation.status === "ready") label = `延续分 ${number(continuation.score, 1)}`;
        else if (continuation.status === "not_applicable") label = "延续分 —（今日未涨）";
        const detail = el("span", "state-detail", label);
        if (continuation.status === "ready") {
          detail.title = `历史同类上涨信号：次日收盘高于信号日收盘 ${number(continuation.rate * 100, 1)}%，保守下界 ${number(continuation.lower_bound * 100, 1)}%，${number(continuation.samples)} 笔 / ${number(continuation.signal_dates)} 个信号日，规则 ${displayLabel(continuation.rule)}`;
        }
        scoreCell.append(detail);
      }
      row.append(rankCell, stockIdentity(entry), scoreCell);
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
      if (!report || report.as_of !== date || ![1, 2, 3].includes(report.schema_version)) throw new Error("报告日期或版本不匹配，请重新加载。");
      renderReport(report);
    } catch (error) {
      if (requestID !== state.reportRequest || error.name === "AbortError") return;
      setText("report-error-message", error.message);
      show("report-error-state", true);
    } finally {
      if (requestID === state.reportRequest) show("report-loading", false);
    }
  }

  function syncURL(date, latestDate) {
    const url = new URL(location.href);
    if (date && date !== latestDate) url.searchParams.set("as_of", date);
    else url.searchParams.delete("as_of");
    history.replaceState(null, "", url.pathname + (url.search || ""));
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
    syncURL(selectedDate, latestDate);
    loadReport(selectedDate);
  }

  function shanghaiDate() {
    return new Intl.DateTimeFormat("en-CA", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit" }).format(new Date());
  }

  function targetClock(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
  }

  function percentText(value) {
    if (!isNumber(value)) return "—";
    return `${value > 0 ? "+" : ""}${(value * 100).toFixed(2)}%`;
  }

  async function predictorRequest(path, init = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch(path, {
        ...init, signal: controller.signal, cache: "no-store",
        headers: { Accept: "application/json", ...init.headers },
      });
      let payload = null;
      try { payload = await response.json(); } catch (error) { if (controller.signal.aborted) throw error; }
      return { response, payload };
    } finally {
      clearTimeout(timer);
    }
  }

  function predictorError(message) {
    setText("predictor-notice", message);
    show("predictor-notice", true);
  }

  function renderPredictorView(view) {
    state.predictor = view;
    const toggle = byId("predictor-push-toggle");
    toggle.checked = view.push_enabled === true;
    toggle.disabled = view.write_enabled !== true;
    setText("predictor-push-state", view.push_enabled ? "已开启" : "已关闭");
    setText("predictor-footer", `模型 ${view.model_id || "尚未训练"} · 有效期 ${view.model_valid_for || "—"} · 更新 ${formatTime(view.updated_at)}`);
  }

  function renderPredictions(predictions) {
    const rows = list(predictions);
    byId("predictor-body").replaceChildren();
    show("predictor-notice", false);
    if (!rows.length) {
      setText("predictor-count", "0");
      show("predictor-empty", true);
      show("predictor-table-wrap", false);
      return;
    }
    const latest = Math.max(...rows.map((prediction) => new Date(prediction.target_end).getTime()));
    const current = rows.filter((prediction) => new Date(prediction.target_end).getTime() === latest);
    const names = new Map();
    for (const entry of list(state.report?.selected)) names.set(entry.code, entry.name);
    for (const entry of list(state.report?.ranking)) names.set(entry.code, entry.name);
    const fragment = document.createDocumentFragment();
    for (const prediction of current) {
      const row = el("tr");
      row.dataset.testid = "predictor-row";
      row.dataset.code = prediction.code;
      row.append(stockIdentity({ code: prediction.code, name: names.get(prediction.code) || prediction.code }));
      row.append(el("td", "numeric", number(prediction.base_price, 2)));
      for (const key of ["high_pct", "low_pct", "close_pct"]) {
        const price = prediction.base_price * (1 + prediction[key]);
        const cell = el("td", "numeric", `${number(price, 2)} (${percentText(prediction[key])})`);
        if (prediction[key] > 0) cell.classList.add("chart-rise");
        if (prediction[key] < 0) cell.classList.add("chart-fall");
        row.append(cell);
      }
      row.append(el("td", "", prediction.trend || "—"), el("td", "", prediction.confidence || "—"), el("td", "", targetClock(prediction.target_end)));
      fragment.append(row);
    }
    byId("predictor-body").replaceChildren(fragment);
    setText("predictor-count", number(current.length));
    setText("predictor-description", `今日入选股票的下一根 5 分钟 K 线预测，目标时间 ${targetClock(current[0].target_end)}。`);
    show("predictor-empty", false);
    show("predictor-table-wrap", true);
  }

  async function loadPredictor() {
    try {
      const { response, payload } = await predictorRequest("/api/predictor");
      if (response.status === 503) { show("predictor", false); return; }
      if (!response.ok || !payload || payload.enabled !== true) throw new Error(payload?.error || `预测状态读取失败（HTTP ${response.status}）`);
      show("predictor", true);
      renderPredictorView(payload);
      const history = await predictorRequest(`/api/predictions?date=${encodeURIComponent(shanghaiDate())}`);
      if (!history.response.ok || !history.payload) throw new Error(history.payload?.error || `预测数据读取失败（HTTP ${history.response.status}）`);
      renderPredictions(history.payload.predictions);
    } catch (error) {
      if (error.name === "AbortError") return;
      show("predictor", true);
      predictorError(`5 分钟预测暂不可用：${error.message}`);
    }
  }

  async function togglePredictorPush(event) {
    const toggle = event.target;
    const target = toggle.checked;
    toggle.disabled = true;
    setText("predictor-push-state", "保存中");
    try {
      const { response, payload } = await predictorRequest("/api/predictor/config", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-Stock-Watcher-Action": "predictor-config" },
        body: JSON.stringify({ push_enabled: target }),
      });
      if (!response.ok || !payload) throw new Error(payload?.error || `开关保存失败（HTTP ${response.status}）`);
      renderPredictorView(payload);
      setText("live-status", target ? "5 分钟预测飞书推送已开启。" : "5 分钟预测飞书推送已关闭，预测仍会保存。");
      show("predictor-notice", false);
    } catch (error) {
      toggle.checked = !target;
      setText("predictor-push-state", target ? "已关闭" : "已开启");
      predictorError(`推送开关未保存：${error.message}`);
    } finally {
      toggle.disabled = state.predictor?.write_enabled !== true;
    }
  }

  async function refreshRankings() {
    if (state.refreshing) return;
    state.refreshing = true;
    byId("refresh-button").disabled = true;
    byId("retry-button").disabled = true;
    byId("refresh-button").querySelector("span").textContent = "正在刷新";
    byId("dashboard-content").setAttribute("aria-busy", "true");
    show("loading-state", !state.data);
    try {
      const data = await loadDashboard();
      state.data = data;
      updateReportDates(data);
      show("dashboard-content", true);
      show("error-state", false);
      void loadPredictor();
      setText("live-status", `报告日期已刷新，共 ${data.stocks.length} 支股票。`);
    } catch (error) {
      setText("error-title", state.data ? "刷新失败，数据已过期" : "排名报告加载失败");
      setText("error-message", `${error.message}${state.data ? " 当前保留上次成功读取的排名。" : ""}`);
      show("error-state", true);
    } finally {
      state.refreshing = false;
      byId("refresh-button").disabled = false;
      byId("retry-button").disabled = false;
      byId("refresh-button").querySelector("span").textContent = "刷新排名";
      byId("dashboard-content").setAttribute("aria-busy", "false");
      show("loading-state", false);
    }
  }

  byId("refresh-button").addEventListener("click", refreshRankings);
  byId("retry-button").addEventListener("click", refreshRankings);
  byId("report-retry-button").addEventListener("click", () => loadReport(state.reportDate));
  byId("report-date-select").addEventListener("change", (event) => {
    const latestDate = state.data?.report?.as_of || list(state.data?.dates)[0] || "";
    state.followLatest = event.target.value === latestDate;
    state.reportDate = event.target.value;
    syncURL(event.target.value, latestDate);
    loadReport(event.target.value);
  });
  byId("predictor-push-toggle").addEventListener("change", togglePredictorPush);
  setInterval(() => { if (!document.hidden) refreshRankings(); }, 60000);
  document.addEventListener("visibilitychange", () => {
    setText("auto-refresh-label", document.hidden ? "页面隐藏，自动刷新已暂停" : "每 60 秒自动刷新报告列表");
    if (!document.hidden) refreshRankings();
  });
  refreshRankings();
})();
