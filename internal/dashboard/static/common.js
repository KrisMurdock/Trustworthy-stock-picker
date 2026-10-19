"use strict";

(() => {
  const byId = (id) => document.getElementById(id);
  const statusLabels = { available: "已同步 · 未发布推荐", selected: "已入选", eligible: "有效数据", filtered: "未通过筛选", excluded: "暂不参与", missing: "数据缺失" };
  const labels = {
    missing_data: "尚无行情数据", invalid_or_stale_data: "数据无效或已过期", no_history: "暂无历史数据",
    stale_or_suspended: "数据滞后或停牌", no_trading_volume: "当日无成交量", insufficient_or_invalid_history: "历史数据不足或无效",
    special_treatment: "风险警示股票", no_bullish_alignment: "均线未形成多头排列", insufficient_volume_or_macd: "量能或 MACD 未达条件",
    historical_net_loss_limit_breached: "历史单笔净亏损超限", overheat_or_weakness: "过热或趋势偏弱", score_below_threshold: "趋势分未达门槛", strategy_evidence_or_risk_gate: "历史证据或风险条件未达标",
    rising_ma20: "20 日均线向上", stronger_than_pool: "近 5 日强于股池", strong_close: "收盘处于当日高位",
    insufficient_or_unprofitable_evidence: "匹配规则未通过历史检验", extended_price: "短期涨幅较大", large_daily_range: "日内波动较大",
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
  function stockIdentity(stock) {
    const cell = el("td");
    const link = el("a", "stock-name stock-link", stock.name || "未命名股票");
    link.href = stock.code ? `/chart?code=${encodeURIComponent(stock.code)}` : "/chart";
    link.setAttribute("aria-label", `查看${stock.name || stock.code}的日线 K 线`);
    cell.append(link, el("span", "stock-code", stock.code || "—"));
    return cell;
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
  async function loadDashboard() {
    const data = await requestJSON("/api/dashboard");
    if (!data || data.schema_version !== 1 || !data.summary || !Array.isArray(data.stocks)) throw new Error("快照格式不匹配，请检查服务版本。");
    return data;
  }
  window.StockWatcher = Object.freeze({
    byId, statusLabels, labels, number, isNumber, setText, show, displayLabel, formatTime, el, list,
    volume, formattedVolume, stockIdentity, requestJSON, loadDashboard,
  });
})();
