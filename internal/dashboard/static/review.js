"use strict";

(() => {
  const { byId, el, list, number, isNumber, formatTime, displayLabel, show, setText } = window.StockWatcher;
  const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;
  const REVISION_PATTERN = /^[0-9a-f]{64}$/;
  const CODE_PATTERN = /^(sh|sz)\d{6}$/;
  const REVIEW_LABELS = {
    mature: "已成熟",
    pending: "未成熟",
    not_entered: "未入场",
    missing_data: "缺数据",
    unsupported: "不支持复盘",
    target: "目标止盈",
    stop: "止损",
    stop_gap: "跳空止损",
    timeout: "到期退出",
    legacy_report_has_no_execution_review: "旧报告未保存执行复盘所需输入",
    frozen_report_parameters_missing: "旧报告缺少冻结参数，不支持完整复盘",
    calendar_unavailable: "交易日历不可用，部分路径无法判定",
    frozen_report_input_missing: "冻结报告输入缺失",
    frozen_signal_bar: "冻结信号日线",
    signal_bar_missing: "信号日线缺失",
    signal_bar: "信号日线",
    missing_signal_bar: "缺失冻结信号日线",
    pass: "通过",
    fail: "否决",
    unknown: "未知",
    shadow: "影子模式",
    enforce: "正式门控",
    ok: "正常",
    insufficient: "数据不足",
    invalid: "数据无效",
    not_evaluated: "未评估",
    blocked: "已阻断",
    report: "报告快照",
    candidate: "候选依据",
    outcome: "模拟结果",
    checkpoints: "T+1／T+3／T+5",
    structure_guard: "结构门控",
    missing: "缺失项",
  };

  const state = {
    dashboard: null,
    dates: [],
    latestDate: "",
    asOf: "",
    revision: "",
    requestedDate: "",
    requestedCode: "",
    requestedRevision: "",
    day: null,
    selectedCode: "",
    currentEvent: null,
    dayController: null,
    dayRequest: 0,
    eventController: null,
    eventRequest: 0,
    refreshing: false,
    initialized: false,
    lastRefreshAt: 0,
    master: {
      drawerOpen: false,
      trigger: null,
      roles: [],
      roleId: "",
      defaultRole: "",
      rolesError: "",
      threads: {},
      stream: null,
      lastChain: null,
      run: 0,
    },
    analysis: {
      stream: null,
      view: null,
      run: 0,
    },
    llm: {
      mode: "loading",
      config: null,
      error: "",
      loading: false,
      settingsTrigger: null,
    },
  };

  const finite = (value) => typeof value === "number" && Number.isFinite(value);
  const count = (value) => Number.isInteger(value) && value >= 0 ? number(value) : "—";
  const price = (value) => finite(value) ? number(value, 2) : "—";
  const pct = (value, signed = true) => {
    if (!finite(value)) return "—";
    const prefix = signed && value > 0 ? "+" : "";
    return `${prefix}${number(value, 2)}%`;
  };
  const rate = (value) => finite(value) ? `${number(value * 100, 2)}%` : "—";
  const volume = (value) => {
    if (!finite(value)) return "—";
    if (Math.abs(value) >= 100000000) return `${number(value / 100000000, 2)} 亿`;
    if (Math.abs(value) >= 10000) return `${number(value / 10000, 2)} 万`;
    return number(value);
  };
  const textValue = (value, fallback = "—") => typeof value === "string" && value.trim() ? value : fallback;
  const label = (value) => {
    if (typeof value !== "string" || !value) return "—";
    return REVIEW_LABELS[value] || displayLabel(value) || value;
  };
  const shortRevision = (value) => typeof value === "string" && value.length > 16 ? `${value.slice(0, 12)}…${value.slice(-4)}` : textValue(value);
  const dedupe = (values) => [...new Set(list(values).filter((item) => typeof item === "string" && item.trim()))];
  const isVisible = (node) => !!node && !node.hidden && node.getClientRects().length > 0;
  const safeMessage = (value) => {
    let message = typeof value === "string" ? value.trim() : "";
    const enteredSecret = byId("llm-api-key")?.value?.trim();
    if (enteredSecret && enteredSecret.length >= 4) message = message.split(enteredSecret).join("[已隐藏]");
    message = message
      .replace(/Bearer\s+[^\s,;]+/gi, "Bearer [已隐藏]")
      .replace(/sk-[A-Za-z0-9_-]{8,}/g, "[已隐藏]")
      .replace(/api[_-]?key\s*[:=]\s*\S+/gi, "api_key=[已隐藏]");
    return message ? message.slice(0, 500) : "请求失败，请稍后重试。";
  };
  const jsonText = (value) => {
    try {
      const rendered = JSON.stringify(value, null, 2);
      return typeof rendered === "string" ? rendered : String(value ?? "—");
    } catch (_) {
      return String(value ?? "—");
    }
  };
  const addLive = (message) => setText("review-live-status", message);

  function stateTag(eventState) {
    const stateValue = typeof eventState === "string" ? eventState : "unsupported";
    const classes = { mature: "state-mature", pending: "state-pending", not_entered: "state-not-entered", missing_data: "state-missing-data", unsupported: "state-unsupported" };
    return el("span", `review-state ${classes[stateValue] || "state-unsupported"}`, label(stateValue));
  }

  function numberClass(value) {
    if (!finite(value) || value === 0) return "";
    return value > 0 ? "positive" : "negative";
  }

  async function fetchJSON(url, options = {}) {
    const controller = new AbortController();
    const sourceSignal = options.signal;
    const abort = () => controller.abort();
    if (sourceSignal?.aborted) controller.abort();
    sourceSignal?.addEventListener("abort", abort, { once: true });
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, options.timeout || 15000);
    try {
      const headers = { Accept: "application/json", ...(options.headers || {}) };
      if (options.body !== undefined) headers["Content-Type"] = "application/json";
      const response = await fetch(url, {
        method: options.method || "GET",
        headers,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
        signal: controller.signal,
        cache: "no-store",
        credentials: "same-origin",
      });
      const raw = await response.text();
      let payload = null;
      if (raw) {
        try { payload = JSON.parse(raw); } catch (_) { payload = null; }
      }
      if (!response.ok) {
        let message = payload?.error || payload?.message || raw;
        if (response.status === 401 || response.status === 403) message = "当前访问未获授权，请检查访问权限或网关配置。";
        else if (response.status === 404) message = options.notFound || "请求的数据不存在。";
        else if (response.status === 429) message = "请求过于频繁，请稍后重试。";
        else if (response.status >= 500) message = options.serverError || "服务暂时不可用，请稍后重试。";
        throw new Error(safeMessage(message));
      }
      return payload;
    } catch (error) {
      if (timedOut) throw new Error("请求超时，请检查服务状态后重试。");
      if (error.name === "AbortError") throw error;
      if (error instanceof TypeError) throw new Error("无法连接服务，请检查网络连接后重试。");
      throw error;
    } finally {
      clearTimeout(timer);
      sourceSignal?.removeEventListener("abort", abort);
    }
  }

  function setRefreshBusy(busy) {
    const button = byId("review-refresh");
    button.disabled = busy;
    button.querySelector("span").textContent = busy ? "正在刷新" : "刷新复盘";
  }

  function showPageError(message, title = "复盘快照加载失败") {
    setText("review-error-title", title);
    setText("review-error-message", safeMessage(message));
    show("review-error", true);
    show("review-loading", false);
    show("review-empty", false);
  }

  function showEmptyReport(message) {
    if (message) byId("review-empty").querySelector("p").textContent = message;
    show("review-empty", true);
    show("review-error", false);
    show("review-loading", false);
    show("review-content", false);
    byId("review-date-select").disabled = true;
    byId("review-revision-select").disabled = true;
  }

  function extractDashboardDates(data) {
    const reportDate = data?.report?.as_of;
    const dates = [...new Set([...list(data?.dates), reportDate, state.requestedDate].filter((value) => typeof value === "string" && DATE_PATTERN.test(value)))].sort().reverse();
    return { reportDate: DATE_PATTERN.test(reportDate || "") ? reportDate : dates[0] || "", dates };
  }

  function updateDateSelect() {
    const select = byId("review-date-select");
    select.replaceChildren();
    if (!state.dates.length) {
      const option = el("option", "", "暂无报告");
      option.value = "";
      select.append(option);
      select.disabled = true;
      return;
    }
    for (const date of state.dates) {
      const option = el("option", "", date);
      option.value = date;
      select.append(option);
    }
    select.disabled = false;
    select.value = state.asOf;
  }

  function prepareDashboard(data) {
    state.dashboard = data;
    const extracted = extractDashboardDates(data);
    state.dates = extracted.dates;
    state.latestDate = extracted.reportDate;
    if (!state.dates.length || !state.latestDate) return false;
    if (!state.asOf || !state.dates.includes(state.asOf)) state.asOf = state.latestDate;
    updateDateSelect();
    return true;
  }

  function syncURL() {
    const url = new URL(location.href);
    if (state.asOf) url.searchParams.set("as_of", state.asOf);
    else url.searchParams.delete("as_of");
    if (state.selectedCode) url.searchParams.set("code", state.selectedCode);
    else url.searchParams.delete("code");
    if (state.revision) url.searchParams.set("revision", state.revision);
    else url.searchParams.delete("revision");
    history.replaceState(null, "", url.pathname + (url.search ? url.search : ""));
  }

  function resetContext(reason) {
    state.selectedCode = "";
    state.currentEvent = null;
    clearDetail();
    resetAnalysis(true);
    clearMasterThreads(reason);
    syncURL();
  }

  function clearDetail() {
    state.currentEvent = null;
    byId("review-detail-body").replaceChildren();
    show("review-detail-body", false);
    show("review-detail-loading", false);
    show("review-detail-error", false);
    show("review-detail-empty", true);
    setText("review-detail-description", "默认不选中事件，避免先入为主。");
    show("review-detail-state", false);
  }

  function renderDayMeta(day) {
    setText("review-data-as-of", textValue(day.data_as_of));
    const revision = byId("review-report-revision");
    revision.textContent = textValue(day.revision);
    revision.title = textValue(day.revision);
    setText("review-generated-at", formatTime(day.generated_at));
    setText("review-target-date", textValue(day.target_trading_date));
    setText("review-strategy", textValue(day.strategy));
    setText("review-snapshot-note", day.data_as_of
      ? `冻结报告生成于 ${formatTime(day.generated_at)}；之后路径截至本地行情 ${day.data_as_of}。`
      : "冻结报告已载入；尚未取得可用于复盘路径的后续本地行情。");
    const calendarBits = [];
    if (day.calendar_from && day.calendar_through) calendarBits.push(`交易日历 ${day.calendar_from} 至 ${day.calendar_through}`);
    if (day.calendar_source) calendarBits.push(`来源 ${day.calendar_source}`);
    setText("review-calendar-note", calendarBits.join(" · "));

    const warnings = list(day.warnings).map((warning) => label(warning));
    const aggregate = day.aggregate || {};
    const all = aggregate.all || {};
    if (finite(all.missing) && all.missing > 0) warnings.push(`${count(all.missing)} 个事件缺少数据，不能计入成熟样本。`);
    if (finite(all.unsupported) && all.unsupported > 0) warnings.push(`${count(all.unsupported)} 个事件不支持执行复盘。`);
    if (finite(all.pending) && all.pending > 0) warnings.push(`${count(all.pending)} 个事件尚未成熟，路径仍可能变化。`);
    const notice = byId("review-warnings");
    notice.replaceChildren();
    if (warnings.length) {
      notice.append(el("strong", "", "部分数据与限制"));
      const items = el("ul");
      for (const warning of dedupe(warnings)) items.append(el("li", "", warning));
      notice.append(items);
    }
    show("review-warnings", warnings.length > 0);
  }

  function createCountCard(title, metric, detail) {
    const card = el("article", "review-count-card");
    card.append(el("span", "", title), el("strong", "", count(metric?.total)), el("small", "", detail(metric || {})));
    return card;
  }

  function metricCell(metric, key, kind) {
    const value = metric?.[key];
    const cell = el("span", "review-metric-value");
    cell.setAttribute("role", "cell");
    if (kind === "return") {
      if (key === "mean_net_pct") {
        cell.append(el("span", `review-metric-number ${numberClass(value)}`, pct(value)));
      } else if (key === "positive_rate") {
        cell.append(el("span", "review-metric-number", rate(value)));
      } else if (key === "worst_max_adverse_net_pct") {
        cell.append(el("span", `review-metric-number ${numberClass(value)}`, pct(value)));
      } else {
        cell.append(el("span", "review-metric-number", count(value)));
      }
      if (key !== "loss_breach") {
        const mature = metric?.mature;
        cell.append(el("span", "review-metric-sample", `N=${count(mature)}`));
        if (finite(mature) && mature < 10) cell.append(el("span", "review-sample-short", "样本不足"));
      }
      if (!finite(value)) cell.prepend(el("span", "sr-only", "未知值"));
    } else {
      cell.textContent = count(value);
    }
    return cell;
  }

  function appendMetricRow(container, title, key, kind = "count", emphasis = false) {
    const row = el("div", `review-metric-row${emphasis ? " is-emphasis" : ""}`);
    row.setAttribute("role", "row");
    const labelCell = el("span", "review-metric-label", title);
    labelCell.setAttribute("role", "cell");
    row.append(labelCell, metricCell(state.day?.aggregate?.recommended, key, kind), metricCell(state.day?.aggregate?.observed, key, kind));
    container.append(row);
  }

  function appendMetricGroup(container, title, rows) {
    const groupTitle = el("div", "review-metric-group-title", title);
    groupTitle.setAttribute("role", "row");
    container.append(groupTitle);
    for (const row of rows) appendMetricRow(container, ...row);
  }

  function renderAggregate(day) {
    const aggregate = day.aggregate || {};
    const recommended = aggregate.recommended || {};
    const observed = aggregate.observed || {};
    const all = aggregate.all || {};
    const body = byId("review-aggregate-body");
    body.replaceChildren();

    const cards = el("div", "review-count-cards");
    const recommendedCard = createCountCard("推荐事件", recommended, (metric) => `已成熟 ${count(metric.mature)} · 未成熟 ${count(metric.pending)}`);
    recommendedCard.classList.add("is-recommended");
    cards.append(
      recommendedCard,
      createCountCard("观察事件", observed, (metric) => `已成熟 ${count(metric.mature)} · 未入场 ${count(metric.not_entered)}`),
      createCountCard("全部事件", all, (metric) => `已入场 ${count(metric.entered)} · 缺数据 ${count(metric.missing)}`),
    );

    const metrics = el("div", "review-metrics");
    metrics.setAttribute("role", "table");
    metrics.setAttribute("aria-label", "推荐与观察事件聚合指标");
    const head = el("div", "review-metric-head");
    head.setAttribute("role", "row");
    for (const title of ["指标", "推荐", "观察"]) {
      const cell = el("span", "", title);
      cell.setAttribute("role", "columnheader");
      head.append(cell);
    }
    metrics.append(head);
    appendMetricGroup(metrics, "样本状态", [
      ["已成熟", "mature", "count"],
      ["未成熟", "pending", "count"],
      ["未入场", "not_entered", "count"],
      ["缺数据", "missing", "count"],
      ["不支持复盘", "unsupported", "count"],
      ["已入场", "entered", "count"],
    ]);
    appendMetricGroup(metrics, "退出结果", [
      ["目标止盈", "target", "count"],
      ["止损", "stop", "count"],
      ["跳空止损", "stop_gap", "count"],
      ["到期退出", "timeout", "count"],
    ]);
    appendMetricGroup(metrics, "收益与风险", [
      ["平均净收益", "mean_net_pct", "return"],
      ["正收益比例", "positive_rate", "return"],
      ["最差 MAE", "worst_max_adverse_net_pct", "return", true],
      ["亏损超限", "loss_breach", "return"],
      ["顺序不明", "ambiguous", "count"],
    ]);
    body.append(cards, metrics);
    setText("review-event-count", count(list(day.events).length));
    setText("review-aggregate-note", "收益指标只统计已成熟样本；MAE 为模拟持仓期内的最深净浮亏，N<10 标记为样本不足。");
    show("review-aggregate", true);
  }

  function recommendationTags(event) {
    const wrap = el("div", "review-recommendation");
    wrap.append(el("span", `tag ${event.recommended === true ? "tag-recommended" : "tag-observe"}`, event.recommended === true ? "推荐" : "观察"));
    if (event.daily_recommended === true && event.recommended !== true) wrap.append(el("span", "tag tag-neutral", "日线推荐"));
    if (event.eligible_recommended === true && event.recommended !== true) wrap.append(el("span", "tag tag-eligible", "结构合格"));
    return wrap;
  }

  function eventRisks(event) {
    const risks = [];
    if (event.ambiguous === true) risks.push("日内顺序不明");
    if (event.state_reason) risks.push(label(event.state_reason));
    if (event.exit_reason && event.exit_reason !== "target") risks.push(label(event.exit_reason));
    return dedupe(risks);
  }

  function riskCell(event) {
    const cell = el("td", "review-risk-cell");
    const risks = eventRisks(event);
    if (!risks.length) {
      cell.append(el("span", "muted", "—"));
    } else {
      const items = el("ul");
      for (const risk of risks) items.append(el("li", "", risk));
      cell.append(items);
    }
    if (event.daily_recommended === true || event.eligible_recommended === true) {
      const tiers = [];
      if (event.daily_recommended === true) tiers.push("日线推荐");
      if (event.eligible_recommended === true) tiers.push("结构合格");
      cell.append(el("span", "review-tier-note", tiers.join(" · ")));
    }
    return cell;
  }

  function createEventRow(event, index) {
    const row = el("tr");
    row.dataset.code = event.code || "";
    row.dataset.testid = "review-event-row";
    row.setAttribute("aria-selected", state.selectedCode === event.code ? "true" : "false");
    if (state.selectedCode === event.code) row.classList.add("is-selected");

    const rankCell = el("td");
    rankCell.append(el("span", `rank-badge${isNumber(event.rank) && event.rank > 0 && event.rank <= 3 ? " rank-top" : ""}`, isNumber(event.rank) && event.rank > 0 ? number(event.rank) : "—"));
    const stockCell = el("td");
    const button = el("button", "review-event-link", textValue(event.name, event.code || "未命名股票"));
    button.type = "button";
    button.dataset.code = event.code || "";
    button.setAttribute("aria-label", `查看 ${textValue(event.name, event.code || "该股票")} 的复盘详情`);
    const code = el("span", "stock-code", textValue(event.code));
    stockCell.append(button, code);
    const stateCell = el("td");
    stateCell.append(stateTag(event.state));
    const netCell = el("td", `numeric review-net ${numberClass(event.net_pct)}`, pct(event.net_pct));
    row.append(
      rankCell,
      stockCell,
      (() => { const cell = el("td"); cell.append(recommendationTags(event)); return cell; })(),
      el("td", "", textValue(event.signal_date)),
      el("td", "", textValue(event.entry_date)),
      el("td", "", textValue(event.exit_date)),
      stateCell,
      netCell,
      riskCell(event),
    );
    row.addEventListener("click", () => selectEvent(event.code, { updateURL: true }));
    button.addEventListener("click", (clickEvent) => {
      clickEvent.stopPropagation();
      selectEvent(event.code, { updateURL: true, focusDetail: false });
    });
    row.dataset.index = String(index);
    return row;
  }

  function markSelectedRow() {
    for (const row of byId("review-event-body").querySelectorAll("tr[data-code]")) {
      const selected = row.dataset.code === state.selectedCode;
      row.classList.toggle("is-selected", selected);
      row.setAttribute("aria-selected", selected ? "true" : "false");
    }
  }

  function renderEvents(day) {
    const events = list(day.events);
    const body = byId("review-event-body");
    body.replaceChildren();
    setText("review-events-count", count(events.length));
    show("review-events-loading", false);
    show("review-events-empty", events.length === 0);
    show("review-events", events.length > 0);
    if (!events.length) return;
    const fragment = document.createDocumentFragment();
    events.forEach((event, index) => fragment.append(createEventRow(event, index)));
    body.append(fragment);
  }

  function updateRevisionSelect(day, requestedRevision) {
    const select = byId("review-revision-select");
    const revisions = list(day.revisions).filter((revision) => revision && REVISION_PATTERN.test(revision.revision || ""));
    select.replaceChildren();
    setText("review-revision-count", `${count(revisions.length)} 个修订`);
    if (!revisions.length) {
      const option = el("option", "", "暂无修订");
      option.value = "";
      select.append(option);
      select.disabled = true;
      return;
    }
    const sorted = revisions.slice().sort((a, b) => String(b.generated_at || "").localeCompare(String(a.generated_at || "")));
    const desired = requestedRevision || day.revision || sorted[0].revision;
    let found = false;
    for (const revision of sorted) {
      const option = el("option", "", `${shortRevision(revision.revision)}${revision.revision === day.revision ? " · 当前" : ""}${revision.generated_at ? ` · ${formatTime(revision.generated_at)}` : ""}`);
      option.value = revision.revision;
      option.title = revision.revision;
      if (revision.revision === desired) {
        option.selected = true;
        found = true;
      }
      select.append(option);
    }
    if (!found) {
      const option = el("option", "", "当前报告（未列入修订列表）");
      option.value = "";
      select.prepend(option);
      select.value = "";
    } else {
      select.value = desired;
    }
    select.disabled = false;
  }

  function eventByCode(code) {
    return list(state.day?.events).find((event) => event.code === code) || null;
  }

  function resetAnalysis(cancel) {
    const active = state.analysis.stream;
    if (active) {
      active.view.silenced = true;
      if (cancel) active.controller.abort();
    }
    state.analysis.run += 1;
    state.analysis.stream = null;
    state.analysis.view = null;
    byId("review-analysis-output").replaceChildren();
    byId("review-analysis-output").append(
      (() => {
        const message = el("div", "master-message assistant");
        message.append(
          el("span", "master-message-role", "AI 复盘归因"),
          (() => { const answer = el("div", "master-answer-text"); answer.id = "review-analysis-answer"; answer.setAttribute("aria-live", "polite"); return answer; })(),
          (() => { const slot = el("div"); slot.id = "review-analysis-chain-slot"; return slot; })(),
          (() => { const citations = el("p", "citation-status"); citations.id = "review-analysis-citations"; citations.setAttribute("role", "status"); return citations; })(),
        );
        return message;
      })(),
    );
    show("review-analysis-output", false);
    const status = byId("review-analysis-state");
    status.className = "review-analysis-state";
    status.textContent = "";
    show("review-analysis-state", false);
    updateAIReadiness();
  }

  function renderReviews(day) {
    renderDayMeta(day);
    renderAggregate(day);
    renderEvents(day);
    updateRevisionSelect(day, state.revision);
    show("review-content", true);
    show("review-empty", false);
    show("review-error", false);
    show("review-loading", false);
  }

  async function loadDay(asOf, revision, options = {}) {
    state.dayController?.abort();
    const request = ++state.dayRequest;
    state.dayController = new AbortController();
    state.asOf = asOf;
    state.revision = revision || "";
    const previousDay = state.day;
    if (!previousDay || previousDay.as_of !== asOf || (previousDay.revision || "") !== state.revision) {
      state.day = null;
      state.currentEvent = null;
      show("review-detail-body", false);
      show("review-detail-empty", true);
      show("review-detail-error", false);
    }
    byId("review-date-select").disabled = true;
    byId("review-revision-select").disabled = true;
    show("review-events-loading", true);
    if (!state.day) show("review-loading", true);
    const query = new URLSearchParams({ as_of: asOf });
    if (revision) query.set("revision", revision);
    try {
      const day = await fetchJSON(`/api/review?${query.toString()}`, {
        signal: state.dayController.signal,
        timeout: 16000,
        notFound: "所选日期没有可用的复盘报告。",
        serverError: "复盘数据暂时不可用，请稍后重试。",
      });
      if (request !== state.dayRequest) return false;
      if (!day || day.schema_version !== 1 || day.as_of !== asOf || !Array.isArray(day.events) || !day.aggregate) {
        throw new Error("复盘数据格式不匹配，请检查服务版本。");
      }
      state.day = day;
      renderReviews(day);
      const focusCode = options.focusCode || "";
      if (focusCode) {
        const event = eventByCode(focusCode);
        if (event) await selectEvent(focusCode, { updateURL: false });
        else {
          clearDetail();
          setText("review-detail-description", "地址中的股票不在当前复盘事件中。");
        }
      } else {
        clearDetail();
      }
      syncURL();
      updateAIReadiness();
      state.lastRefreshAt = Date.now();
      return true;
    } catch (error) {
      if (request === state.dayRequest && !state.day) {
        show("review-aggregate", false);
        byId("review-event-body").replaceChildren();
        show("review-events", false);
        show("review-events-empty", false);
        show("review-content", false);
      }
      throw error;
    } finally {
      if (request === state.dayRequest) {
        show("review-events-loading", false);
        if (state.day) {
          byId("review-date-select").disabled = !state.dates.length;
          byId("review-revision-select").disabled = !list(state.day.revisions).length;
        }
      }
    }
  }

  async function refreshReview(options = {}) {
    if (state.refreshing) return;
    state.refreshing = true;
    setRefreshBusy(true);
    if (!state.day) {
      show("review-loading", true);
      show("review-error", false);
    }
    try {
      const data = await fetchJSON("/api/dashboard", { timeout: 10000, notFound: "看板快照不存在。" });
      state.dashboard = data;
      if (!prepareDashboard(data)) {
        showEmptyReport("还没有保存的报告日期。完成一次同步并生成报告后再来复盘。");
        state.day = null;
        return;
      }
      if (state.currentEvent) state.currentEvent = null;
      const oldAsOf = options.initial ? null : state.day?.as_of;
      const focusCode = state.selectedCode;
      const revision = state.revision;
      await loadDay(state.asOf, revision, { focusCode });
      if (oldAsOf && oldAsOf !== state.asOf) resetContext("复盘日期已变化，已开启新的对话线程。");
      addLive(`复盘已刷新：${state.asOf}。`);
    } catch (error) {
      if (error.name === "AbortError") return;
      const stale = !!state.day;
      showPageError(`${error.message}${stale ? " 当前保留上次成功读取的数据。" : ""}`, stale ? "刷新失败，数据已过期" : "复盘快照加载失败");
      if (!stale) show("review-content", false);
    } finally {
      state.refreshing = false;
      setRefreshBusy(false);
    }
  }

  function formatEvidenceKind(kind) {
    return REVIEW_LABELS[kind] || textValue(kind, "证据");
  }

  function evidenceLabel(id) {
    if (!id) return "[—]";
    return id.startsWith("[") ? id : `[${id}]`;
  }

  function appendDefinitionGrid(container, entries) {
    const dl = el("dl", "review-kv-grid");
    for (const [term, value] of entries) {
      const row = el("div", "review-kv");
      row.append(el("dt", "", term), el("dd", "", value));
      dl.append(row);
    }
    container.append(dl);
  }

  function detailSection(title, caption) {
    const section = el("section", "review-detail-section");
    section.append(el("h4", "", title));
    if (caption) section.append(el("p", "review-section-caption", caption));
    return section;
  }

  function emptyNote(text) {
    return el("p", "review-empty-inline", text);
  }

  function renderPlanSection(plan) {
    const section = detailSection("参考计划", "来自冻结报告；价格是参考值，不代表实际成交。");
    if (!plan) {
      section.append(emptyNote("该报告没有保存参考计划。"));
      return section;
    }
    if (plan.reference_only === true) section.append(el("span", "tag tag-neutral", "仅参考 · 未成交"));
    appendDefinitionGrid(section, [
      ["参考买入价", price(plan.reference_entry_price)],
      ["参考股数", count(plan.reference_shares)],
      ["参考买入成本", finite(plan.reference_buy_cost) ? `${number(plan.reference_buy_cost, 2)} 元` : "—"],
      ["净止盈 2.5% 价", price(plan.target_net_2_5_price)],
      ["净止盈 3% 价", price(plan.target_net_3_price)],
      ["止盈价", price(plan.take_profit_price)],
      ["止损价", price(plan.stop_price)],
      ["亏损上限", pct(plan.net_loss_limit_pct, false)],
      ["模型止损净幅", pct(plan.modeled_stop_net_pct, false)],
      ["有效止损目标", pct(plan.effective_stop_goal_net_pct, false)],
    ]);
    return section;
  }

  function renderSignalSection(bar) {
    const section = detailSection("冻结信号", "信号日线来自报告输入，不随之后的行情修订。");
    if (!bar) {
      section.append(emptyNote("缺少冻结信号日线。"));
      return section;
    }
    appendDefinitionGrid(section, [
      ["日期", textValue(bar.date)],
      ["开盘", price(bar.open)],
      ["最高", price(bar.high)],
      ["最低", price(bar.low)],
      ["收盘", price(bar.close)],
      ["成交量（原始）", volume(bar.volume_raw)],
    ]);
    return section;
  }

  function checkpointMap(event) {
    const source = list(event.outcome?.checkpoints).length ? event.outcome.checkpoints : event.checkpoints;
    const map = new Map();
    for (const checkpoint of list(source)) {
      if (isNumber(checkpoint.signal_day)) map.set(checkpoint.signal_day, checkpoint);
    }
    return map;
  }

  function renderCheckpoints(event) {
    const section = detailSection("T+1／T+3／T+5", "检查点是信号日之后的收盘净收益；未成熟路径可能缺失。");
    const checkpoints = checkpointMap(event);
    const grid = el("div", "review-checkpoint-grid");
    for (const dayNumber of [1, 3, 5]) {
      const checkpoint = checkpoints.get(dayNumber);
      const card = el("div", "review-checkpoint");
      card.append(
        el("span", "", `T+${dayNumber}`),
        el("strong", `review-net ${numberClass(checkpoint?.close_net_pct)}`, pct(checkpoint?.close_net_pct)),
        el("small", "", checkpoint?.date ? textValue(checkpoint.date) : "未生成"),
      );
      grid.append(card);
    }
    section.append(grid);
    return section;
  }

  function renderOutcomeSection(event) {
    const section = detailSection("模拟交易结果", "使用不复权价格模拟，结果为研究记录，不代表实盘。");
    const outcome = event.outcome;
    if (!outcome) {
      appendDefinitionGrid(section, [
        ["状态", label(event.state)],
        ["净收益", pct(event.net_pct)],
        ["MFE", pct(event.max_favorable_net_pct)],
        ["MAE", pct(event.max_adverse_net_pct)],
        ["退出原因", label(event.exit_reason)],
        ["顺序不明", event.ambiguous === true ? "是" : "否"],
      ]);
      return section;
    }
    appendDefinitionGrid(section, [
      ["状态", textValue(outcome.status)],
      ["买入日", textValue(outcome.entry_date)],
      ["退出日", textValue(outcome.exit_date)],
      ["退出价", price(outcome.exit_price)],
      ["净收益", pct(outcome.net_pct)],
      ["持有天数", count(outcome.holding_days)],
      ["MFE", pct(outcome.max_favorable_net_pct)],
      ["MAE", pct(outcome.max_adverse_net_pct)],
      ["顺序不明", outcome.ambiguous === true ? "是" : "否"],
      ["退出原因", label(event.exit_reason)],
    ]);
    return section;
  }

  function zoneText(zone) {
    if (!zone) return "—";
    return `${price(zone.lower)} – ${price(zone.upper)}（中值 ${price(zone.mid)}）`;
  }

  function renderStructureSection(guard) {
    const section = detailSection("结构门控", "日线决定方向，分钟周期只提供否决证据；未知不等同通过。");
    if (!guard) {
      section.append(emptyNote("该事件没有结构门控证据。"));
      return section;
    }
    appendDefinitionGrid(section, [
      ["门控状态", label(guard.guard_status)],
      ["运行模式", label(guard.mode)],
      ["数据状态", label(guard.data_status)],
      ["信号状态", label(guard.signal_status)],
      ["原因", textValue(guard.reason)],
      ["每日区间", zoneText(guard.daily_zone)],
    ]);
    const levels = list(guard.levels);
    if (!levels.length) {
      section.append(emptyNote("没有可展示的周期结构级别。"));
      return section;
    }
    const levelList = el("div", "review-level-list");
    for (const level of levels) {
      const item = el("div", "review-level");
      const head = el("div", "review-level-head");
      head.append(el("span", "", `${textValue(level.period, "周期")} · ${label(level.status)}`), el("span", "", `${count(level.bars)} 根 / ${count(level.finished_strokes)} 笔`));
      item.append(head);
      const details = [];
      if (level.reason) details.push(level.reason);
      if (level.latest_zone) details.push(`区间 ${zoneText(level.latest_zone)}`);
      if (level.last_fractal) details.push(`最后分型 ${level.last_fractal}`);
      if (level.last_finished_stroke) details.push(`最后完成笔 ${level.last_finished_stroke}`);
      item.append(el("p", "", details.length ? details.join(" · ") : "无额外说明。"));
      levelList.append(item);
    }
    section.append(levelList);
    return section;
  }

  function listBlock(container, title, values, risk = false, empty = "—") {
    container.append(el("h4", "", title));
    const items = dedupe(list(values));
    if (!items.length) {
      container.append(emptyNote(empty));
      return;
    }
    const ul = el("ul", `review-bullet-list${risk ? " is-risk" : ""}`);
    for (const item of items) ul.append(el("li", "", label(item)));
    container.append(ul);
  }

  function renderEvidence(event) {
    const section = detailSection("证据列表", "每条证据都有稳定 ID；回答里的 [E#] 可以回到这里核对。");
    const evidence = list(event.evidence);
    if (!evidence.length) {
      section.append(emptyNote("该事件没有保存证据列表。"));
      return section;
    }
    const wrap = el("div", "review-evidence-list");
    for (const item of evidence) {
      const id = textValue(item.evidence_id, "E?");
      const details = el("details", "review-evidence-item");
      details.id = `review-evidence-${id}`;
      const summary = el("summary");
      const meta = [formatEvidenceKind(item.kind), item.source, item.as_of].filter(Boolean).join(" · ");
      summary.append(el("strong", "", evidenceLabel(id)), el("span", "", textValue(meta, "本地证据")));
      const value = el("pre", "review-evidence-value", jsonText(item.value));
      value.tabIndex = 0;
      details.append(summary, value);
      wrap.append(details);
    }
    section.append(wrap);
    return section;
  }

  function renderEventDetail(event) {
    state.currentEvent = event;
    setText("review-detail-description", `报告修订 ${shortRevision(event.report_revision)}${event.report_generated_at ? ` · 生成于 ${formatTime(event.report_generated_at)}` : ""}`);
    const detailState = byId("review-detail-state");
    detailState.replaceChildren(stateTag(event.state));
    detailState.hidden = false;

    const content = el("div", "review-detail-content");
    const hero = el("div", "review-detail-hero");
    const titleRow = el("div", "review-detail-title-row");
    const titleBlock = el("div");
    titleBlock.append(el("h3", "", textValue(event.name, event.code || "未命名股票")), el("span", "review-detail-code", textValue(event.code)));
    titleRow.append(titleBlock, stateTag(event.state));
    const tags = el("div", "review-detail-tags");
    tags.append(...recommendationTags(event).childNodes);
    if (event.ambiguous === true) tags.append(el("span", "tag tag-excluded", "顺序不明"));
    const chartLink = el("a", "review-detail-link", "在个股 K 线中查看 →");
    chartLink.href = `/chart?code=${encodeURIComponent(event.code || "")}`;
    hero.append(titleRow, tags, chartLink);

    const summary = el("div", "review-detail-summary");
    const summaryItems = [
      ["排名", isNumber(event.rank) && event.rank > 0 ? number(event.rank) : "—"],
      ["观察排名", event.observation_rank ? number(event.observation_rank) : "—"],
      ["信号日", textValue(event.signal_date)],
      ["模拟买入", textValue(event.entry_date)],
      ["退出日", textValue(event.exit_date)],
      ["净收益", pct(event.net_pct)],
      ["MFE", pct(event.max_favorable_net_pct)],
      ["MAE", pct(event.max_adverse_net_pct)],
      ["退出原因", label(event.exit_reason)],
      ["状态原因", label(event.state_reason)],
    ];
    for (const [term, value] of summaryItems) {
      const cell = el("div", "review-summary-cell");
      cell.append(el("span", "", term), el("strong", "", value));
      summary.append(cell);
    }
    hero.append(summary);
    content.append(hero);

    content.append(renderPlanSection(event.reference_plan));
    content.append(renderSignalSection(event.signal_bar));
    content.append(renderCheckpoints(event));
    content.append(renderOutcomeSection(event));
    content.append(renderStructureSection(event.structure_guard));

    const riskSection = detailSection("风险、原因与匹配规则");
    listBlock(riskSection, "风险", event.risks, true, "没有记录额外风险。");
    listBlock(riskSection, "推荐依据", event.reasons, false, "没有记录推荐依据。");
    listBlock(riskSection, "匹配规则", event.matched_rules, false, "没有记录匹配规则。");
    content.append(riskSection);

    const missingSection = detailSection("缺失证据");
    const missing = dedupe(list(event.missing));
    if (missing.length) {
      const ul = el("ul", "review-missing-list");
      for (const item of missing) ul.append(el("li", "", label(item)));
      missingSection.append(ul);
    } else {
      missingSection.append(emptyNote("未标记缺失证据。"));
    }
    content.append(missingSection);
    content.append(renderEvidence(event));

    const body = byId("review-detail-body");
    body.replaceChildren(content);
    show("review-detail-empty", false);
    show("review-detail-loading", false);
    show("review-detail-error", false);
    show("review-detail-body", true);
  }

  async function selectEvent(code, options = {}) {
    if (!code || !state.day) return;
    const previousCode = state.selectedCode;
    state.selectedCode = code;
    state.currentEvent = null;
    if (previousCode !== code && state.initialized) {
      resetAnalysis(true);
      clearMasterThreads("事件焦点已切换，已开启新的对话线程。");
    }
    markSelectedRow();
    if (options.updateURL !== false) syncURL();
    if (options.focusDetail !== false) {
      const rowButton = [...byId("review-event-body").querySelectorAll("button[data-code]")].find((button) => button.dataset.code === code);
      rowButton?.focus({ preventScroll: true });
    }
    state.eventController?.abort();
    const request = ++state.eventRequest;
    state.eventController = new AbortController();
    show("review-detail-empty", false);
    show("review-detail-body", false);
    show("review-detail-error", false);
    show("review-detail-loading", true);
    const query = new URLSearchParams({ as_of: state.asOf, code });
    if (state.revision) query.set("revision", state.revision);
    try {
      const event = await fetchJSON(`/api/review/event?${query.toString()}`, {
        signal: state.eventController.signal,
        timeout: 16000,
        notFound: "该股票不在当前复盘事件中。",
        serverError: "事件详情暂时不可用，请稍后重试。",
      });
      if (request !== state.eventRequest) return;
      if (!event || event.code !== code) throw new Error("事件代码与请求不匹配，请重新加载。");
      renderEventDetail(event);
      updatePreview();
      addLive(`已载入 ${textValue(event.name, code)} 的复盘详情。`);
    } catch (error) {
      if (request !== state.eventRequest || error.name === "AbortError") return;
      show("review-detail-loading", false);
      show("review-detail-body", false);
      setText("review-detail-error-message", safeMessage(error.message));
      show("review-detail-error", true);
    }
  }

  function citationIds(value) {
    if (typeof value === "string") return [value.replace(/[\[\]]/g, "")].filter((item) => /^E\d+$/.test(item));
    if (!Array.isArray(value)) return [];
    return value.map((item) => {
      if (typeof item === "string") return item.replace(/[\[\]]/g, "");
      if (item && typeof item === "object") return String(item.evidence_id || item.id || item.ref || item.evidence || "").replace(/[\[\]]/g, "");
      return "";
    }).filter((item) => /^E\d+$/.test(item));
  }

  function summarizeItem(item) {
    if (item === null || item === undefined) return "";
    if (typeof item === "string" || typeof item === "number" || typeof item === "boolean") return String(item);
    if (Array.isArray(item)) return item.map(summarizeItem).filter(Boolean).join("；");
    const keys = ["title", "label", "name", "action", "intent", "description", "detail", "summary", "query", "tool", "message"];
    const parts = [];
    for (const key of keys) {
      const value = item[key];
      if (typeof value === "string" && value.trim()) parts.push(value.trim());
    }
    return parts.length ? [...new Set(parts)].join(" · ") : jsonText(item);
  }

  function createChain(testId) {
    const details = el("details", "chain-panel");
    if (testId) details.dataset.testid = testId;
    details.open = true;
    const summary = el("summary");
    const stateLabel = el("span", "chain-state", "准备中");
    summary.append(el("span", "", "执行链路"), stateLabel);
    const listElement = el("ol", "chain-list");
    const stages = [
      ["intent", "问题拆分 / 意图"],
      ["plan", "查询计划"],
      ["evidence", "证据收集"],
      ["prompt", "提示词组装"],
      ["model", "模型生成"],
      ["citation", "引用校验"],
    ];
    const refs = {};
    for (const [key, title] of stages) {
      const item = el("li", "chain-stage");
      item.dataset.stage = key;
      const dot = el("span", "chain-dot");
      dot.setAttribute("aria-hidden", "true");
      const value = el("p", "", "等待…");
      const body = el("div");
      body.append(el("strong", "", title), value);
      item.append(dot, body);
      listElement.append(item);
      refs[key] = { item, value };
    }
    details.append(summary, listElement);
    const controller = {
      root: details,
      start() {
        details.open = true;
        stateLabel.textContent = "进行中";
        for (const ref of Object.values(refs)) {
          ref.item.className = "chain-stage";
          ref.value.textContent = "等待…";
        }
      },
      set(key, text, status = "done") {
        const ref = refs[key];
        if (!ref) return;
        ref.item.className = `chain-stage${status === "active" ? " is-active" : status === "error" ? " is-error" : status === "waiting" ? "" : " is-done"}`;
        ref.value.textContent = textValue(text, "已完成");
      },
      summary(text) {
        stateLabel.textContent = textValue(text, "进行中");
      },
      finishDone(text) {
        for (const ref of Object.values(refs)) {
          if (!ref.item.classList.contains("is-error")) ref.item.classList.add("is-done");
        }
        stateLabel.textContent = textValue(text, "已完成");
        details.open = false;
      },
      fail(text) {
        for (const ref of Object.values(refs)) {
          if (ref.item.classList.contains("is-active") || !ref.item.classList.contains("is-done")) ref.item.classList.add("is-error");
        }
        stateLabel.textContent = textValue(text, "执行中断");
        details.open = true;
      },
      cancel(text) {
        stateLabel.textContent = textValue(text, "已取消");
        details.open = true;
      },
    };
    return controller;
  }

  function setViewStatus(view, text, kind = "") {
    if (!view.statusElement || view.silenced) return;
    view.statusElement.textContent = text;
    view.statusElement.className = view.statusClass;
    if (kind) view.statusElement.classList.add(`is-${kind}`);
    view.statusElement.hidden = false;
  }

  function renderCitationText(view) {
    const answer = view.answer;
    answer.replaceChildren();
    if (!view.text) {
      answer.append(el("span", "stream-placeholder", view.done || view.failed || view.cancelled ? "没有可展示的回答内容。" : "正在整理本地证据…"));
      view.seenReferences = 0;
      updateCitationStatus(view);
      return;
    }
    const pattern = /\[(E\d+)\]/g;
    let cursor = 0;
    let match;
    let seen = 0;
    while ((match = pattern.exec(view.text)) !== null) {
      if (match.index > cursor) answer.append(document.createTextNode(view.text.slice(cursor, match.index)));
      const id = match[1];
      seen += 1;
      if (view.invalid.has(id)) {
        answer.append(el("span", "citation-invalid", `[${id}]`));
      } else if (view.valid.has(id) || view.evidence.has(id) || view.initialEvidence.has(id)) {
        const link = el("a", "citation-link", `[${id}]`);
        link.href = `#review-evidence-${id}`;
        link.dataset.evidenceId = id;
        link.setAttribute("aria-label", `查看本地证据 ${id}`);
        answer.append(link);
      } else {
        answer.append(el("span", "citation-pending", `[${id}]`));
      }
      cursor = pattern.lastIndex;
    }
    if (cursor < view.text.length) answer.append(document.createTextNode(view.text.slice(cursor)));
    view.seenReferences = seen;
    updateCitationStatus(view);
  }

  function updateCitationStatus(view) {
    if (!view.citation) return;
    if (!view.done && !view.failed && !view.cancelled) {
      view.citation.textContent = "引用校验中…";
      return;
    }
    view.citation.replaceChildren();
    if (view.failed) {
      view.citation.append(el("span", "citation-empty-label", "生成中断，未完成引用校验。"));
      return;
    }
    if (view.cancelled && !view.done) {
      view.citation.append(el("span", "citation-empty-label", "生成已取消，未完成引用校验。"));
      return;
    }
    if (view.zeroReference || view.seenReferences === 0) {
      view.citation.append(el("span", "citation-empty-label", "未引用本地证据"));
      return;
    }
    const referenced = new Set([...view.text.matchAll(/\[(E\d+)\]/g)].map((match) => match[1]));
    const validReferenced = [...referenced].filter((id) => !view.invalid.has(id) && (view.valid.has(id) || view.evidence.has(id) || view.initialEvidence.has(id))).length;
    const validCount = Math.max(validReferenced, finite(view.reportedValidCount) ? view.reportedValidCount : 0);
    const invalidCount = Math.max(view.invalid.size, finite(view.reportedInvalidCount) ? view.reportedInvalidCount : 0);
    if (invalidCount) {
      view.citation.append(document.createTextNode(`引用校验：${number(validCount)} 条有效，${number(invalidCount)} 条无效。`));
    } else {
      view.citation.append(document.createTextNode(`引用校验：${number(validCount)} 条有效本地证据。`));
    }
  }

  function createStreamView(options) {
    const chain = createChain(options.chainTestId || "");
    options.chainSlot.replaceChildren(chain.root);
    const view = {
      answer: options.answer,
      chain,
      citation: options.citation || null,
      statusElement: options.status || null,
      statusClass: options.statusClass || "master-stream-status",
      text: "",
      valid: new Set(),
      invalid: new Set(),
      evidence: new Set(options.initialEvidence || []),
      initialEvidence: new Set(options.initialEvidence || []),
      zeroReference: false,
      seenReferences: 0,
      done: false,
      failed: false,
      cancelled: false,
      hadEvent: false,
      silenced: false,
      reportedValidCount: null,
      reportedInvalidCount: null,
    };
    view.render = () => renderCitationText(view);
    view.start = () => {
      chain.start();
      view.text = "";
      view.render();
      if (options.citation) options.citation.textContent = "引用校验中…";
      setViewStatus(view, options.startText || "正在生成…", "streaming");
    };
    view.setDone = (event) => {
      view.done = true;
      const partial = event?.partial === true || list(event?.failed_intents).length > 0;
      const failedIntents = list(event?.failed_intents).map(summarizeItem).filter(Boolean);
      chain.finishDone(partial ? "部分完成" : "已完成");
      view.render();
      const suffix = failedIntents.length ? `；未完成意图：${failedIntents.join("、")}` : "";
      setViewStatus(view, partial ? `回答完成，但部分意图未完成${suffix}` : "回答完成", partial ? "warning" : "complete");
    };
    view.fail = (message) => {
      view.failed = true;
      chain.fail("执行中断");
      view.render();
      setViewStatus(view, safeMessage(message), "error");
    };
    view.cancel = () => {
      view.cancelled = true;
      chain.cancel("已取消");
      view.render();
      setViewStatus(view, "已取消生成；保留当前未完成回答。", "cancelled");
    };
    return view;
  }

  function applyStreamEvent(view, event) {
    if (!event || typeof event !== "object") return;
    view.hadEvent = true;
    const type = String(event.type || "").toLowerCase();
    if (type === "intent") {
      const intents = list(event.intents);
      const summary = event.summary ? summarizeItem(event.summary) : (intents.length ? intents.map(summarizeItem).filter(Boolean).join("；") : "已识别问题意图");
      view.chain.set("intent", summary, "done");
      return;
    }
    if (type === "plan") {
      const steps = list(event.steps);
      view.chain.set("plan", event.summary ? summarizeItem(event.summary) : (steps.length ? steps.map(summarizeItem).filter(Boolean).join("；") : "查询计划已确定"), "done");
      return;
    }
    if (type === "evidence") {
      for (const id of citationIds(event.evidence_ids || event.ids || event.evidence)) view.evidence.add(id);
      const evidenceCount = finite(event.count) ? event.count : view.evidence.size;
      const summary = event.summary ? summarizeItem(event.summary) : `已收集 ${count(evidenceCount)} 条本地证据`;
      view.chain.set("evidence", summary, "done");
      view.render();
      return;
    }
    if (type === "prompt") {
      const pieces = [];
      if (event.summary) pieces.push(summarizeItem(event.summary));
      if (finite(event.evidence_count)) pieces.push(`证据 ${count(event.evidence_count)} 条`);
      pieces.push(event.trimmed === true ? "已裁剪证据以适配上下文" : "证据未裁剪");
      view.chain.set("prompt", pieces.join("；"), "done");
      return;
    }
    if (type === "delta") {
      const delta = typeof event.text === "string" ? event.text : typeof event.delta === "string" ? event.delta : "";
      view.text += delta;
      view.render();
      view.chain.set("model", `已生成 ${number(view.text.length)} 字`, "active");
      return;
    }
    if (type === "citation") {
      for (const id of citationIds(event.valid)) view.valid.add(id);
      for (const id of citationIds(event.invalid)) view.invalid.add(id);
      if (finite(event.valid)) view.reportedValidCount = event.valid;
      if (finite(event.invalid)) view.reportedInvalidCount = event.invalid;
      if (event.zero_reference === true || event.zero_reference === 1) view.zeroReference = true;
      const parts = [];
      if (view.valid.size) parts.push(`有效引用 ${number(view.valid.size)} 条`);
      if (view.invalid.size) parts.push(`无效引用 ${number(view.invalid.size)} 条`);
      if (view.zeroReference) parts.push("未引用本地证据");
      view.chain.set("citation", parts.length ? parts.join("；") : "引用校验完成", "done");
      view.render();
      return;
    }
    if (type === "done") {
      view.setDone(event);
      return;
    }
    if (type === "error") {
      const message = event.message || event.error?.message || event.code || event.error?.code || "AI 分析失败";
      view.fail(message);
    }
  }

  async function consumeNDJSON(body, onEvent) {
    if (!body) throw new Error("浏览器未收到可读取的流式响应。");
    const reader = body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    const handleLine = (line) => {
      const value = line.replace(/\r$/, "").trim();
      if (!value) return;
      if (!value.startsWith("{")) return;
      let event;
      try { event = JSON.parse(value); } catch (_) { throw new Error("流式数据格式异常，请重试。"); }
      onEvent(event);
    };
    try {
      while (true) {
        const { value, done } = await reader.read();
        buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
        let newline = buffer.indexOf("\n");
        while (newline >= 0) {
          handleLine(buffer.slice(0, newline));
          buffer = buffer.slice(newline + 1);
          newline = buffer.indexOf("\n");
        }
        if (done) break;
      }
      if (buffer.trim()) handleLine(buffer);
    } finally {
      reader.releaseLock();
    }
  }

  async function readStreamError(response) {
    let raw = "";
    try { raw = await response.text(); } catch (_) { raw = ""; }
    let payload = null;
    if (raw) {
      try { payload = JSON.parse(raw); } catch (_) { payload = null; }
    }
    let message = payload?.error || payload?.message || raw;
    if (response.status === 401 || response.status === 403) message = "AI 服务未获授权，请检查部署配置。";
    else if (response.status === 404) message = "AI 接口暂不可用，请确认后端已启用。";
    else if (response.status === 429) message = "AI 请求过于频繁，请稍后重试。";
    else if (response.status >= 500) message = "AI 服务暂时不可用，请稍后重试。";
    return safeMessage(message);
  }

  async function runStream(url, payload, view, controller, action) {
    view.start();
    try {
      const response = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json", Accept: "application/x-ndjson", "X-Stock-Watcher-Action": action },
        body: JSON.stringify(payload),
        signal: controller.signal,
        credentials: "same-origin",
        cache: "no-store",
      });
      if (!response.ok) throw new Error(await readStreamError(response));
      await consumeNDJSON(response.body, (event) => applyStreamEvent(view, event));
      if (!view.done && !view.failed && !view.cancelled) {
        if (view.hadEvent || view.text) view.setDone({ partial: false });
        else view.fail("AI 响应流没有返回内容。");
      }
    } catch (error) {
      if (controller.signal.aborted) view.cancel();
      else view.fail(error.message || "AI 请求失败，请稍后重试。");
    }
  }

  function currentRole() {
    return state.master.roles.find((role) => role.id === state.master.roleId) || null;
  }

  function currentThread() {
    if (!state.master.roleId) return [];
    if (!state.master.threads[state.master.roleId]) state.master.threads[state.master.roleId] = [];
    return state.master.threads[state.master.roleId];
  }

  function renderMasterEmpty(text) {
    const container = byId("master-answer");
    const empty = el("div", "master-empty");
    empty.id = "master-empty";
    empty.append(el("strong", "", "从当前复盘开始提问"), el("p", "", text || "可以追问为什么推荐、为什么未入场，或某个风险证据是否成立。"));
    container.replaceChildren(empty);
  }

  function clearMasterThreads(reason) {
    state.master.run += 1;
    if (state.master.stream) {
      state.master.stream.view.silenced = true;
      state.master.stream.controller.abort();
      state.master.stream = null;
    }
    state.master.threads = {};
    byId("master-input").value = "";
    renderMasterEmpty(reason || "切换角色或日期会开启新的对话线程。");
    const status = byId("master-stream-status");
    status.textContent = "";
    status.hidden = true;
    updatePreview();
    updateComposerState();
  }

  function appendMasterMessage(role, content, roleName) {
    const empty = byId("master-empty");
    if (empty) empty.remove();
    const message = el("article", `master-message ${role}`);
    if (role === "assistant") {
      message.append(el("span", "master-message-role", `大师${roleName ? ` · ${roleName}` : ""}`));
      const answer = el("div", "master-answer-text");
      const chainSlot = el("div");
      const citation = el("p", "citation-status");
      citation.setAttribute("role", "status");
      message.append(answer, chainSlot, citation);
      byId("master-answer").append(message);
      return { message, answer, chainSlot, citation };
    }
    message.textContent = content;
    byId("master-answer").append(message);
    return { message };
  }

  function clearPreviousChainTestId() {
    const previous = byId("master-answer").querySelector('[data-testid="master-chain"]');
    if (previous) previous.removeAttribute("data-testid");
  }

  function buildMasterPayload(includeDraft = false) {
    if (!state.asOf) return null;
    const role = currentRole();
    const turns = currentThread()
      .filter((message) => typeof message.content === "string" && message.content.trim())
      .map((message) => ({ role: message.role, content: message.content }));
    let question = "";
    let history = turns;
    if (includeDraft) {
      const draft = byId("master-input").value.trim();
      if (draft) {
        question = draft;
        history = turns;
      }
    }
    if (!question) {
      let lastUser = -1;
      for (let index = turns.length - 1; index >= 0; index -= 1) {
        if (turns[index].role === "user") {
          lastUser = index;
          break;
        }
      }
      if (lastUser >= 0) {
        question = turns[lastUser].content;
        history = turns.slice(0, lastUser);
      }
    }
    const payload = {
      mode: "chat",
      role: role?.id || "",
      question,
      as_of: state.asOf,
      revision: state.revision || state.day?.revision || "",
      code: state.selectedCode || "",
    };
    if (history.length) payload.history = history.slice(-12);
    return payload;
  }

  function updatePreview() {
    const preview = byId("llm-payload-preview");
    const payload = buildMasterPayload(true);
    preview.textContent = payload ? jsonText(payload) : "尚未选择复盘日期；选择后这里会显示实际发送的数据。";
  }

  async function sendMaster() {
    const input = byId("master-input");
    const question = input.value.trim();
    const role = currentRole();
    const configReady = aiReady();
    if (!question || !role || !state.day || !configReady || state.master.stream) return;
    const thread = currentThread();
    thread.push({ role: "user", content: question });
    appendMasterMessage("user", question);
    input.value = "";
    const payload = buildMasterPayload(false);
    const assistantRecord = { role: "assistant", content: "" };
    thread.push(assistantRecord);
    const nodes = appendMasterMessage("assistant", "", role.name);
    clearPreviousChainTestId();
    const controller = new AbortController();
    const view = createStreamView({
      answer: nodes.answer,
      chainSlot: nodes.chainSlot,
      chainTestId: "master-chain",
      citation: nodes.citation,
      status: byId("master-stream-status"),
      statusClass: "master-stream-status",
      startText: "正在接收大师回答…",
      initialEvidence: list(state.currentEvent?.evidence).map((item) => item.evidence_id),
    });
    const run = ++state.master.run;
    state.master.stream = { controller, view, assistantRecord };
    updatePreview();
    updateComposerState();
    await runStream("/api/master/chat", payload, view, controller, "master-chat");
    assistantRecord.content = view.text;
    if (state.master.stream?.controller === controller) state.master.stream = null;
    if (run === state.master.run) {
      updatePreview();
      updateComposerState();
      addLive(view.failed ? "大师回答失败。" : view.cancelled ? "大师回答已取消。" : "大师回答完成。");
    }
  }

  function cancelMaster() {
    state.master.stream?.controller.abort();
  }

  function updateComposerState() {
    const input = byId("master-input");
    const send = byId("master-send");
    const cancel = byId("master-cancel");
    const role = currentRole();
    const configReady = aiReady();
    const streaming = !!state.master.stream;
    const unavailable = !state.day || !role || !configReady || streaming;
    input.disabled = unavailable;
    send.disabled = unavailable || !input.value.trim();
    cancel.disabled = !streaming;
    send.textContent = streaming ? "生成中" : "发送";
    input.placeholder = !state.day
      ? "先选择复盘日期"
      : !configReady
        ? (state.llm.config?.enabled === false ? "AI 功能已由部署配置关闭" : "请先完成 AI 设置")
        : !role
          ? "等待角色加载"
          : "例如：这只股票为什么没有进入正式推荐？";
    const analysisButton = byId("review-analysis-button");
    const analysisStreaming = !!state.analysis.stream;
    analysisButton.disabled = !state.day || !configReady || analysisStreaming;
    analysisButton.textContent = analysisStreaming ? "正在分析" : "AI 复盘归因";
    const analysisCancel = byId("review-analysis-cancel");
    analysisCancel.hidden = !analysisStreaming;
    analysisCancel.disabled = !analysisStreaming;
  }

  function fieldSource(config, key) {
    const field = config?.fields?.[key];
    if (typeof field === "string") return field;
    if (field && typeof field === "object") return String(field.source || field.origin || field.mode || "");
    return config?.[key] ? "server" : "default";
  }

  function sourceText(source) {
    if (source === "env") return "由部署配置锁定";
    if (source === "file") return "来源：私有配置文件";
    if (source === "default") return "来源：默认值";
    if (source === "server") return "来源：服务端配置";
    return source ? `来源：${source}` : "";
  }

  function applyFieldState(id, sourceId, locked, value, placeholder) {
    const input = byId(id);
    const source = byId(sourceId);
    input.disabled = locked;
    if (value !== undefined && value !== null) input.value = String(value);
    if (placeholder !== undefined) input.placeholder = placeholder;
    source.textContent = locked ? "由部署配置锁定" : sourceText(fieldSource(state.llm.config || {}, id.replace("llm-", "").replaceAll("-", "_")));
    input.title = locked ? "由部署配置锁定" : "";
  }

  function fieldLocked(config, key) {
    if (config?.write_enabled === false) return true;
    if (config?.locked && config.locked[key] === true) return true;
    return fieldSource(config, key) === "env";
  }

  function renderLLMConfig(config) {
    const writeEnabled = config?.write_enabled !== false;
    const baseLocked = fieldLocked(config, "base_url");
    const keyLocked = fieldLocked(config, "api_key");
    const modelLocked = fieldLocked(config, "model");
    const timeoutLocked = fieldLocked(config, "timeout");
    applyFieldState("llm-base-url", "llm-base-url-source", baseLocked, config?.base_url || "", "https://api.example.com/v1");
    applyFieldState("llm-model", "llm-model-source", modelLocked, config?.model || "", "模型名称");
    applyFieldState("llm-timeout", "llm-timeout-source", timeoutLocked, config?.timeout || "60s", "60s");
    const apiKey = byId("llm-api-key");
    apiKey.value = "";
    apiKey.disabled = keyLocked;
    byId("llm-api-key-source").textContent = keyLocked ? "由部署配置锁定" : config?.api_key_present ? `已配置${config.api_key_suffix ? ` · 尾号 ${config.api_key_suffix}` : ""}` : "尚未配置";
    apiKey.placeholder = config?.api_key_present ? `已配置${config.api_key_suffix ? `，尾号 ${config.api_key_suffix}` : ""}；留空则不修改` : "输入 API Key";
    apiKey.title = keyLocked ? "由部署配置锁定" : "";
    byId("llm-save").disabled = !writeEnabled || state.llm.loading;
    const writeNote = byId("llm-write-note");
    if (!writeEnabled) {
      writeNote.textContent = `当前部署的写入模式为 ${textValue(config?.write_mode, "off")}，网页不能修改 AI 配置。`;
      writeNote.hidden = false;
    } else {
      writeNote.hidden = true;
    }
  }

  function setConfigStatus(message, kind = "info") {
    const status = byId("llm-config-status");
    status.textContent = message;
    status.className = `llm-config-status${kind === "warning" ? " is-warning" : kind === "error" ? " is-error" : ""}`;
  }

  function updateAIReadiness() {
    const config = state.llm.config;
    const configured = aiReady();
    const masterStatus = byId("master-ai-status");
    masterStatus.className = "notice review-ai-status";
    if (state.llm.mode === "loading") {
      masterStatus.textContent = "正在读取 AI 配置…";
    } else if (configured) {
      const parts = ["AI 已配置"];
      if (config.model) parts.push(`模型 ${config.model}`);
      if (config.api_key_present) parts.push(config.api_key_suffix ? `密钥尾号 ${config.api_key_suffix}` : "密钥已配置");
      masterStatus.textContent = parts.join(" · ");
    } else if (config?.enabled === false) {
      masterStatus.textContent = "AI 功能已由部署配置关闭；配置仍可查看，但暂时不能生成回答。";
      masterStatus.classList.add("is-warning");
    } else if (state.llm.mode === "unconfigured") {
      masterStatus.textContent = "AI 尚未配置。打开 AI 设置并保存 Base URL、模型和 API Key 后再提问。";
      masterStatus.classList.add("is-warning");
    } else {
      masterStatus.textContent = state.llm.error || "AI 设置接口暂不可用，暂时不能生成回答。";
      masterStatus.classList.add("is-error");
    }

    const notice = byId("review-analysis-notice");
    const settingsButton = byId("review-analysis-settings");
    const paragraph = notice.querySelector("p");
    notice.classList.toggle("is-unconfigured", !configured);
    settingsButton.hidden = configured;
    if (configured) {
      const scope = state.selectedCode ? `所选事件 ${state.selectedCode}` : state.asOf ? `报告 ${state.asOf}` : "当前复盘";
      paragraph.textContent = `将分析${scope}。只有点击按钮才会发送请求；默认不自动生成。`;
    } else if (config?.enabled === false) {
      paragraph.textContent = "AI 功能已由部署配置关闭；复盘数据本身仍可正常查看。";
    } else if (state.llm.mode === "unconfigured") {
      paragraph.textContent = "AI 尚未配置，请先打开 AI 设置。复盘数据本身仍可正常查看。";
    } else if (state.llm.mode === "unavailable") {
      paragraph.textContent = state.llm.error || "AI 设置接口暂不可用；复盘数据本身仍可正常查看。";
    } else {
      paragraph.textContent = "正在读取 AI 配置；读取完成前不会自动生成。";
    }
    updateComposerState();
  }

  function aiReady() {
    const config = state.llm.config;
    return state.llm.mode === "configured" && config?.configured === true && config?.enabled !== false;
  }

  async function loadLLMConfig(options = {}) {
    if (state.llm.loading) return;
    state.llm.loading = true;
    if (!options.quiet) {
      state.llm.mode = "loading";
      setConfigStatus("正在读取 AI 配置…");
      show("llm-config-error", false);
      updateAIReadiness();
    }
    try {
      const config = await fetchJSON("/api/llm/config", { timeout: 10000, notFound: "AI 设置接口暂不可用。", serverError: "AI 配置读取失败，请稍后重试。" });
      if (!config || typeof config !== "object") throw new Error("AI 配置格式不匹配。");
      state.llm.config = config;
      state.llm.error = "";
      state.llm.mode = config.configured === true ? "configured" : "unconfigured";
      renderLLMConfig(config);
      if (aiReady()) {
        setConfigStatus(`已配置${config.model ? ` · ${config.model}` : ""}${config.api_key_suffix ? ` · 密钥尾号 ${config.api_key_suffix}` : ""}`);
      } else if (config.enabled === false) {
        setConfigStatus("AI 功能已由部署配置关闭；配置仍可保存，但网页不会生成回答。", "warning");
      } else {
        setConfigStatus("尚未配置。填写 Base URL、模型和 API Key 后保存。", "warning");
      }
      show("llm-config-error", false);
    } catch (error) {
      state.llm.config = null;
      state.llm.mode = "unavailable";
      state.llm.error = safeMessage(error.message);
      setConfigStatus(state.llm.error, "error");
      const errorBox = byId("llm-config-error");
      errorBox.textContent = state.llm.error;
      show("llm-config-error", true);
    } finally {
      state.llm.loading = false;
      const save = byId("llm-save");
      save.disabled = !state.llm.config || state.llm.config.write_enabled === false;
      updateAIReadiness();
    }
  }

  function openSettings(trigger) {
    state.llm.settingsTrigger = trigger || document.activeElement;
    const dialog = byId("llm-settings-dialog");
    byId("llm-test-status").hidden = true;
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) dialog.showModal();
    } else {
      dialog.setAttribute("open", "");
    }
    loadLLMConfig();
  }

  function closeSettings() {
    const dialog = byId("llm-settings-dialog");
    if (typeof dialog.close === "function" && dialog.open) dialog.close();
    else dialog.removeAttribute("open");
    state.llm.settingsTrigger?.focus?.();
  }

  async function saveLLMConfig() {
    const config = state.llm.config || {};
    const body = {};
    const candidates = [
      ["base_url", "llm-base-url"],
      ["model", "llm-model"],
      ["timeout", "llm-timeout"],
    ];
    for (const [key, id] of candidates) {
      const input = byId(id);
      if (input.disabled) continue;
      const value = input.value.trim();
      if (value !== String(config[key] || "").trim()) body[key] = value;
    }
    const apiKey = byId("llm-api-key");
    if (!apiKey.disabled && apiKey.value.trim()) body.api_key = apiKey.value.trim();
    const status = byId("llm-test-status");
    status.className = "llm-test-status";
    if (!Object.keys(body).length) {
      status.textContent = "没有需要保存的修改。";
      status.hidden = false;
      return;
    }
    const save = byId("llm-save");
    save.disabled = true;
    status.textContent = "正在保存…";
    status.hidden = false;
    try {
      const updated = await fetchJSON("/api/llm/config", {
        method: "PUT",
        body,
        headers: { "X-Stock-Watcher-Action": "llm-config" },
        timeout: 12000,
        notFound: "AI 设置接口暂不可用。",
        serverError: "AI 设置保存失败，请稍后重试。",
      });
      if (updated && typeof updated === "object") {
        state.llm.config = updated;
        state.llm.mode = updated.configured === true ? "configured" : "unconfigured";
        renderLLMConfig(updated);
      }
      apiKey.value = "";
      await loadLLMConfig({ quiet: true });
      status.className = "llm-test-status is-success";
      status.textContent = "设置已保存，状态已刷新。";
      addLive("AI 设置已保存。");
    } catch (error) {
      status.className = "llm-test-status is-error";
      status.textContent = safeMessage(error.message);
    } finally {
      save.disabled = !state.llm.config || state.llm.config.write_enabled === false;
    }
  }

  async function testLLMConfig() {
    const status = byId("llm-test-status");
    const button = byId("llm-test");
    status.className = "llm-test-status";
    status.textContent = "正在测试连接…";
    status.hidden = false;
    button.disabled = true;
    try {
      const result = await fetchJSON("/api/llm/config/test", {
        method: "POST",
        body: {},
        headers: { "X-Stock-Watcher-Action": "llm-test" },
        timeout: 45000,
        notFound: "AI 测试接口暂不可用。",
        serverError: "AI 连接测试失败，请检查部署与上游服务。",
      });
      if (result?.ok !== true) throw new Error(result?.error || "连接测试未通过。");
      status.className = "llm-test-status is-success";
      status.textContent = "连接成功。";
      addLive("AI 连接测试成功。");
    } catch (error) {
      status.className = "llm-test-status is-error";
      status.textContent = safeMessage(error.message);
    } finally {
      button.disabled = false;
    }
  }

  function renderRoleOptions() {
    const select = byId("master-role-select");
    select.replaceChildren();
    if (!state.master.roles.length) {
      const option = el("option", "", state.master.rolesError ? "角色暂不可用" : "暂无角色");
      option.value = "";
      select.append(option);
      select.disabled = true;
      setText("master-role-description", state.master.rolesError || "没有可用的 AI 角色。");
      updateComposerState();
      return;
    }
    for (const role of state.master.roles) {
      const option = el("option", "", role.name || role.id);
      option.value = role.id;
      option.title = role.description || role.name || role.id;
      select.append(option);
    }
    if (!state.master.roles.some((role) => role.id === state.master.roleId)) {
      const preferred = state.master.roles.find((role) => role.id === state.master.defaultRole);
      state.master.roleId = preferred ? preferred.id : state.master.roles[0].id;
    }
    select.value = state.master.roleId;
    select.disabled = false;
    const role = currentRole();
    const bits = [role?.description || "切换角色会开启新的对话线程。"];
    if (role?.prompt_version) bits.push(`提示词版本 ${role.prompt_version}`);
    setText("master-role-description", bits.join(" · "));
    updateComposerState();
  }

  async function loadMasterRoles() {
    try {
      const payload = await fetchJSON("/api/master/roles", { timeout: 10000, notFound: "大师角色接口暂不可用。", serverError: "大师角色读取失败。" });
      const roles = Array.isArray(payload)
        ? payload
        : list(payload?.roles || payload?.roles?.items || payload?.data?.roles || payload?.data?.items || payload?.data);
      const defaultRole = Array.isArray(payload)
        ? ""
        : String(payload?.default_role?.id || payload?.default_role?.role_id || payload?.default_role || "");
      state.master.roles = roles.map((role, index) => {
        if (typeof role === "string") return { id: role, name: role, description: "", prompt_version: "" };
        return {
          id: String(role?.id || role?.role_id || role?.key || `role-${index + 1}`),
          name: String(role?.name || role?.title || role?.id || `角色 ${index + 1}`),
          description: String(role?.description || role?.summary || ""),
          prompt_version: String(role?.prompt_version || role?.version || ""),
        };
      }).filter((role) => role.id);
      if (!state.master.roles.length) throw new Error("没有可用的 AI 角色。");
      state.master.defaultRole = defaultRole;
      state.master.rolesError = "";
    } catch (error) {
      state.master.roles = [];
      state.master.roleId = "";
      state.master.rolesError = safeMessage(error.message);
    }
    renderRoleOptions();
  }

  function openMasterDrawer() {
    const drawer = byId("master-drawer");
    if (state.master.drawerOpen) return;
    state.master.trigger = document.activeElement;
    state.master.drawerOpen = true;
    byId("master-open").setAttribute("aria-expanded", "true");
    drawer.hidden = false;
    byId("master-scrim").hidden = false;
    document.body.classList.add("master-open");
    document.querySelector(".site-header")?.setAttribute("inert", "");
    byId("main-content").setAttribute("inert", "");
    document.addEventListener("keydown", drawerKeydown, true);
    loadLLMConfig({ quiet: true });
    if (!state.master.roles.length) loadMasterRoles();
    updatePreview();
    requestAnimationFrame(() => {
      const target = byId("master-role-select").disabled ? byId("master-close") : byId("master-role-select");
      target.focus();
    });
  }

  function closeMasterDrawer() {
    if (!state.master.drawerOpen) return;
    state.master.drawerOpen = false;
    document.removeEventListener("keydown", drawerKeydown, true);
    byId("master-open").setAttribute("aria-expanded", "false");
    byId("master-drawer").hidden = true;
    byId("master-scrim").hidden = true;
    document.body.classList.remove("master-open");
    document.querySelector(".site-header")?.removeAttribute("inert");
    byId("main-content").removeAttribute("inert");
    state.master.trigger?.focus?.();
    state.master.trigger = null;
  }

  function drawerFocusable() {
    const drawer = byId("master-drawer");
    return [...drawer.querySelectorAll('a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')].filter(isVisible);
  }

  function drawerKeydown(event) {
    if (!state.master.drawerOpen || byId("llm-settings-dialog").open) return;
    if (event.key === "Escape") {
      event.preventDefault();
      closeMasterDrawer();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = drawerFocusable();
    if (!focusable.length) {
      event.preventDefault();
      byId("master-drawer").focus();
      return;
    }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function analysisPayload() {
    const payload = {
      mode: "review",
      role: currentRole()?.id || "",
      as_of: state.asOf,
      revision: state.revision || state.day?.revision || "",
    };
    if (state.selectedCode) payload.code = state.selectedCode;
    return payload;
  }

  async function startAnalysis() {
    if (!state.day || state.analysis.stream || !aiReady()) return;
    resetAnalysis(false);
    const run = state.analysis.run;
    const answer = byId("review-analysis-answer");
    const chainSlot = byId("review-analysis-chain-slot");
    const citation = byId("review-analysis-citations");
    const status = byId("review-analysis-state");
    const controller = new AbortController();
    const view = createStreamView({
      answer,
      chainSlot,
      chainTestId: "review-analysis-chain",
      citation,
      status,
      statusClass: "review-analysis-state",
      startText: "正在生成 AI 复盘归因…",
      initialEvidence: list(state.currentEvent?.evidence).map((item) => item.evidence_id),
    });
    state.analysis.stream = { controller, view };
    show("review-analysis-output", true);
    status.className = "review-analysis-state";
    status.textContent = "正在生成…";
    status.hidden = false;
    updateComposerState();
    await runStream("/api/review/analysis", analysisPayload(), view, controller, "review-analysis");
    if (state.analysis.stream?.controller === controller) state.analysis.stream = null;
    if (run !== state.analysis.run) return;
    if (view.done) {
      status.className = "review-analysis-state is-complete";
      status.textContent = "AI 复盘归因已完成。引用可以回到事件证据中核对。";
    } else if (view.cancelled) {
      status.className = "review-analysis-state is-cancelled";
      status.textContent = "已取消生成；保留当前未完成回答。";
    } else if (view.failed) {
      status.className = "review-analysis-state is-error";
      status.textContent = view.statusElement?.textContent || "AI 复盘归因失败。";
    }
    updateComposerState();
  }

  function cancelAnalysis() {
    state.analysis.stream?.controller.abort();
  }

  function handleCitationClick(event) {
    const link = event.target.closest("a.citation-link");
    if (!link) return;
    const id = link.dataset.evidenceId;
    const target = byId(`review-evidence-${id}`);
    event.preventDefault();
    if (!target) {
      addLive(`引用 ${evidenceLabel(id)} 来自本次本地证据；当前详情没有展示该条。`);
      return;
    }
    target.open = true;
    target.scrollIntoView({ block: "nearest", behavior: "smooth" });
    target.classList.add("is-cited");
    window.setTimeout(() => target.classList.remove("is-cited"), 1600);
    addLive(`已定位本地证据 ${id}。`);
  }

  function wireEvents() {
    byId("review-refresh").addEventListener("click", () => refreshReview());
    byId("review-retry").addEventListener("click", () => refreshReview());
    byId("review-date-select").addEventListener("change", async (event) => {
      const date = event.target.value;
      if (!DATE_PATTERN.test(date) || date === state.asOf) return;
      state.asOf = date;
      state.revision = "";
      resetContext("复盘日期已切换，已清空事件焦点和对话。");
      try {
        await loadDay(date, "", {});
      } catch (error) {
        if (error.name !== "AbortError") showPageError(error.message);
      }
    });
    byId("review-revision-select").addEventListener("change", async (event) => {
      const revision = event.target.value;
      if (revision && !REVISION_PATTERN.test(revision)) return;
      state.revision = revision;
      const focusCode = state.selectedCode;
      resetAnalysis(true);
      clearMasterThreads("修订版本已切换，已开启新的对话线程。");
      syncURL();
      try {
        await loadDay(state.asOf, revision, { focusCode });
      } catch (error) {
        if (error.name !== "AbortError") showPageError(error.message);
      }
    });
    byId("review-detail-retry").addEventListener("click", () => {
      if (state.selectedCode) selectEvent(state.selectedCode, { updateURL: false });
    });
    byId("master-open").addEventListener("click", openMasterDrawer);
    byId("master-close").addEventListener("click", closeMasterDrawer);
    byId("master-scrim").addEventListener("click", closeMasterDrawer);
    byId("master-send").addEventListener("click", sendMaster);
    byId("master-cancel").addEventListener("click", cancelMaster);
    byId("master-input").addEventListener("input", () => {
      updateComposerState();
      updatePreview();
    });
    byId("master-input").addEventListener("keydown", (event) => {
      if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        sendMaster();
      }
    });
    byId("master-role-select").addEventListener("change", (event) => {
      if (event.target.value === state.master.roleId) return;
      state.master.roleId = event.target.value;
      const role = currentRole();
      const bits = [role?.description || "已开启新的对话线程。"];
      if (role?.prompt_version) bits.push(`提示词版本 ${role.prompt_version}`);
      setText("master-role-description", bits.join(" · "));
      clearMasterThreads("角色已切换，已开启新的对话线程。");
      updatePreview();
      updateComposerState();
    });
    byId("master-payload-details").addEventListener("toggle", () => {
      if (byId("master-payload-details").open) updatePreview();
    });
    byId("llm-settings-open").addEventListener("click", (event) => openSettings(event.currentTarget));
    byId("review-analysis-settings").addEventListener("click", (event) => openSettings(event.currentTarget));
    byId("llm-settings-close").addEventListener("click", closeSettings);
    byId("llm-save").addEventListener("click", saveLLMConfig);
    byId("llm-test").addEventListener("click", testLLMConfig);
    byId("review-analysis-button").addEventListener("click", startAnalysis);
    byId("review-analysis-cancel").addEventListener("click", cancelAnalysis);
    document.addEventListener("click", handleCitationClick);
    window.addEventListener("focus", () => {
      if (!state.initialized || document.visibilityState === "hidden") return;
      if (Date.now() - state.lastRefreshAt < 1500) return;
      refreshReview();
    });
    window.addEventListener("beforeunload", () => {
      state.dayController?.abort();
      state.eventController?.abort();
      state.master.stream?.controller.abort();
      state.analysis.stream?.controller.abort();
    });
  }

  async function initialize() {
    wireEvents();
    resetAnalysis(false);
    renderMasterEmpty();
    const params = new URLSearchParams(location.search);
    state.requestedDate = DATE_PATTERN.test(params.get("as_of") || "") ? params.get("as_of") : "";
    state.requestedCode = CODE_PATTERN.test(params.get("code") || "") ? params.get("code") : "";
    state.requestedRevision = REVISION_PATTERN.test(params.get("revision") || "") ? params.get("revision") : "";
    state.asOf = state.requestedDate;
    state.revision = state.requestedRevision;
    state.master.roleId = "";
    loadMasterRoles();
    loadLLMConfig();
    try {
      const data = await fetchJSON("/api/dashboard", { timeout: 10000, notFound: "看板快照不存在。" });
      state.dashboard = data;
      if (!prepareDashboard(data)) {
        showEmptyReport("还没有保存的报告日期。完成一次同步并生成报告后再来复盘。");
        return;
      }
      show("review-content", true);
      await loadDay(state.asOf, state.revision, { focusCode: state.requestedCode });
      syncURL();
      addLive(`复盘已载入：${state.asOf}。`);
    } catch (error) {
      if (error.name === "AbortError") return;
      showPageError(error.message);
    } finally {
      state.initialized = true;
      state.lastRefreshAt = Date.now();
    }
  }

  initialize();
})();
