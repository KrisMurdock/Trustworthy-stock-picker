"use strict";

(() => {
  const { byId, el, list, number, isNumber, formatTime, show } = window.StockWatcher;
  const state = { data: null, asOf: "", query: "", category: "all", sort: "change_desc", limit: 200, controller: null };
  const statusLabels = { complete: "已完整同步", partial: "部分同步", failed: "同步失败", running: "同步中", missing: "尚未同步" };
  const trendLabels = { up: "多头", down: "空头", mixed: "震荡", unknown: "待积累" };

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
  }

  async function load(asOf) {
    if (state.controller) state.controller.abort();
    const controller = new AbortController();
    state.controller = controller;
    state.asOf = asOf || "";
    show("market-context-loading", true);
    show("market-context-empty", false);
    show("market-context-body", false);
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
