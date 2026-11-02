"use strict";

(() => {
  const { byId, setText, show, el, list, number, formatTime, requestJSON } = window.StockWatcher;
  const state = { summary: null, request: 0, controller: null };
  const lightLabels = { trend: "趋势", energy: "量能", mid_term: "中期", short_term: "短期" };
  const stateLabels = { red: "红", green: "绿", unknown: "未知" };
  const lifecycleLabels = {
    first_all_red: "首次全红",
    continuing_all_red: "持续全红",
    exit_all_red: "退出全红",
    non_all_red: "非全红",
    unknown: "无法判断",
  };
  const dataLabels = { ready: "就绪", partial: "上一日未知", unknown: "无法判断" };

  function validDate(value) {
    return /^\d{4}-\d{2}-\d{2}$/.test(value || "") && !Number.isNaN(Date.parse(value + "T00:00:00Z"));
  }

  function dateOffset(value, days) {
    const date = new Date(value + "T00:00:00Z");
    date.setUTCDate(date.getUTCDate() + days);
    return date.toISOString().slice(0, 10);
  }

  function lights(record) {
    const stack = el("div", "four-light-lights");
    for (const [key, label] of Object.entries(lightLabels)) {
      const value = record[key] || "unknown";
      stack.append(el("span", `light-chip ${value}`, `${label}${stateLabels[value] || "未知"}`));
    }
    return stack;
  }

  function stockCell(record) {
    const cell = el("td");
    const link = el("a", "stock-name stock-link", record.name || "未命名股票");
    link.href = `/chart?code=${encodeURIComponent(record.code || "")}`;
    cell.append(link, el("span", "stock-code", record.code || "—"));
    return cell;
  }

  function recordRow(record) {
    const row = el("tr");
    row.dataset.testid = "four-light-row";
    row.dataset.code = record.code || "";
    row.dataset.lifecycle = record.lifecycle || "";
    const lightsCell = el("td");
    lightsCell.append(lights(record));
    row.append(stockCell(record), lightsCell,
      el("td", "", lifecycleLabels[record.lifecycle] || record.lifecycle || "—"),
      el("td", "", record.previous_trade_date || "—"),
      el("td", "", dataLabels[record.data_status] || record.data_status || "—"));
    const reason = el("td");
    reason.append(el("div", "four-light-reason", record.reason || "—"));
    const source = [record.source, record.data_field, record.latest_bar_date ? `最新 ${record.latest_bar_date}` : ""].filter(Boolean).join(" · ");
    if (source) reason.append(el("div", "stock-code", source));
    row.append(reason);
    return row;
  }

  function renderRows(id, records, emptyText, reasonMode = false) {
    const body = byId(id);
    body.replaceChildren();
    if (!records.length) {
      const row = el("tr", "four-light-empty-row");
      const cell = el("td", "", emptyText);
      cell.colSpan = 6;
      row.append(cell);
      body.append(row);
      return;
    }
    const fragment = document.createDocumentFragment();
    for (const record of records) {
      const row = recordRow(record);
      if (reasonMode) {
        const reasonCell = row.lastElementChild;
        reasonCell.replaceChildren(el("div", "four-light-reason", record.reason || "原因未记录"));
      }
      fragment.append(row);
    }
    body.append(fragment);
  }

  function summaryTag(id, count) {
    setText(id, String(count));
  }

  function renderSummary(data) {
    state.summary = data;
    const run = data.run || {};
    setText("four-light-total", number(run.total || 0));
    setText("four-light-all-red", number(run.all_red || 0));
    summaryTag("four-light-first-count", run.first_all_red || 0);
    summaryTag("four-light-continuing-count", run.continuing_all_red || 0);
    summaryTag("four-light-exit-count", run.exit_all_red || 0);
    summaryTag("four-light-unknown-count", run.unknown || 0);
    summaryTag("four-light-first-tag", run.first_all_red || 0);
    summaryTag("four-light-continuing-tag", run.continuing_all_red || 0);
    summaryTag("four-light-exit-tag", run.exit_all_red || 0);
    summaryTag("four-light-unknown-tag", run.unknown || 0);
    byId("four-light-date").value = run.as_of || "";
    const meta = byId("four-light-run-meta");
    meta.replaceChildren(
      el("span", "", `规则 ${data.rule}`),
      el("span", "", "近似实现 · 未对齐目标软件"),
      el("span", "", `计算完成 ${formatTime(run.finished_at)}`),
      el("span", "", `数据就绪 ${number(run.ready || 0)} / ${number(run.total || 0)}`),
      el("span", "", `运行 ${run.id || "—"}`),
    );
    renderRows("four-light-first-body", list(data.first_all_red), "当日没有首次全红。");
    renderRows("four-light-continuing-body", list(data.continuing_all_red), "当日没有持续全红。");
    renderRows("four-light-exit-body", list(data.exit_all_red), "当日没有退出全红。");
    renderRows("four-light-unknown-body", list(data.unknown_records), "当日没有无法判断的股票。", true);
    show("four-light-content", true);
  }

  async function loadSummary(asOf = "") {
    const request = ++state.request;
    state.controller?.abort();
    state.controller = new AbortController();
    show("four-light-loading", true);
    show("four-light-error", false);
    show("four-light-empty", false);
    show("four-light-content", false);
    try {
      const query = asOf ? `?as_of=${encodeURIComponent(asOf)}` : "";
      const data = await requestJSON(`/api/four-light${query}`, state.controller.signal);
      if (request !== state.request) return;
      renderSummary(data);
      const first = list(data.first_all_red)[0] || list(data.continuing_all_red)[0] || list(data.exit_all_red)[0];
      byId("four-light-history-from").value = dateOffset(data.run.as_of, -30);
      byId("four-light-history-through").value = data.run.as_of;
      if (first && !byId("four-light-history-code").value) byId("four-light-history-code").value = first.code;
    } catch (error) {
      if (request !== state.request || error.name === "AbortError") return;
      if (String(error.message).includes("four-light signals not found")) {
        byId("four-light-empty-message").textContent = asOf
          ? `${asOf} 没有完整归档；确认全市场日线已同步并运行 four-light-all。`
          : "全市场行情已就绪后，运行 four-light-all 生成第一份归档。";
        show("four-light-empty", true);
      } else {
        byId("four-light-error-message").textContent = error.message || "请稍后重试。";
        show("four-light-error", true);
      }
    } finally {
      if (request === state.request) show("four-light-loading", false);
    }
  }

  async function loadHistory(event) {
    event.preventDefault();
    const code = byId("four-light-history-code").value.trim();
    const from = byId("four-light-history-from").value;
    const through = byId("four-light-history-through").value;
    const status = byId("four-light-history-status");
    status.className = "four-light-history-status";
    if (!code || !validDate(from) || !validDate(through) || from > through) {
      status.className = "four-light-history-status error";
      status.textContent = "请填写股票代码和有效日期区间。";
      return;
    }
    status.textContent = "正在读取个股历史…";
    byId("four-light-history-body").replaceChildren();
    try {
      const data = await requestJSON(`/api/four-light/history?code=${encodeURIComponent(code)}&from=${encodeURIComponent(from)}&through=${encodeURIComponent(through)}&rule=four_light_approx_v1`);
      const records = list(data.records).slice().reverse();
      renderRows("four-light-history-body", records, "该区间没有已归档状态。");
      status.textContent = `${data.code} · ${data.from} 至 ${data.through} · ${records.length} 条状态`;
    } catch (error) {
      if (String(error.message).includes("four-light history not found")) {
        status.textContent = "该区间没有已归档状态。";
      } else {
        status.className = "four-light-history-status error";
        status.textContent = error.message || "历史读取失败。";
      }
    }
  }

  byId("four-light-refresh").addEventListener("click", () => {
    const day = byId("four-light-date").value;
    if (day && !validDate(day)) {
      byId("four-light-error-message").textContent = "日期必须是 YYYY-MM-DD。";
      show("four-light-error", true);
      return;
    }
    loadSummary(day);
  });
  byId("four-light-retry").addEventListener("click", () => loadSummary(byId("four-light-date").value));
  byId("four-light-history-form").addEventListener("submit", loadHistory);
  const requested = new URLSearchParams(location.search).get("as_of");
  loadSummary(validDate(requested) ? requested : "");
})();
