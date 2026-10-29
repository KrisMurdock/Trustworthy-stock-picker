"use strict";

(() => {
  const { byId, el, list, number, isNumber, formatTime, show } = window.StockWatcher;
  const state = { data: null, asOf: "", query: "", category: "all", sort: "change_desc", limit: 200, controller: null, signalController: null };
  const statusLabels = { complete: "已完整同步", partial: "部分同步", failed: "同步失败", running: "同步中", missing: "尚未同步" };
  const trendLabels = { up: "多头", down: "空头", mixed: "震荡", unknown: "待积累" };
  const signalStatusLabels = { ok: "已生成", stale: "数据滞后", partial: "部分可用", insufficient_history: "样本不足" };
  const directionLabels = { up: "偏多", down: "偏空" };
  const strengthLabels = { weak: "弱", medium: "中", strong: "强" };
  const conclusionLabels = {
    model_above_baseline: "留出验证优于基线",
    weekday_above_model: "周内基线优于模型",
    no_clear_edge: "未见明确优势",
  };
  const sessionConclusionLabels = {
    high_confidence_above_target: "滚动高置信子集达到 90% 点估计",
    high_confidence_above_baseline: "滚动高置信子集优于基线",
    model_above_baseline: "滚动验证优于基线",
    no_clear_edge: "未见明确优势",
  };

  function pct(value) {
    if (!isNumber(value)) return "—";
    const sign = value > 0 ? "+" : "";
    return `${sign}${number(value, 2)}%`;
  }

  function price(value) {
    return isNumber(value) ? number(value, 2) : "—";
  }

  function changeClass(value) {
    if (!isNumber(value) || Math.abs(value) < 0.0001) return "is-flat";
    return value > 0 ? "is-up" : "is-down";
  }

  function setEmpty(message) {
    byId("market-context-empty-note").textContent = message;
    show("market-context-loading", false);
    show("market-context-empty", true);
    show("market-context-body", false);
  }

  function setStatus(data) {
    const badge = byId("market-context-status");
    badge.textContent = statusLabels[data.status] || data.status || "未知";
    badge.dataset.status = data.status || "missing";
    byId("market-context-as-of").textContent = data.as_of || "—";
    byId("market-context-synced-at").textContent = formatTime(data.synced_at);
    byId("market-context-note").textContent = data.failed
      ? `已读取 ${number(data.complete)} / ${number(data.total)} 个指数与板块；${number(data.failed)} 个同步失败，可重新执行默认同步。`
      : "同步上证、深证、创业板等核心指数，以及东方财富行业与概念板块全量日线。";
  }

  function renderWarnings(data) {
    const host = byId("market-context-warnings");
    host.replaceChildren();
    const warnings = list(data.warnings);
    if (data.sync_errors?.length) warnings.push(`同步失败示例：${data.sync_errors.slice(0, 3).join("；")}`);
    if (!warnings.length) {
      show("market-context-warnings", false);
      return;
    }
    host.append(el("strong", "", "数据限制"), el("p", "", warnings.join(" · ")));
    show("market-context-warnings", true);
  }

  function renderIndices(data) {
    const host = byId("market-context-indices");
    host.replaceChildren();
    const items = list(data.indices);
    if (!items.length) {
      host.append(el("p", "market-context-inline-empty", "暂无核心指数快照"));
      return;
    }
    for (const item of items) {
      const card = el("article", "market-context-index-card");
      const heading = el("div", "market-context-index-heading");
      heading.append(el("strong", "", item.name || item.code), el("span", "", item.code));
      const quote = el("div", "market-context-index-quote");
      quote.append(el("strong", changeClass(item.change_pct), price(item.close)), el("span", changeClass(item.change_pct), pct(item.change_pct)));
      const meta = el("div", "market-context-index-meta");
      meta.append(el("span", "", `MA5 ${price(item.ma5)}`), el("span", "", `MA20 ${price(item.ma20)}`), el("span", `market-context-trend ${item.trend || "unknown"}`, trendLabels[item.trend] || "待积累"));
      card.append(heading, quote, meta);
      if (item.stale) card.classList.add("is-stale");
      host.append(card);
    }
  }

  function weekdayOf(date) {
    if (!date) return 0;
    const parsed = new Date(`${date}T00:00:00Z`);
    return Number.isNaN(parsed.getTime()) ? 0 : parsed.getUTCDay();
  }

  function renderSession(session) {
    const item = el("div", "market-signal-session");
    item.dataset.testid = "market-signal-session";
    const heading = el("div", "market-signal-session-heading");
    heading.append(el("b", "", session.label || session.session || "—"));
    heading.append(el("span", `market-signal-session-status ${session.status || "missing"}`, signalStatusLabels[session.status] || "待计算"));
    item.append(heading);
    if (session.status === "ok") {
      const verdict = el("div", `market-signal-session-verdict ${session.direction || "unknown"}`);
      verdict.append(el("strong", "", `${directionLabels[session.direction] || "待定"} ${number(session.up_probability * 100, 1)}%`));
      if (session.high_confidence) verdict.append(el("em", "", "模型高置信"));
      item.append(verdict);
    } else {
      item.append(el("p", "market-signal-session-unavailable", session.reason || "需要更多指数分钟历史"));
    }
    const validation = session.validation || {};
    if (validation.status === "ok") {
      const evidence = el("p", "market-signal-session-evidence");
      evidence.textContent = `留出 ${number(validation.samples)} 天 ${number(validation.accuracy * 100, 1)}% / 多数 ${number(validation.majority_accuracy * 100, 1)}% / 周内 ${number(validation.weekday_accuracy * 100, 1)}%`;
      item.append(evidence);
      if (validation.walk_forward_samples) {
        const walk = el("p", "market-signal-session-evidence");
        walk.textContent = `滚动验证 ${number(validation.walk_forward_samples)} 天 ${number(validation.walk_forward_accuracy * 100, 1)}% / 多数 ${number(validation.walk_forward_majority_accuracy * 100, 1)}%`;
        item.append(walk);
      }
      if (validation.high_confidence_samples) {
        const high = el("p", "market-signal-session-evidence is-high");
        const coverage = number((validation.high_confidence_coverage || 0) * 100, 1);
        const low = number((validation.high_confidence_wilson_low || 0) * 100, 1);
        const highBound = number((validation.high_confidence_wilson_high || 0) * 100, 1);
        high.textContent = `滚动高置信 ${number(validation.high_confidence_samples)} 天（覆盖 ${coverage}%）${number(validation.high_confidence_accuracy * 100, 1)}% · Wilson ${low}–${highBound}%`;
        item.append(high);
      }
      const conclusion = el("p", "market-signal-session-evidence");
      conclusion.textContent = `${sessionConclusionLabels[validation.conclusion] || "待观察"}${validation.target_met ? " · 点估计达到 90% 目标" : ""}`;
      item.append(conclusion);
    }
    const weekdays = list(session.weekdays);
    if (weekdays.length) {
      const line = el("p", "market-signal-session-weekdays");
      line.textContent = weekdays.map((stat) => `${stat.label} ${number(stat.win_rate * 100, 1)}%`).join(" · ");
      item.append(line);
    }
    return item;
  }

  function renderSignal(report) {
    const panel = byId("market-signal-panel");
    const cards = byId("market-signal-cards");
    cards.replaceChildren();
    show("market-signal-panel", true);
    const badge = byId("market-signal-status");
    badge.textContent = signalStatusLabels[report.status] || report.status || "未知";
    badge.dataset.status = report.status || "missing";
    byId("market-signal-note").textContent = report.status === "stale"
      ? "指数历史落后于所选日期；方向信号按最新已保存交易日计算，仅用于观察。"
      : "日线方向与上午/下午方向均为盘前信号；高置信仅表示模型概率超过阈值，90% 目标需同时看覆盖率与 Wilson 区间。";
    const signals = list(report.signals);
    if (!signals.length) {
      cards.append(el("p", "market-context-inline-empty", "暂无可计算的核心指数方向信号。"));
      return;
    }
    for (const signal of signals) {
      const card = el("article", `market-signal-card${signal.stale ? " is-stale" : ""}`);
      card.dataset.testid = "market-signal-card";
      const heading = el("div", "market-signal-card-heading");
      const identity = el("div", "market-signal-identity");
      identity.append(el("strong", "", signal.name || signal.code), el("span", "", signal.code || "—"));
      heading.append(identity, el("span", "market-signal-target", signal.target_date ? `目标 ${signal.target_date}` : "目标日待定"));
      card.append(heading);

      const quote = el("div", "market-signal-quote");
      quote.append(el("strong", changeClass(signal.change_pct), price(signal.close)), el("span", changeClass(signal.change_pct), pct(signal.change_pct)));
      card.append(quote);

      if (signal.status === "ok") {
        const verdict = el("div", "market-signal-verdict");
        const label = directionLabels[signal.direction] || "待定";
        verdict.append(el("strong", `market-signal-direction ${signal.direction || "unknown"}`, `${label} ${number(signal.up_probability * 100, 1)}%`));
        verdict.append(el("span", "", `上涨概率 · 信号强度 ${strengthLabels[signal.strength] || "—"}`));
        card.append(verdict);
      } else {
        const unavailable = el("div", "market-signal-verdict is-unavailable");
        unavailable.append(el("strong", "", "样本不足，暂不输出方向"), el("span", "", signal.reason || "需要更多已完成交易日"));
        card.append(unavailable);
      }

      const validation = signal.validation || {};
      const validationLine = el("p", "market-signal-evidence");
      if (validation.status === "ok") {
        const baseline = Math.max(validation.majority_accuracy || 0, validation.weekday_accuracy || 0) * 100;
        validationLine.textContent = `样本外 ${validation.samples} 天：模型 ${number(validation.accuracy * 100, 1)}% / 基线 ${number(baseline, 1)}% · ${conclusionLabels[validation.conclusion] || "待观察"}`;
      } else {
        validationLine.textContent = "样本外验证尚不可用；周内统计仍按本地历史展示。";
      }
      card.append(validationLine);

      const sessions = el("div", "market-signal-sessions");
      sessions.dataset.testid = "market-signal-sessions";
      const sessionItems = list(signal.sessions);
      if (sessionItems.length) {
        for (const session of sessionItems) sessions.append(renderSession(session));
      } else {
        sessions.append(el("p", "market-signal-session-unavailable", "指数分钟历史尚未同步；运行 sync-index-minutes 后生成上午/下午信号。"));
      }
      card.append(sessions);

      const weekdays = el("div", "market-signal-weekdays");
      weekdays.dataset.testid = "market-signal-weekdays";
      const targetWeekday = weekdayOf(signal.target_date);
      for (const stat of list(signal.weekdays)) {
        const item = el("div", `market-signal-weekday${stat.weekday === targetWeekday ? " is-target" : ""}`);
        item.title = `${stat.label}：${number(stat.samples)} 个样本，上涨 ${number(stat.up)}，下跌 ${number(stat.down)}`;
        item.append(el("b", "", stat.label), el("span", "", number(stat.win_rate * 100, 1) + "%"), el("small", "", `n=${number(stat.samples)}`));
        weekdays.append(item);
      }
      card.append(weekdays);
      const training = signal.training || {};
      const footer = el("p", "market-signal-footer");
      footer.textContent = `训练 ${number(training.samples)} 个交易日${training.from ? `（${training.from} 至 ${training.to}）` : ""} · 周内数值为上涨胜率`;
      card.append(footer);
      cards.append(card);
    }
    if (list(report.warnings).length) {
      byId("market-signal-note").textContent += " 数据限制：" + report.warnings.join("；");
    }
  }

  async function loadSignal(asOf) {
    if (state.signalController) state.signalController.abort();
    const controller = new AbortController();
    state.signalController = controller;
    const url = asOf ? `/api/market-signal?as_of=${encodeURIComponent(asOf)}` : "/api/market-signal";
    try {
      const response = await fetch(url, { signal: controller.signal, cache: "no-store", headers: { Accept: "application/json" } });
      let payload = null;
      try { payload = await response.json(); } catch (error) { if (controller.signal.aborted) throw error; }
      if (response.status === 404) {
        show("market-signal-panel", false);
        return;
      }
      if (!response.ok) throw new Error(payload?.error || "市场方向信号读取失败");
      if (!payload || payload.schema_version !== 1 || !Array.isArray(payload.signals)) {
        throw new Error("市场方向信号格式不匹配");
      }
      renderSignal(payload);
    } catch (error) {
      if (error.name === "AbortError") return;
      show("market-signal-panel", true);
      byId("market-signal-status").textContent = "暂不可用";
      byId("market-signal-status").dataset.status = "failed";
      byId("market-signal-note").textContent = "市场方向信号暂时不可用，指数与板块快照不受影响。";
      byId("market-signal-cards").replaceChildren(el("p", "market-context-inline-empty", error.message || "读取失败"));
    } finally {
      if (state.signalController === controller) state.signalController = null;
    }
  }

  function renderSummary(data) {
    const summary = data.summary || {};
    byId("market-context-industry").textContent = number(summary.industry);
    byId("market-context-concept").textContent = number(summary.concept);
    byId("market-context-up").textContent = number(summary.up);
    byId("market-context-down").textContent = number(summary.down);
    byId("market-context-flat").textContent = number(summary.flat);
  }

  function matchesCategory(board, category) {
    if (category === "all") return true;
    return String(board.category || "").split(",").includes(category);
  }

  function filteredBoards() {
    if (!state.data) return [];
    const query = state.query.trim().toLowerCase();
    const boards = list(state.data.boards).filter((board) => {
      if (!matchesCategory(board, state.category)) return false;
      if (!query) return true;
      return `${board.name || ""} ${board.code || ""}`.toLowerCase().includes(query);
    });
    boards.sort((a, b) => {
      if (state.sort === "change_asc") return (a.change_pct ?? -Infinity) - (b.change_pct ?? -Infinity);
      if (state.sort === "name") return String(a.name || "").localeCompare(String(b.name || ""), "zh-CN");
      if (state.sort === "code") return String(a.code || "").localeCompare(String(b.code || ""));
      return (b.change_pct ?? -Infinity) - (a.change_pct ?? -Infinity);
    });
    return boards;
  }

  function renderBoards() {
    const host = byId("market-context-board-list");
    host.replaceChildren();
    const boards = filteredBoards();
    const visible = boards.slice(0, state.limit);
    for (const board of visible) {
      const row = el("div", "market-context-board-row", "");
      row.dataset.code = board.code || "";
      const identity = el("div", "market-context-board-identity");
      identity.append(el("strong", "", board.name || "未命名板块"), el("span", "", board.code || "—"));
      row.append(identity);
      row.append(el("span", "market-context-category", board.category === "industry,concept" ? "行业 + 概念" : board.category === "concept" ? "概念" : "行业"));
      row.append(el("span", "market-context-number", price(board.close)));
      row.append(el("span", `market-context-number ${changeClass(board.change_pct)}`, pct(board.change_pct)));
      row.append(el("span", "market-context-number", price(board.ma5)));
      row.append(el("span", "market-context-number", price(board.ma20)));
      row.append(el("span", `market-context-trend ${board.trend || "unknown"}`, trendLabels[board.trend] || "待积累"));
      const date = el("span", `market-context-date${board.stale ? " is-stale" : ""}`, board.as_of || "—");
      if (board.stale) date.title = "该板块在所选日期没有最新日线，显示最近有效截止日";
      row.append(date);
      host.append(row);
    }
    if (!visible.length) {
      host.append(el("p", "market-context-inline-empty", state.query ? "没有匹配的板块，请调整搜索词。" : "当日没有可展示的板块快照。"));
    }
    byId("market-context-count").textContent = `显示 ${number(visible.length)} / ${number(boards.length)} 个板块`;
    show("market-context-more", boards.length > visible.length);
  }

  function render(data) {
    state.data = data;
    setStatus(data);
    renderWarnings(data);
    renderIndices(data);
    renderSummary(data);
    state.limit = 200;
    renderBoards();
    show("market-context-loading", false);
    show("market-context-empty", false);
    show("market-context-body", true);
    void loadSignal(state.asOf || data.as_of);
  }

  async function load(asOf) {
    if (state.controller) state.controller.abort();
    if (state.signalController) state.signalController.abort();
    const controller = new AbortController();
    state.controller = controller;
    state.asOf = asOf || "";
    show("market-context-loading", true);
    show("market-context-empty", false);
    show("market-context-body", false);
    show("market-signal-panel", false);
    const url = state.asOf ? `/api/market-context?as_of=${encodeURIComponent(state.asOf)}` : "/api/market-context";
    try {
      const response = await fetch(url, { signal: controller.signal, cache: "no-store", headers: { Accept: "application/json" } });
      let payload = null;
      try { payload = await response.json(); } catch (error) { if (controller.signal.aborted) throw error; }
      if (response.status === 404) {
        const day = state.asOf ? ` ${state.asOf} ` : " ";
        setEmpty(`该日期${day}没有指数与板块快照。默认同步完成后会自动出现在这里。`);
        byId("market-context-status").textContent = "尚无快照";
        byId("market-context-status").dataset.status = "missing";
        return;
      }
      if (!response.ok) throw new Error(payload?.error || "市场环境读取失败");
      if (!payload || payload.schema_version !== 1 || !Array.isArray(payload.indices) || !Array.isArray(payload.boards)) {
        throw new Error("市场环境快照格式不匹配");
      }
      render(payload);
    } catch (error) {
      if (error.name === "AbortError") return;
      setEmpty("市场环境暂时不可用，请稍后刷新复盘页面。");
      byId("market-context-status").textContent = "读取失败";
      byId("market-context-status").dataset.status = "failed";
    } finally {
      if (state.controller === controller) state.controller = null;
    }
  }

  function wire() {
    const panel = byId("market-context-panel");
    if (!panel) return;
    byId("market-context-search").addEventListener("input", (event) => {
      state.query = event.target.value;
      state.limit = 200;
      renderBoards();
    });
    byId("market-context-category").addEventListener("change", (event) => {
      state.category = event.target.value;
      state.limit = 200;
      renderBoards();
    });
    byId("market-context-sort").addEventListener("change", (event) => {
      state.sort = event.target.value;
      renderBoards();
    });
    byId("market-context-more").addEventListener("click", () => {
      state.limit += 200;
      renderBoards();
    });
    for (const button of panel.querySelectorAll("[data-context-query]")) {
      button.addEventListener("click", () => {
        byId("market-context-search").value = button.dataset.contextQuery || "";
        state.query = byId("market-context-search").value;
        state.limit = 200;
        renderBoards();
      });
    }
    const dateSelect = byId("review-date-select");
    if (dateSelect) {
      dateSelect.addEventListener("change", () => {
        if (dateSelect.value) load(dateSelect.value);
      });
    }
    load("");
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", wire, { once: true });
  else wire();
})();
