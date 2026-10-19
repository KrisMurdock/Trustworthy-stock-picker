"use strict";

(() => {
  const { byId, setText, show, loadDashboard, requestJSON } = window.StockWatcher;
  const state = { data: null, refreshing: false };
  const chart = new window.StockChart(requestJSON);

  function readCode() {
    return new URLSearchParams(location.search).get("code") || "";
  }

  function syncCode(code) {
    const url = new URL(location.href);
    if (code) url.searchParams.set("code", code);
    else url.searchParams.delete("code");
    history.replaceState(null, "", url.pathname + (url.search || ""));
  }

  async function refreshChart() {
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
      const preferred = readCode() || chart.code;
      chart.update(data.stocks, data.adjust, preferred);
      syncCode(chart.code);
      show("dashboard-content", true);
      show("error-state", false);
      setText("live-status", `K 线股票列表已刷新，共 ${data.stocks.length} 支股票。`);
    } catch (error) {
      setText("error-title", state.data ? "刷新失败，数据已过期" : "K 线股票列表加载失败");
      setText("error-message", `${error.message}${state.data ? " 当前保留上次成功读取的图表。" : ""}`);
      show("error-state", true);
    } finally {
      state.refreshing = false;
      byId("refresh-button").disabled = false;
      byId("retry-button").disabled = false;
      byId("refresh-button").querySelector("span").textContent = "刷新 K 线";
      byId("dashboard-content").setAttribute("aria-busy", "false");
      show("loading-state", false);
    }
  }

  byId("chart-stock").addEventListener("change", (event) => syncCode(event.target.value));
  byId("refresh-button").addEventListener("click", refreshChart);
  byId("retry-button").addEventListener("click", refreshChart);
  refreshChart();
})();
