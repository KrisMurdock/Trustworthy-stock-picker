"use strict";

// Embedded SVG chart: no external scripts or market requests from the browser.
window.StockChart = class {
  constructor(requestJSON) {
    this.requestJSON = requestJSON;
    this.stocks = [];
    this.sequence = 0;
    this.code = "";
    this.byId = (id) => document.getElementById(id);
    this.byId("chart-stock").addEventListener("change", (event) => this.select(event.target.value));
    this.byId("chart-range").addEventListener("change", () => this.draw());
    this.byId("chart-period").addEventListener("change", () => this.select(this.code));
    this.byId("chart-adjust").addEventListener("change", () => this.select(this.code));
    this.byId("chart-retry").addEventListener("click", () => this.select(this.code, false, true));
    this.byId("chart-day").addEventListener("input", (event) => this.inspect(Number(event.target.value), true));
  }
  update(stocks, adjust, preferredCode = "") {
    this.stocks = stocks;
    if (this.configuredAdjust !== adjust) {
      this.configuredAdjust = adjust;
      const basis = this.byId("chart-adjust");
      basis.replaceChildren();
      for (const value of [...new Set([adjust, "none"])]) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = { qfq: "前复权 · 分析窗口", hfq: "后复权 · 分析窗口", none: "不复权 · 原始历史" }[value];
        basis.append(option);
      }
    }
    const select = this.byId("chart-stock");
    select.replaceChildren();
    for (const stock of stocks) {
      const option = document.createElement("option");
      option.value = stock.code;
      option.textContent = `${stock.name || stock.code} · ${stock.code}${stock.bar ? "" : " · 暂无数据"}`;
      select.append(option);
    }
    const selected = stocks.some((stock) => stock.code === preferredCode)
      ? preferredCode
      : stocks.some((stock) => stock.code === this.code)
      ? this.code
      : (stocks.find((stock) => stock.bar) || stocks[0])?.code || "";
    this.select(selected);
  }
  async select(code, scroll = false, force = false) {
    const stock = this.stocks.find((item) => item.code === code);
    if (!stock) return;
    this.controller?.abort();
    const sequence = ++this.sequence;
    this.controller = new AbortController();
    this.code = code;
    const minutePeriod = Number(this.byId("chart-period").value) || 0;
    this.periodLabel = minutePeriod ? `${minutePeriod} 分钟 K` : "日 K";
    const basis = this.byId("chart-adjust");
    if (minutePeriod && !basis.disabled) this.dailyAdjust = basis.value;
    if (minutePeriod) basis.value = "none";
    else if (basis.disabled) basis.value = this.dailyAdjust || this.configuredAdjust;
    this.byId("chart-range").querySelector('option[value="0"]').textContent = minutePeriod ? "全部已加载" : "全部已保存";
    const adjust = basis.value;
    basis.disabled = !!minutePeriod;
    this.byId("chart-sync-note").hidden = true;
    this.byId("chart-basis-note").textContent = minutePeriod
      ? "分钟线使用不复权价格；15/30/60 分钟由 5 分钟线聚合。虚线为形成中的 K 线，价格与成交量会继续变化；缺失数据不会补造。"
      : adjust === "none"
      ? "原始历史展示本地已保存的不复权价格；除权除息可能形成跳空，实际覆盖范围以日期为准。"
      : "复权日线展示当前分析窗口。查看本地长期历史，请切换为「不复权 · 原始历史」。";
    this.history = null;
    this.byId("chart-stock").value = code;
    this.byId("chart-title").textContent = `${stock.name || code} · ${this.periodLabel}`;
    this.byId("chart-meta").textContent = "正在读取已保存的 K 线…";
    this.byId("chart-plot").replaceChildren();
    this.byId("chart-body").hidden = true;
    this.byId("chart-error").hidden = true;
    this.byId("chart-loading").hidden = false;
    if (scroll) this.byId("stock-chart").scrollIntoView({ block: "start", behavior: "smooth" });
    if (!minutePeriod && !stock.bar && !force && adjust === this.configuredAdjust) {
      this.byId("chart-loading").hidden = true;
      this.byId("chart-meta").textContent = `${code} · 暂无可展示的日线`;
      this.byId("chart-error-message").textContent = "该股票本次暂无通过校验的日线，请查看股池中的数据缺失原因，等待下次同步。";
      this.byId("chart-error").hidden = false;
      return;
    }
    try {
      const basisQuery = adjust === this.configuredAdjust ? "" : `&adjust=${encodeURIComponent(adjust)}`;
      const url = minutePeriod ? `/api/intraday?code=${encodeURIComponent(code)}&period=${minutePeriod}` : `/api/history?code=${encodeURIComponent(code)}${basisQuery}`;
      const history = await this.requestJSON(url, this.controller.signal);
      if (sequence !== this.sequence) return;
      if (minutePeriod) {
        if (history?.schema_version !== 1 || history.code !== code || history.period_minutes !== minutePeriod || history.base_period_minutes !== 5 || history.source !== "tencent" || history.adjust !== "none" || !history.sync || !Array.isArray(history.bars) ||
            !history.bars.every((bar, i, bars) => Number.isFinite(Date.parse(bar.end)) && typeof bar.final === "boolean" && (!i || Date.parse(bars[i-1].end) < Date.parse(bar.end)))) {
          throw new Error("分钟数据不完整，请重新同步后重试。");
        }
        if (!history.bars.length) throw new Error("暂无该周期分钟数据，请等待同步完成；缺失的 5 分钟数据不会被补造。");
        history.bars = history.bars.map((bar) => ({ ...bar, date: this.minuteTime(bar.end) }));
      }
      if (history?.schema_version !== 1 || history.code !== code || history.adjust !== (minutePeriod ? "none" : adjust) || !Array.isArray(history.bars) || !history.bars.length ||
          !history.bars.every((bar, i, bars) => (minutePeriod ? /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/ : /^\d{4}-\d{2}-\d{2}$/).test(bar.date) && (!i || bars[i - 1].date < bar.date) &&
            [bar.open, bar.high, bar.low, bar.close, bar.volume_raw].every(Number.isFinite) && bar.low > 0 && bar.volume_raw >= 0 &&
            bar.high >= Math.max(bar.open, bar.close) && bar.low <= Math.min(bar.open, bar.close))) {
        throw new Error(`${minutePeriod ? "分钟" : "日线"}数据不完整，请重新同步后重试。`);
      }
      this.history = history;
      this.points = history.bars.map((bar, index, bars) => {
        const point = { ...bar, volume: bar.volume_raw };
        for (const period of [5, 10, 20]) {
          point[`ma${period}`] = index < period - 1 ? null : bars.slice(index - period + 1, index + 1).reduce((sum, item) => sum + item.close, 0) / period;
        }
        return point;
      });
      const basisLabel = { qfq: "前复权", hfq: "后复权", none: "不复权" }[history.adjust];
      const source = { tencent: "腾讯", sina: "新浪", baostock: "BaoStock", tushare: "Tushare", eastmoney: "东方财富" }[history.source] || history.source;
      const volumeUnit = ["baostock", "sina"].includes(history.source) || (history.source === "tencent" && code.startsWith("sh68")) ? "股" : "手";
      const last = history.bars.at(-1).date;
      if (minutePeriod) {
        this.byId("chart-meta").textContent = `${code} · 腾讯 · 不复权 · 成交量：数据源原始单位 · ${history.bars[0].date} 至 ${last} · ${history.bars.length} 根 ${minutePeriod} 分钟 K 线`;
        const state = history.sync;
        const status = { ok: "最近一次同步成功", stale: "行情有延迟，显示已保存数据", partial: "行情存在缺口或等待收盘确认", failed: "最近同步失败，显示已保存数据" }[state.status] || "同步状态未知";
        const forming = history.bars.some((bar) => !bar.final) ? " · 含形成中 K 线" : "";
        this.byId("chart-sync-note").textContent = `${status}${forming} · 抓取时间 ${this.minuteTime(history.fetched_at)} · 最近盘后校准 ${state.calibrated_date || "尚未完成"}`;
        this.byId("chart-sync-note").hidden = false;
      } else {
        this.byId("chart-meta").textContent = `${code} · ${source} · ${basisLabel} · 成交量单位：${volumeUnit} · ${history.bars[0].date} 至 ${last} · 截止 ${history.as_of} · ${history.bars.length} 根已保存日线`;
      }
      this.byId("chart-body").hidden = false;
      this.draw();
    } catch (error) {
      if (sequence !== this.sequence || error.name === "AbortError") return;
      this.byId("chart-meta").textContent = `${code} · ${this.periodLabel}暂不可用`;
      this.byId("chart-error-message").textContent = error.message;
      this.byId("chart-error").hidden = false;
    } finally {
      if (sequence === this.sequence) this.byId("chart-loading").hidden = true;
    }
  }
  minuteTime(value) {
    const epoch = Date.parse(value);
    return Number.isFinite(epoch) ? new Date(epoch + 8 * 3600000).toISOString().slice(0, 16).replace("T", " ") : "未知";
  }
  svg(tag, attributes, text) {
    const node = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (const [key, value] of Object.entries(attributes)) node.setAttribute(key, value);
    if (text !== undefined) node.textContent = text;
    return node;
  }
  format(value) { return value === null ? "—" : value.toLocaleString("zh-CN", { minimumFractionDigits: 2, maximumFractionDigits: 2 }); }
  draw() {
    if (!this.history) return;
    const count = Number(this.byId("chart-range").value);
    this.visible = count ? this.points.slice(-count) : this.points;
    const points = this.visible;
    const low = Math.min(...points.map((bar) => Math.min(bar.low, ...[bar.ma5, bar.ma10, bar.ma20].filter((value) => value !== null))));
    const high = Math.max(...points.map((bar) => Math.max(bar.high, bar.ma5 || 0, bar.ma10 || 0, bar.ma20 || 0)));
    const pad = Math.max((high - low) * .08, high * .001);
    const min = low - pad, max = high + pad;
    const maxVolume = Math.max(1, ...points.map((bar) => bar.volume));
    const width = Math.max(960, points.length * 8 + 88), left = 72, right = 16, plotWidth = width - left - right, step = plotWidth / points.length;
    const x = (i) => left + (i + .5) * step;
    const y = (price) => 22 + (max - price) / (max - min) * 258;
    const svg = this.svg("svg", { viewBox: `0 0 ${width} 420`, role: "img", "aria-label": `${this.history.name || this.code} ${this.periodLabel}、成交量及 MA5/10/20`, "data-testid": "kline-svg" });
    // Keep long archives readable instead of squeezing thousands of candles into
    // one viewport. The date slider also moves the selected candle into view.
    if (width > 960) svg.style.width = `${width}px`;
    svg.append(this.svg("title", {}, `${points[0].date} 至 ${points.at(-1).date}，共 ${points.length} 根${this.periodLabel}；红涨绿跌`));
    for (let i = 0; i <= 4; i++) {
      const price = min + (max - min) * i / 4;
      svg.append(this.svg("line", { x1: left, x2: width - right, y1: y(price), y2: y(price), class: "chart-grid" }));
      svg.append(this.svg("text", { x: left - 10, y: y(price) + 4, "text-anchor": "end", class: "chart-axis" }, this.format(price)));
    }
    svg.append(this.svg("text", { x: left, y: 307, class: "chart-axis" }, "成交量（源单位）"));
    points.forEach((bar, i) => {
      const color = bar.close >= bar.open ? "chart-rise" : "chart-fall";
      const group = this.svg("g", { class: `${color}${bar.final === false ? " chart-forming" : ""}`, "data-date": bar.date });
      const bodyWidth = Math.max(.6, Math.min(12, step * .65));
      group.append(this.svg("line", { x1: x(i), x2: x(i), y1: y(bar.high), y2: y(bar.low), class: "chart-wick" }));
      group.append(this.svg("rect", { x: x(i) - bodyWidth / 2, y: Math.min(y(bar.open), y(bar.close)), width: bodyWidth, height: Math.max(1, Math.abs(y(bar.open) - y(bar.close))), class: "chart-candle", "data-testid": "kline-candle" }));
      const volumeHeight = bar.volume / maxVolume * 64;
      group.append(this.svg("rect", { x: x(i) - bodyWidth / 2, y: 380 - volumeHeight, width: bodyWidth, height: volumeHeight, class: "chart-volume", "data-testid": "kline-volume" }));
      svg.append(group);
    });
    for (const period of [5, 10, 20]) {
      const path = points.map((bar, i) => bar[`ma${period}`] === null ? "" : `${i === 0 || points[i - 1][`ma${period}`] === null ? "M" : "L"}${x(i)},${y(bar[`ma${period}`])}`).join(" ");
      if (path) svg.append(this.svg("path", { d: path, class: `chart-ma chart-ma${period}`, "data-testid": `kline-ma${period}` }));
    }
    this.cursor = this.svg("line", { x1: x(points.length - 1), x2: x(points.length - 1), y1: 22, y2: 380, class: "chart-cursor" });
    svg.append(this.cursor);
    points.forEach((bar, i) => {
      const hit = this.svg("rect", { x: left + i * step, y: 22, width: step, height: 358, class: "chart-hit" });
      hit.append(this.svg("title", {}, `${bar.date} 开 ${this.format(bar.open)} 高 ${this.format(bar.high)} 低 ${this.format(bar.low)} 收 ${this.format(bar.close)}`));
      hit.addEventListener("pointerenter", () => this.inspect(i));
      hit.addEventListener("click", () => this.inspect(i));
      svg.append(hit);
    });
    for (const i of [...new Set([0, Math.floor((points.length - 1) / 2), points.length - 1])]) {
      svg.append(this.svg("text", { x: x(i), y: 407, "text-anchor": i === 0 ? "start" : i === points.length - 1 ? "end" : "middle", class: "chart-axis" }, points[i].date));
    }
    this.byId("chart-plot").replaceChildren(svg);
    const slider = this.byId("chart-day");
    slider.max = points.length - 1;
    this.cursorX = x;
    this.inspect(points.length - 1, true);
  }
  inspect(index, reveal = false) {
    const bar = this.visible?.[index];
    if (!bar) return;
    this.byId("chart-day").value = index;
    this.byId("chart-day").setAttribute("aria-valuetext", `${bar.date}，收盘 ${this.format(bar.close)}`);
    this.cursor.setAttribute("x1", this.cursorX(index));
    this.cursor.setAttribute("x2", this.cursorX(index));
    if (reveal) {
      const plot = this.byId("chart-plot");
      const svg = this.cursor.ownerSVGElement;
      const position = this.cursorX(index) * svg.getBoundingClientRect().width / svg.viewBox.baseVal.width;
      if (position < plot.scrollLeft + 72 || position > plot.scrollLeft + plot.clientWidth - 24) {
        plot.scrollLeft = Math.max(0, position - plot.clientWidth / 2);
      }
    }
    this.byId("chart-detail").textContent = `${bar.date}${bar.final === false ? "（形成中）" : ""}　开 ${this.format(bar.open)}　高 ${this.format(bar.high)}　低 ${this.format(bar.low)}　收 ${this.format(bar.close)}　量 ${this.format(bar.volume)}`;
    for (const period of [5, 10, 20]) this.byId(`chart-ma${period}`).textContent = `MA${period} ${this.format(bar[`ma${period}`])}`;
  }
};
