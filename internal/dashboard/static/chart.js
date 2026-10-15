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
    this.byId("chart-retry").addEventListener("click", () => this.select(this.code, false, true));
    this.byId("chart-day").addEventListener("input", (event) => this.inspect(Number(event.target.value)));
  }
  update(stocks) {
    this.stocks = stocks;
    const select = this.byId("chart-stock");
    select.replaceChildren();
    for (const stock of stocks) {
      const option = document.createElement("option");
      option.value = stock.code;
      option.textContent = `${stock.name || stock.code} · ${stock.code}${stock.bar ? "" : " · 暂无数据"}`;
      select.append(option);
    }
    this.select(stocks.some((stock) => stock.code === this.code) ? this.code : (stocks.find((stock) => stock.bar) || stocks[0])?.code || "");
  }
  async select(code, scroll = false, force = false) {
    const stock = this.stocks.find((item) => item.code === code);
    if (!stock) return;
    this.controller?.abort();
    const sequence = ++this.sequence;
    this.controller = new AbortController();
    this.code = code;
    this.history = null;
    this.byId("chart-stock").value = code;
    this.byId("chart-title").textContent = `${stock.name || code} · 日 K`;
    this.byId("chart-meta").textContent = "正在读取已保存的日线…";
    this.byId("chart-plot").replaceChildren();
    this.byId("chart-body").hidden = true;
    this.byId("chart-error").hidden = true;
    this.byId("chart-loading").hidden = false;
    if (scroll) this.byId("stock-chart").scrollIntoView({ block: "start", behavior: "smooth" });
    if (!stock.bar && !force) {
      this.byId("chart-loading").hidden = true;
      this.byId("chart-meta").textContent = `${code} · 暂无可展示的日线`;
      this.byId("chart-error-message").textContent = "该股票本次暂无通过校验的日线，请查看股池中的数据缺失原因，等待下次同步。";
      this.byId("chart-error").hidden = false;
      return;
    }
    try {
      const history = await this.requestJSON(`/api/history?code=${encodeURIComponent(code)}`, this.controller.signal);
      if (sequence !== this.sequence) return;
      if (history?.schema_version !== 1 || history.code !== code || !Array.isArray(history.bars) || !history.bars.length ||
          !history.bars.every((bar, i, bars) => /^\d{4}-\d{2}-\d{2}$/.test(bar.date) && (!i || bars[i - 1].date < bar.date) &&
            [bar.open, bar.high, bar.low, bar.close, bar.volume_raw].every(Number.isFinite) && bar.low > 0 && bar.volume_raw >= 0 &&
            bar.high >= Math.max(bar.open, bar.close) && bar.low <= Math.min(bar.open, bar.close))) {
        throw new Error("日线数据不完整，请重新同步后重试。");
      }
      this.history = history;
      this.points = history.bars.map((bar, index, bars) => {
        const point = { ...bar, volume: bar.volume_raw };
        for (const period of [5, 10, 20]) {
          point[`ma${period}`] = index < period - 1 ? null : bars.slice(index - period + 1, index + 1).reduce((sum, item) => sum + item.close, 0) / period;
        }
        return point;
      });
      const adjust = { qfq: "前复权", hfq: "后复权", none: "不复权" }[history.adjust] || history.adjust;
      const last = history.bars.at(-1).date;
      this.byId("chart-meta").textContent = `${code} · ${adjust} · 末根日线 ${last} · 截止 ${history.as_of} · ${history.bars.length} 根已保存日线`;
      this.byId("chart-body").hidden = false;
      this.draw();
    } catch (error) {
      if (sequence !== this.sequence || error.name === "AbortError") return;
      this.byId("chart-meta").textContent = `${code} · 日线暂不可用`;
      this.byId("chart-error-message").textContent = error.message;
      this.byId("chart-error").hidden = false;
    } finally {
      if (sequence === this.sequence) this.byId("chart-loading").hidden = true;
    }
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
    const width = 960, left = 72, right = 16, plotWidth = width - left - right, step = plotWidth / points.length;
    const x = (i) => left + (i + .5) * step;
    const y = (price) => 22 + (max - price) / (max - min) * 258;
    const svg = this.svg("svg", { viewBox: "0 0 960 420", role: "img", "aria-label": `${this.history.name || this.code} 日K、成交量及 MA5/10/20`, "data-testid": "kline-svg" });
    svg.append(this.svg("title", {}, `${points[0].date} 至 ${points.at(-1).date}，共 ${points.length} 根日 K；红涨绿跌`));
    for (let i = 0; i <= 4; i++) {
      const price = min + (max - min) * i / 4;
      svg.append(this.svg("line", { x1: left, x2: width - right, y1: y(price), y2: y(price), class: "chart-grid" }));
      svg.append(this.svg("text", { x: left - 10, y: y(price) + 4, "text-anchor": "end", class: "chart-axis" }, this.format(price)));
    }
    svg.append(this.svg("text", { x: left, y: 307, class: "chart-axis" }, "成交量（源单位）"));
    points.forEach((bar, i) => {
      const color = bar.close >= bar.open ? "chart-rise" : "chart-fall";
      const group = this.svg("g", { class: color, "data-date": bar.date });
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
    this.inspect(points.length - 1);
  }
  inspect(index) {
    const bar = this.visible?.[index];
    if (!bar) return;
    this.byId("chart-day").value = index;
    this.byId("chart-day").setAttribute("aria-valuetext", `${bar.date}，收盘 ${this.format(bar.close)}`);
    this.cursor.setAttribute("x1", this.cursorX(index));
    this.cursor.setAttribute("x2", this.cursorX(index));
    this.byId("chart-detail").textContent = `${bar.date}　开 ${this.format(bar.open)}　高 ${this.format(bar.high)}　低 ${this.format(bar.low)}　收 ${this.format(bar.close)}　量 ${this.format(bar.volume)}`;
    for (const period of [5, 10, 20]) this.byId(`chart-ma${period}`).textContent = `MA${period} ${this.format(bar[`ma${period}`])}`;
  }
};
