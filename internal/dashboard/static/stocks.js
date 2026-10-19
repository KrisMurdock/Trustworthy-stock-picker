"use strict";

(() => {
  const { byId, setText, show, el, number, isNumber, displayLabel, statusLabels, volume, formattedVolume, stockIdentity, loadDashboard } = window.StockWatcher;
  const state = { data: null, sort: "code", direction: 1, refreshing: false };

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

  function renderStocks() {
    if (!state.data) return;
    const stocks = Array.isArray(state.data.stocks) ? state.data.stocks : [];
    const query = byId("stock-search").value.trim().toLocaleLowerCase();
    const sector = byId("pool-filter").value;
    const status = byId("data-filter").value;
    const visible = stocks.filter((stock) => (!query || `${stock.name || ""} ${stock.code || ""}`.toLocaleLowerCase().includes(query)) && (!sector || (stock.sector || "未分类") === sector) && (!status || (status === "available" ? !!stock.bar : status === "missing" ? !stock.bar && stock.status !== "suspended" && stock.status !== "gap" : stock.status === status))).sort(compareStocks);
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
      const stockStatus = Object.hasOwn(statusLabels, stock.status) ? stock.status : "missing";
      statusCell.append(el("span", `tag tag-${stockStatus}`, statusLabels[stockStatus]));
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

  function syncStatusURL() {
    const url = new URL(location.href);
    const status = byId("data-filter").value;
    if (status) url.searchParams.set("status", status);
    else url.searchParams.delete("status");
    history.replaceState(null, "", url.pathname + (url.search || ""));
  }

  function applyStatusFromURL() {
    const status = new URLSearchParams(location.search).get("status");
    byId("data-filter").value = ["available", "missing", "suspended", "gap", "isolated"].includes(status) ? status : "";
  }

  async function refreshStocks() {
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
      updateSectorOptions(data.stocks);
      renderStocks();
      const adjustLabels = { qfq: "前复权", hfq: "后复权", none: "不复权" };
      const adjust = adjustLabels[data.adjust] || data.adjust || "—";
      setText("stocks-footer", `${data.as_of || "等待同步"} · ${adjust}日线 · 成交量已统一换算为股`);
      show("dashboard-content", true);
      show("error-state", false);
      setText("live-status", `股池已刷新，共 ${data.stocks.length} 支股票。`);
    } catch (error) {
      setText("error-title", state.data ? "刷新失败，数据已过期" : "股票池加载失败");
      setText("error-message", `${error.message}${state.data ? " 当前保留上次成功读取的股票池。" : ""}`);
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

  applyStatusFromURL();
  syncStatusURL();
  byId("refresh-button").addEventListener("click", refreshStocks);
  byId("retry-button").addEventListener("click", refreshStocks);
  byId("stock-search").addEventListener("input", renderStocks);
  byId("pool-filter").addEventListener("change", renderStocks);
  byId("data-filter").addEventListener("change", () => { syncStatusURL(); renderStocks(); });
  byId("stock-sort").addEventListener("change", (event) => { state.sort = event.target.value; state.direction = ["code", "name"].includes(state.sort) ? 1 : -1; renderStocks(); });
  byId("sort-direction").addEventListener("click", () => { state.direction *= -1; renderStocks(); });
  byId("clear-filters").addEventListener("click", () => {
    byId("stock-search").value = "";
    byId("pool-filter").value = "";
    byId("data-filter").value = "";
    syncStatusURL();
    renderStocks();
    byId("stock-search").focus();
  });
  for (const button of document.querySelectorAll("[data-column]")) button.addEventListener("click", () => {
    const field = button.dataset.column;
    state.direction = state.sort === field ? -state.direction : field === "code" ? 1 : -1;
    state.sort = field;
    renderStocks();
  });
  setInterval(() => { if (!document.hidden) refreshStocks(); }, 60000);
  document.addEventListener("visibilitychange", () => {
    setText("auto-refresh-label", document.hidden ? "页面隐藏，自动刷新已暂停" : "每 60 秒自动刷新");
    if (!document.hidden) refreshStocks();
  });
  refreshStocks();
})();
