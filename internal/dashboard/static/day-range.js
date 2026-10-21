"use strict";

(() => {
  const byId = (id) => document.getElementById(id);
  const show = (id, visible) => { byId(id).hidden = !visible; };
  const setText = (id, value) => { byId(id).textContent = value; };
  const list = (value) => Array.isArray(value) ? value : [];
  const isNumber = (value) => typeof value === "number" && Number.isFinite(value);
  const number = (value, digits = 0) => isNumber(value) ? value.toLocaleString("zh-CN", { minimumFractionDigits: digits, maximumFractionDigits: digits }) : "—";
  const price = (value) => isNumber(value) ? value.toFixed(2) : "—";
  const percent = (value, digits = 1) => isNumber(value) ? `${(value * 100).toFixed(digits)}%` : "—";
  const ratio = (value) => isNumber(value) ? `${value.toFixed(3)}×` : "—";
  const el = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };
  const formatTime = (value) => {
    if (!value) return "—";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    return new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date).replaceAll("/", "-") + " 北京时间";
  };
  const issueLabels = {
    dayrange_store_unavailable: "dayrange 数据库尚未初始化",
    audit_has_unresolved_samples: "数据审计仍包含未核对样本",
    audit_read_failed: "审计记录读取失败",
    prediction_read_failed: "预测记录读取失败",
    candidate_not_validated: "候选模型尚未通过门禁",
    validated_model_unavailable: "暂无通过门禁的模型，正式结果使用历史波幅基线",
    model_read_failed: "模型记录读取失败",
    calendar_unavailable: "交易日历暂时不可用",
    dayrange_disabled: "dayrange 功能未启用"
  };
  const reasonLabels = {
    missing_reference: "缺少上一交易日参考价",
    target_suspended: "目标日停牌",
    target_status_unknown: "目标日交易状态未知",
    source_inconsistent: "行情源或状态冲突",
    adjusted_history_missing: "缺少复权交叉校验数据",
    corporate_action: "检测到除权除息",
    invalid_prices: "日线价格无效",
    missing_sector: "缺少行业归属",
    insufficient_history: "历史长度不足",
    snapshot_not_ready: "冻结快照不完整",
    already_published: "已保存，不重复发布"
  };
  const gateLabels = {
    missing_required_metrics: "缺少门禁所需指标",
    insufficient_validation: "验证交易日或样本数不足",
    high_point_not_5pct_better: "最高价 MAE 未改善 5%",
    low_point_not_5pct_better: "最低价 MAE 未改善 5%",
    marginal_coverage_outside_75_85: "边际覆盖率不在 75%–85%",
    interval_width_over_110pct: "区间宽度超过基线 110%"
  };
  const state = { data: null, controller: null, requestID: 0, refreshing: false };

  async function requestJSON(path, signal) {
    const controller = new AbortController();
    const forwardAbort = () => controller.abort();
    if (signal?.aborted) controller.abort();
    signal?.addEventListener("abort", forwardAbort, { once: true });
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, 10000);
    try {
      const response = await fetch(path, { signal: controller.signal, cache: "no-store", headers: { Accept: "application/json" } });
      let payload = null;
      try { payload = await response.json(); } catch (error) { if (controller.signal.aborted) throw error; }
      if (!response.ok) throw new Error(payload?.error || `请求失败（HTTP ${response.status}）`);
      return payload;
    } catch (error) {
      if (timedOut) throw new Error("请求超时，请稍后重试。");
      throw error;
    } finally {
      clearTimeout(timer);
      signal?.removeEventListener("abort", forwardAbort);
    }
  }

  function renderStatus(status, response) {
    const enabled = status?.enabled === true;
    const available = status?.available === true;
    setText("page-status-tag", enabled ? (available ? "已启用 · 只读研究" : "已启用 · 存储不可用") : "未启用");
    byId("page-status-tag").className = `tag ${enabled && available ? "tag-neutral" : "tag-gap"}`;
    const date = typeof status?.date === "string" ? status.date : "";
    setText("snapshot-date", date || "暂无日期");
    setText("target-date", date || "—");
    setText("prediction-count", number(status?.prediction_count || 0));
    setText("resolved-count", number(status?.resolved_actuals || 0));
    setText("unresolved-count", number(status?.unresolved_actuals || 0));
    const model = status?.active_model;
    setText("active-model", model?.model_id ? `${model.kind === "baseline" ? "基线" : "模型"} · ${model.model_id.slice(0, 12)}` : "历史波幅基线");
    if (model?.model_id) byId("active-model").title = model.model_id;
    setText("feature-version", model?.feature_version ?? "—");
    const issues = [...new Set([...list(status?.issues), ...list(response?.issues)])];
    byId("status-issue-list").replaceChildren(...issues.map((issue) => el("li", "", issueLabels[issue] || issue)));
    show("status-issues", issues.length > 0);
  }

  function intervalFor(prediction, side) {
    return side === "high" ? prediction.interval_high : prediction.interval_low;
  }

  function coverageFor(record) {
    const actual = record.actual;
    if (!actual || actual.status !== "resolved" || !actual.actual) return null;
    const high = intervalFor(record, "high");
    const low = intervalFor(record, "low");
    if (!high || !low) return { high: null, low: null, joint: null };
    return {
      high: actual.actual.high >= high.lower && actual.actual.high <= high.upper,
      low: actual.actual.low >= low.lower && actual.actual.low <= low.upper,
      joint: actual.actual.high >= high.lower && actual.actual.high <= high.upper && actual.actual.low >= low.lower && actual.actual.low <= low.upper,
    };
  }

  function estimateCell(estimate) {
    const cell = el("td", "numeric");
    const stack = el("div", "estimate-stack");
    stack.append(el("strong", "", price(estimate?.point)));
    const quantiles = list(estimate?.quantiles);
    if (quantiles.length) {
      stack.append(el("span", "", quantiles.map((q) => `${percent(q.level, 0)} ${price(q.price)}`).join(" · ")));
    } else {
      stack.append(el("span", "", "无分位数"));
    }
    cell.append(stack);
    return cell;
  }

  function intervalCell(prediction) {
    const cell = el("td", "numeric");
    const stack = el("div", "interval-stack");
    const high = prediction.interval_high;
    const low = prediction.interval_low;
    if (high && low) {
      stack.append(el("strong", "", `高 ${price(high.lower)} – ${price(high.upper)}`));
      stack.append(el("strong", "", `低 ${price(low.lower)} – ${price(low.upper)}`));
      stack.append(el("span", "", `${percent(high.nominal_coverage, 0)} 边际区间 · ${high.method || "—"}`));
    } else {
      stack.append(el("span", "", "无预测区间"));
    }
    cell.append(stack);
    return cell;
  }

  function actualCell(record) {
    const cell = el("td", "numeric");
    const actual = record.actual;
    const stack = el("div", "estimate-stack");
    if (actual?.status === "resolved" && actual.actual) {
      stack.append(el("strong", "", `高 ${price(actual.actual.high)}`));
      stack.append(el("strong", "", `低 ${price(actual.actual.low)}`));
      stack.append(el("span", "", `${actual.source || "日线"} · ${actual.observed_through || "—"}`));
    } else {
      stack.append(el("span", "", actual ? (reasonLabels[actual.reason] || actual.reason || "未核对") : "尚未核对"));
    }
    cell.append(stack);
    return cell;
  }

  function coverageCell(record) {
    const cell = el("td");
    const stack = el("div", "coverage-stack");
    const coverage = coverageFor(record);
    if (!coverage) {
      stack.append(el("span", "tag tag-neutral", "待核对"));
    } else {
      for (const [name, value] of [["高", coverage.high], ["低", coverage.low], ["联合", coverage.joint]]) {
        if (value === null) stack.append(el("span", "tag tag-neutral", `${name}无区间`));
        else stack.append(el("span", `tag ${value ? "tag-coverage-ok" : "tag-coverage-miss"}`, `${name}${value ? "覆盖" : "突破"}`));
      }
    }
    cell.append(stack);
    return cell;
  }

  function renderPredictions(records, stockNames) {
    const fragment = document.createDocumentFragment();
    for (const record of records) {
      const row = el("tr");
      row.dataset.code = record.code || "";
      const identity = el("td");
      const link = el("a", "stock-name stock-link", stockNames?.[record.code] || record.code || "未命名股票");
      link.href = `/chart?code=${encodeURIComponent(record.code || "")}`;
      identity.append(link, el("span", "stock-code", record.code || "—"));
      row.append(identity);
      const reference = el("td", "numeric");
      reference.append(el("strong", "", price(record.reference_price)));
      row.append(reference, estimateCell(record.quantiles_high ? { point: record.point_high, quantiles: record.quantiles_high } : null), estimateCell(record.quantiles_low ? { point: record.point_low, quantiles: record.quantiles_low } : null), intervalCell(record), actualCell(record), coverageCell(record));
      const source = el("td");
      const sourceStack = el("div", "source-stack");
      sourceStack.append(el("strong", "", record.source === "model" ? "门禁模型" : "历史波幅基线"));
      sourceStack.append(el("span", "", `模型 ${record.model_id || "—"}`));
      sourceStack.append(el("span", "", `预测 ${formatTime(record.as_of)}`));
      sourceStack.append(el("span", "", `数据截至 ${record.input_through || "—"} · 保存 ${formatTime(record.created_at)}`));
      source.append(sourceStack);
      row.append(source);
      fragment.append(row);
    }
    byId("prediction-body").replaceChildren(fragment);
    show("prediction-empty", records.length === 0);
  }

  function findMetric(report, metricID, target, space) {
    return list(report?.metrics).find((metric) => {
      if (metric.metric_id !== metricID || metric.target !== target || metric.space !== space || metric.aggregation !== "day_equal" || metric.applicability !== "ok" || !isNumber(metric.value)) return false;
      const names = list(metric.slice).map((item) => item.name);
      return names.includes("as_of") && names.includes("mode") && !names.some((name) => ["month", "code", "market_state", "volatility"].includes(name));
    })?.value;
  }

  function evaluationCard(label, value, note) {
    const card = el("article");
    card.append(el("span", "", label), el("strong", "", value), el("small", "", note));
    return card;
  }

  function renderEvaluation(evaluation) {
    const gate = evaluation?.gate;
    const present = Boolean(evaluation && gate);
    show("evaluation-content", present);
    show("evaluation-empty", !present);
    setText("gate-tag", present ? (gate.passed ? "门禁通过" : "未通过 · 仅研究") : "暂无评估");
    byId("gate-tag").className = `tag ${present ? (gate.passed ? "tag-selected" : "tag-gap") : "tag-neutral"}`;
    if (!present) return;
    const candidate = evaluation.candidate;
    const baseline = evaluation.baseline;
    const baselineHighMAE = findMetric(baseline, "point.mae", "high", "price");
    const baselineLowMAE = findMetric(baseline, "point.mae", "low", "price");
    const values = [
      evaluationCard("验证区间", `${evaluation.validation_from || "—"} → ${evaluation.validation_to || "—"}`, `${gate.validation_days} 个交易日 · ${gate.validation_samples} 个配对样本`),
      evaluationCard("最高价 MAE", number(gate.high_mae, 3), `基线 ${number(baselineHighMAE, 3)} CNY · ${candidate?.resolved ?? 0} 条已核对`),
      evaluationCard("最低价 MAE", number(gate.low_mae, 3), `基线 ${number(baselineLowMAE, 3)} CNY`),
      evaluationCard("边际覆盖率", `${percent(gate.high_coverage)} / ${percent(gate.low_coverage)}`, "高 / 低；门禁要求各自 75%–85%"),
      evaluationCard("区间宽度比", `${ratio(gate.high_width_ratio)} / ${ratio(gate.low_width_ratio)}`, "高 / 低；不得超过基线 1.10×"),
      evaluationCard("联合覆盖率", percent(gate.joint_coverage), "仅报告，不单独作为替换条件"),
      evaluationCard("验证样本", number(gate.validation_samples), "至少 100 个配对样本、20 个验证交易日"),
      evaluationCard("模型状态", gate.passed ? "validated" : "research", evaluation.model_id ? `模型 ${evaluation.model_id.slice(0, 16)}` : "仅保存研究档案"),
    ];
    byId("evaluation-grid").replaceChildren(...values);
    const reasons = list(gate.reasons).map((reason) => gateLabels[reason] || reason);
    if (!reasons.length) reasons.push("全部门禁条件通过。");
    byId("gate-reasons").replaceChildren(...reasons.map((reason) => el("li", "", reason)));
  }

  async function load() {
    if (state.refreshing) return;
    state.refreshing = true;
    state.requestID += 1;
    const requestID = state.requestID;
    state.controller?.abort();
    state.controller = new AbortController();
    byId("refresh-button").disabled = true;
    byId("retry-button").disabled = true;
    byId("refresh-button").querySelector("span").textContent = "正在刷新";
    byId("day-range-content").setAttribute("aria-busy", "true");
    show("loading-state", !state.data);
    try {
      const status = await requestJSON("/api/day-range/status", state.controller.signal);
      if (requestID !== state.requestID) return;
      let response = null;
      let evaluation = status.evaluation || null;
      if (status.date) {
        response = await requestJSON(`/api/day-range?date=${encodeURIComponent(status.date)}`, state.controller.signal);
        if (requestID !== state.requestID) return;
      }
      if (requestID !== state.requestID) return;
      state.data = { status, response, evaluation };
      renderStatus(status, response);
      const records = list(response?.predictions);
      renderPredictions(records, response?.stock_names);
      const sourceNames = [...new Set(records.map((record) => record.source === "model" ? "门禁模型" : "历史波幅基线"))];
      setText("forecast-source", sourceNames.length ? sourceNames.join(" / ") : "—");
      const first = records[0];
      setText("input-through", first?.input_through || "—");
      setText("data-cutoff-tag", first?.input_through ? `数据截至 ${first.input_through}` : "数据截至 —");
      setText("prediction-empty-reason", status.enabled ? "当前日期尚无冻结预测；请确认训练、模型与行情数据状态。" : "dayrange 功能尚未启用；启用后由训练和盘前任务生成记录。");
      renderEvaluation(evaluation || status.evaluation);
      show("day-range-content", true);
      show("error-state", false);
    } catch (error) {
      if (error.name === "AbortError" && requestID !== state.requestID) return;
      setText("error-title", state.data ? "刷新失败，保留上次结果" : "全天区间加载失败");
      setText("error-message", `${error.message || "请稍后重试。"}${state.data ? " 当前页面保留上次成功读取的数据。" : ""}`);
      show("error-state", true);
    } finally {
      if (requestID === state.requestID) {
        state.refreshing = false;
        byId("refresh-button").disabled = false;
        byId("retry-button").disabled = false;
        byId("refresh-button").querySelector("span").textContent = "刷新预测";
        byId("day-range-content").setAttribute("aria-busy", "false");
        show("loading-state", false);
      }
    }
  }

  byId("refresh-button").addEventListener("click", load);
  byId("retry-button").addEventListener("click", load);
  setInterval(() => { if (!document.hidden) load(); }, 60000);
  document.addEventListener("visibilitychange", () => {
    setText("auto-refresh-label", document.hidden ? "页面隐藏，自动刷新已暂停" : "每 60 秒自动刷新");
    if (!document.hidden) load();
  });
  load();
})();
