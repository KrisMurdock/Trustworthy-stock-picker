"use strict";
(() => {
  const $ = id => document.getElementById(id);
  const state = { records: [], current: null, parent: null, baseline: null, id: "", request: 0, page: 0, evidenceRequest: 0 };
  const verdicts = { risk_rejected: "风险不达标", risk_unverified: "风险待核实", tradeoff: "收益／周期有取舍", historical: "旧口径结论", baseline: "基线", promising: "开发候选", no_improvement: "未改善", insufficient: "证据不足", not_comparable: "口径不同", pending: "尚无结论" };
  const statuses = { running: "运行中", completed: "运行成功", failed: "运行失败", canceled: "已取消", interrupted: "运行中断" };
  const outcomes = { target: "达到目标", stop: "止损", stop_gap: "跳空止损", timeout: "到期退出", unresolved: "无法判定", corporate_action: "除权窗口未决", entry_skipped: "跳过入场", missing_entry_session: "入场日缺失", missing_exit_session: "退出日缺失", cash_insufficient: "预算不足", risk_budget_insufficient: "交易成本已占满亏损额度" };
  const fmt = (v, digits = 2) => Number.isFinite(v) ? v.toLocaleString("zh-CN", { minimumFractionDigits: digits, maximumFractionDigits: digits }) : "—";
  const timestamp = v => v ? new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", dateStyle: "short", timeStyle: "short", hour12: false }).format(new Date(v)) : "—";
  const node = (tag, text, cls) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (cls) n.className = cls; return n; };
  const show = (id, visible) => { $(id).hidden = !visible; };
  const text = (id, value) => { $(id).textContent = value; };
  const list = value => Array.isArray(value) ? value : [];
  const short = id => (id || "").slice(0, 8);
  const stats = r => r?.current_review?.stats || r?.result?.development?.research?.development?.top_ranked;
  const resolved = outcome => ["target", "stop", "stop_gap", "timeout"].includes(outcome.status);
  const touch = trade => resolved(trade.outcome) && trade.outcome.high_touch?.schema_version === 1 && trade.outcome.high_touch.observed_days > 0 ? trade.outcome.high_touch : null;
  const touchDays = h => h ? `${h.first_2_5_day || "未触达"}${h.first_2_5_order_unknown ? "（顺序不明）" : ""} / ${h.first_3_day || "未触达"}${h.first_3_order_unknown ? "（顺序不明）" : ""}` : "未记录 / 待核实";
  const breaches = trade => trade.selected && resolved(trade.outcome) && trade.outcome.net_pct <= -3 + 1e-9;
  const currentVerdict = r => {
    if (r.status !== "completed") return r.verdict;
    const risk = r.current_review?.risk;
    if (risk?.status === "breached") return "risk_rejected";
    if (risk?.status !== "observed_pass") return "risk_unverified";
    return r.engine !== "development_high_touch_v3" ? "historical" : r.verdict;
  };
  const comparable = (a, b) => a?.status === "completed" && b?.status === "completed" && a.data.id === b.data.id && a.engine === b.engine && a.code_version === b.code_version;
  function button(label, action, cls = "evidence-link") { const b = node("button", label, cls); b.type = "button"; b.addEventListener("click", action); return b; }
  function badge(label, good = false) { return node("span", label, `tag ${good ? "tag-eligible" : "tag-neutral"}`); }
  async function request(url, signal) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (signal?.aborted) abort();
    signal?.addEventListener("abort", abort, { once: true });
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; abort(); }, 10000);
    try {
      const response = await fetch(url, { signal: controller.signal, cache: "no-store", headers: { Accept: "application/json" } });
      if (!response.ok) {
        if ([401, 403].includes(response.status)) throw new Error("当前访问未获授权，请检查网关访问权限。");
        if (response.status === 404) throw new Error("未找到该实验或对应证据，请刷新列表后重试。");
        if (response.status === 504) throw new Error("读取实验档案超时，请稍后重试。");
        throw new Error("实验档案暂时不可用，请稍后重试。");
      }
      return await response.json();
    } catch (error) {
      if (timedOut) throw new Error("请求超时（10 秒），请检查服务后重试。");
      if (error.name === "AbortError") throw error;
      if (error instanceof TypeError) throw new Error("无法连接服务，请检查网络后重试。");
      if (error instanceof SyntaxError) throw new Error("实验记录无法读取，请检查档案完整性。");
      throw error;
    } finally { clearTimeout(timer); signal?.removeEventListener("abort", abort); }
  }
  function renderList() {
    const query = $("experiment-search").value.trim().toLowerCase(), filter = $("experiment-filter").value;
    const visible = state.records.filter(r => (!query || `${r.id} ${r.spec.title} ${r.spec.direction} ${r.spec.hypothesis}`.toLowerCase().includes(query)) && (!filter || filter === currentVerdict(r) || filter === r.status || (filter === "failed" && ["canceled", "interrupted"].includes(r.status))));
    $("experiment-list").replaceChildren(...visible.map(r => {
      const b = button("", () => select(r.id, true), "experiment-item");
      b.dataset.id = r.id; b.setAttribute("aria-current", String(r.id === state.id));
      b.append(node("span", `#${short(r.id)} · ${timestamp(r.created_at)}`, "item-meta"), node("strong", r.spec.title), badge(statuses[r.status] || r.status), document.createTextNode(" "), badge(verdicts[currentVerdict(r)] || currentVerdict(r), currentVerdict(r) === "promising"));
      if (r.parent_id) b.append(node("span", `源自 #${short(r.parent_id)}`, "item-meta"));
      return b;
    }));
    text("experiment-count", state.records.length); show("list-empty", !visible.length);
  }
  async function refresh() {
    state.listController?.abort(); state.listController = new AbortController();
    show("lab-loading", true); show("lab-error", false); $("refresh-experiments").disabled = true;
    try {
      const data = await request("/api/experiments", state.listController.signal);
      if (data.schema_version !== 1 || !Array.isArray(data.experiments)) throw new Error("实验档案版本不受支持。");
      state.records = data.experiments; show("lab-empty", !state.records.length); show("lab-workspace", !!state.records.length); renderList();
      const id = new URL(location.href).searchParams.get("id") || state.id || state.records[0]?.id;
      if (id) { show("lab-workspace", true); await select(id); }
    } catch (e) { if (e.name !== "AbortError") { show("lab-error", true); text("lab-error-text", e.message); } }
    finally { show("lab-loading", false); $("refresh-experiments").disabled = false; }
  }
  async function select(id, push = false) {
    state.controller?.abort(); state.evidenceController?.abort(); ++state.evidenceRequest;
    state.controller = new AbortController(); const signal = state.controller.signal, sequence = ++state.request;
    state.id = id; state.current = null; state.page = 0;
    show("experiment-detail", false); show("detail-error", false); show("detail-loading", true); show("evidence-panel", false); renderList();
    const url = new URL(location.href); url.searchParams.set("id", id); history[push ? "pushState" : "replaceState"]({}, "", url);
    try {
      if (!/^[a-f0-9]{32}$/.test(id)) throw new Error("实验编号无效，请从列表重新选择。");
      const record = await request(`/api/experiments?id=${id}`, signal);
      if (record.schema_version !== 1 || record.id !== id) throw new Error("实验记录不完整。");
      const references = [...new Set([record.parent_id, record.baseline_id].filter(x => x && x !== id))];
      const related = await Promise.all(references.map(x => request(`/api/experiments?id=${x}`, signal)));
      if (sequence !== state.request) return;
      state.current = record; state.parent = related.find(r => r.id === record.parent_id); state.baseline = record.baseline_id === id ? record : related.find(r => r.id === record.baseline_id);
      renderDetail(); show("experiment-detail", true);
    } catch (e) { if (sequence === state.request && e.name !== "AbortError") { text("detail-error-text", e.message); show("detail-error", true); } }
    finally { if (sequence === state.request) show("detail-loading", false); }
  }
  function renderDetail() {
    const r = state.current, result = r.result, dev = result?.development, evaluation = dev?.research?.development;
    text("run-id", `EXPERIMENT / ${short(r.id)}`); text("run-title", r.spec.title);
    text("run-time", `创建于 ${timestamp(r.created_at)}${r.finished_at ? ` · 结束于 ${timestamp(r.finished_at)}` : " · 等待运行结果"}`);
    $("run-tags").replaceChildren(badge(statuses[r.status] || r.status), badge(verdicts[currentVerdict(r)] || currentVerdict(r), currentVerdict(r) === "promising"));
    const lineage = $("run-lineage"); lineage.replaceChildren(node("span", "迭代路径"));
    if (r.baseline_id !== r.id) lineage.append(button(`基线 #${short(r.baseline_id)}`, () => select(r.baseline_id, true)), node("span", "→"));
    if (r.parent_id && r.parent_id !== r.baseline_id) lineage.append(button(`父实验 #${short(r.parent_id)}`, () => select(r.parent_id, true)), node("span", "→"));
    lineage.append(node("strong", `本轮 #${short(r.id)}`));
    text("run-coverage", `${r.data.included} / ${r.data.pool_size} 支`);
    text("run-dates", evaluation?.from ? `${evaluation.from} → ${evaluation.through}` : "尚无有效验证区间");
    text("run-holdout", dev?.holdout_from ? `${dev.holdout_from} → ${dev.holdout_through}` : "待划分");
    for (const field of ["direction", "hypothesis", "change", "criteria"]) text(`run-${field}`, r.spec[field]);
    const p = r.data.parameters;
    $("run-data").replaceChildren(...[
      `日线公共起始边界 ${r.data.from}，截止 ${r.data.as_of}；日历 ${r.data.calendar.from} 至 ${r.data.calendar.through}。实际信号区间还需扣除指标预热、训练与持仓边界。`,
      `每日选择前 ${r.data.top_n} 名；模拟止盈目标 ${fmt(p.target_net_pct)}%，配置止损目标 ${fmt(p.stop_net_pct)}%，买入后最多 ${p.max_holding_days} 个可卖出交易日。当前模型在 3% 硬限额内预留费用、滑点与价格取整；触发价不能保证实际成交价。`,
      `单股参考预算 ${fmt(p.trade_cash)} 元；双边佣金 ${fmt(p.commission_rate * 100, 4)}%，最低佣金 ${fmt(p.min_commission)} 元；过户费 ${fmt(p.transfer_rate * 100, 4)}%，卖出印花税 ${fmt(p.sell_stamp_rate * 100, 4)}%，滑点 ${fmt(p.slippage_rate * 100, 4)}%。`,
      `开发期使用前复权特征与不复权成交模拟；预留 ${p.holdout_days} 个末段信号日期，跨边界持仓样本剔除。最终区间未参与本轮评价。`,
      `引擎 ${r.engine} · 代码版本 ${r.code_version}`, `数据快照 ${r.data.id}`
    ].map(x => node("p", x)));
    const reasons = { missing_saved_pair: "缺少已保存的复权/原始行情", invalid_saved_pair: "行情元数据未通过校验", unaligned_saved_pair: "复权与原始日期不一致" };
    $("run-excluded").replaceChildren(...list(r.data.excluded).map(x => node("li", `${x.name || x.code} ${x.code}：${reasons[x.reason] || x.reason}`)));
    show("run-failure", !!r.error || r.status === "running"); text("run-failure", r.error || "实验正在运行，完成后刷新记录可查看结果。异常退出的任务会在下次本地任务启动时标为中断。");
    show("run-results", !!result && r.status === "completed"); show("risk-review", !!result && r.status === "completed");
    if (!result) return;
    renderComparison(); renderGroups(); renderTrades(); renderRisk();
    $("run-findings").replaceChildren(...list(result.findings).map(f => {
      const box = node("div", undefined, "finding"); box.dataset.kind = f.kind;
      box.append(badge({ measured: "计算事实", limitation: "适用局限", hypothesis: "待验证推断" }[f.kind] || f.kind), node("p", f.text));
      for (const id of list(f.evidence)) { const trade = dev.trades.find(t => t.id === id); if (trade) box.append(button(`${trade.date} · ${trade.code}`, () => evidence(trade))); }
      return box;
    }));
    $("run-next").replaceChildren(...list(result.next).map(x => node("li", x)));
    const children = state.records.filter(x => x.parent_id === r.id);
    $("run-children").replaceChildren(node("span", children.length ? "已衍生的实验" : "尚未记录后续实验"), ...children.map(x => button(`${x.spec.title} #${short(x.id)}`, () => select(x.id, true))));
  }
  function renderRisk() {
    const r = state.current, review = r.current_review, risk = review?.risk;
    const labels = { breached: "风险不达标：不得作为合格候选", unverified: "风险待核实：当前证据不能确认达标", observed_pass: "已完成样本未超限：不代表实盘保证" };
    text("risk-title", labels[risk?.status] || labels.unverified);
    const count = risk ? `${risk.exact_count ? "" : "至少 "}${risk.breaches}` : "未知";
    text("risk-summary", `硬条件：单笔扣费后净亏损严格低于买入总成本的 3%。超限 ${count} 笔；未决 ${risk?.unresolved ?? "未知"} 笔。等于 3% 也不通过；未决交易不按零亏损处理。`);
    text("risk-original", `档案原结论：${verdicts[r.verdict] || r.verdict}。上方为按当前风险要求重新核对的结果，原实验记录保留。`);
    $("show-breaches").disabled = !list(r.result?.development?.trades).some(breaches);
  }
  function renderComparison() {
    const r = state.current, same = comparable(r, state.parent), baseSame = comparable(r, state.baseline);
    text("comparison-note", r.parent_id ? same ? "数据快照、代码版本与评价口径一致，可以逐项比较。先检查 3% 亏损硬条件，再比较最高价触达率与首次触达天数，实际退出收益另列。" : "与父实验的代码或数据口径不同，不展示提升差值；请在相同口径下重跑基线。" : "本轮建立基线，后续实验将在此基础上比较。最终保留区间尚未评价。");
    const metrics = [["完成交易数", "resolved", 1, "", 0], ["入场样本数", "samples", 1, "", 0], ["独立信号日期", "distinct_signal_dates", 1, "", 0], ["平均单笔净收益", "mean_resolved_net_pct", 1, "%", 3], ["盈利比例（已完成）", "resolved_profit_rate", 100, "%", 2], ["模拟止盈成交率（含未决）", "target_rate", 100, "%", 2], ["最差单笔净收益", "worst_resolved_net_pct", 1, "%", 3], ["最差持仓不利变动", "worst_max_adverse_net_pct", 1, "%", 3], ["止损次数", "stops", 1, "", 0], ["未决交易", "unresolved", 1, "", 0], ["跳过入场", "skipped_entries", 1, "", 0], ["净亏损达到或超过 3%", "net_loss_limit_breaches", 1, "", 0], ["实际退出净盈利 ≥2.5%（含未决）", "net_profit_2_5_rate", 100, "%", 2], ["实际退出净盈利 ≥3%（含未决）", "net_profit_3_rate", 100, "%", 2], ["平均持仓（买入后交易日）", "mean_resolved_holding_days", 1, " 天", 2], ["已确认最高价触达 ≥2.5%（含未决）", "high_touch_2_5_rate", 100, "%", 2], ["已确认最高价触达 ≥3%（含未决）", "high_touch_3_rate", 100, "%", 2], ["首次触达 2.5% 平均天数", "mean_first_high_touch_2_5_days", 1, " 天", 2], ["首次触达 3% 平均天数", "mean_first_high_touch_3_days", 1, " 天", 2], ["触达证据缺失 / 未决", "high_touch_unknown", 1, "", 0], ["退出日触达先后顺序不明", "high_touch_order_unknown", 1, "", 0]];
    $("comparison-body").replaceChildren(...metrics.map(([label, key, scale, unit, digits]) => {
      const row = node("tr"); row.append(node("td", label));
      const value = x => {
        const s = stats(x);
        if (!s || (unit && !s.resolved)) return undefined;
        if ((key === "high_touch_2_5_rate" || key === "high_touch_3_rate") && !s.high_touch_samples) return undefined;
        if (key === "mean_first_high_touch_2_5_days" && !s.high_touch_2_5_count) return undefined;
        if (key === "mean_first_high_touch_3_days" && !s.high_touch_3_count) return undefined;
        return s[key] * scale;
      };
      for (const record of [baseSame ? state.baseline : null, same ? state.parent : null, r]) row.append(node("td", Number.isFinite(value(record)) ? fmt(value(record), digits) + unit : "—"));
      const delta = same ? value(r) - value(state.parent) : NaN;
      row.append(node("td", Number.isFinite(delta) ? `${delta > 0 ? "+" : ""}${fmt(delta, digits)}${unit === "%" ? " 个百分点" : unit}` : "—", "muted"));
      return row;
    }));
  }
  function renderGroups() {
    const groups = list(state.current?.result?.groups).filter(g => g.kind === $("group-kind").value);
    $("group-body").replaceChildren(...groups.map(g => { const s = g.stats, tr = node("tr"); for (const value of [g.name || "未分类", `${s.resolved} / ${s.unresolved}`, s.distinct_signal_dates, s.resolved ? fmt(s.mean_resolved_net_pct, 3) + "%" : "—", s.resolved ? fmt(s.resolved_profit_rate * 100) + "%" : "—", s.resolved ? fmt(s.worst_resolved_net_pct, 3) + "%" : "—"]) tr.append(node("td", String(value))); return tr; }));
    if (!groups.length) emptyRow("group-body", 6, "本轮没有入选信号，暂无分组结果。");
  }
  function emptyRow(id, columns, message) { const row = node("tr"), cell = node("td", message, "muted"); cell.colSpan = columns; row.append(cell); $(id).replaceChildren(row); }
  function renderTrades() {
    const filter = $("trade-filter").value;
    const trades = list(state.current?.result?.development?.trades).filter(t => filter === "all" || (filter === "selected" && t.selected) || (filter === "rejected" && !t.selected) || (filter === "loss" && t.selected && resolved(t.outcome) && t.outcome.net_pct < 0) || (filter === "breaches" && breaches(t)) || (filter === "touch25" && t.selected && touch(t)?.first_2_5_day > 0 && !touch(t).first_2_5_order_unknown) || (filter === "touch30" && t.selected && touch(t)?.first_3_day > 0 && !touch(t).first_3_order_unknown));
    const pages = Math.max(1, Math.ceil(trades.length / 20)); state.page = Math.min(state.page, pages - 1);
    $("trade-body").replaceChildren(...trades.slice(state.page * 20, state.page * 20 + 20).map(t => {
      const tr = node("tr"); tr.dataset.trade = t.id;
      tr.append(node("td", `${t.date} · ${t.code}`), node("td", t.selected ? outcomes[t.outcome.status] || t.outcome.status : "被本轮过滤"), node("td", resolved(t.outcome) ? `${fmt(t.outcome.net_pct, 3)}%` : "—"), node("td", touch(t) ? `${fmt(touch(t).max_net_pct, 3)}%` : "未记录 / 待核实"), node("td", touchDays(touch(t))), node("td", `${fmt(t.features.upper_wick_ratio * 100)}%`));
      const cell = node("td"); cell.append(button("查看 K 线", () => evidence(t))); tr.append(cell); return tr;
    }));
    if (!trades.length) emptyRow("trade-body", 7, "当前筛选条件下没有交易记录。");
    text("trade-page", `${trades.length} 条 · ${state.page + 1} / ${pages} 页`); $("trade-prev").disabled = state.page === 0; $("trade-next").disabled = state.page === pages - 1;
  }
  function svg(tag, attrs, value) { const n = document.createElementNS("http://www.w3.org/2000/svg", tag); for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v); if (value !== undefined) n.textContent = value; return n; }
  async function evidence(trade) {
    state.evidenceController?.abort(); state.evidenceController = new AbortController(); const sequence = ++state.evidenceRequest, id = state.id;
    show("evidence-panel", true); $("evidence-chart").replaceChildren(); text("evidence-detail", "");
    text("evidence-title", `${trade.code} · 信号 ${trade.date} · ${trade.selected ? "本轮入选" : "本轮过滤，显示原候选模拟结果"}`);
    text("evidence-status", "正在读取固定快照中的原始日线…"); $("evidence-panel").scrollIntoView({ block: "center", behavior: "smooth" });
    try {
      const data = await request(`/api/experiment-history?id=${id}&code=${trade.code}&date=${trade.date}`, state.evidenceController.signal);
      if (sequence !== state.evidenceRequest || id !== state.id) return;
      if (data.code !== trade.code || !Array.isArray(data.bars) || !data.bars.length) throw new Error("历史证据不完整。");
      const bars = data.bars, min = Math.min(...bars.map(b => b.low)), max = Math.max(...bars.map(b => b.high)), span = Math.max(max - min, .01), width = 850, step = 770 / bars.length;
      const x = i => 65 + (i + .5) * step, y = price => 24 + (max - price) / span * 210;
      const plot = svg("svg", { viewBox: `0 0 ${width} 285`, role: "img", "aria-label": `${trade.code} 实验固定快照日K`, "data-testid": "experiment-kline" });
      plot.append(svg("title", {}, `信号日 ${trade.date}，快照 ${short(state.current.data.id)}`));
      for (let i = 0; i <= 3; i++) { const price = min + span * i / 3; plot.append(svg("line", { x1: 60, x2: 840, y1: y(price), y2: y(price), stroke: "#e3e9e7" }), svg("text", { x: 52, y: y(price) + 4, "text-anchor": "end", fill: "#63747a", stroke: "none", "font-size": 11 }, fmt(price))); }
      bars.forEach((b, i) => {
        const color = b.close >= b.open ? "#bc3d42" : "#208363";
        if (b.date === trade.date) plot.append(svg("rect", { x: x(i) - step / 2, y: 12, width: step, height: 240, fill: "#164d5915", stroke: "none" }), svg("text", { x: x(i), y: 275, "text-anchor": "middle", fill: "#164d59", stroke: "none", "font-size": 11 }, "信号"));
        const g = svg("g", {}); g.append(svg("line", { x1: x(i), x2: x(i), y1: y(b.high), y2: y(b.low), stroke: color }), svg("rect", { x: x(i) - step * .3, y: Math.min(y(b.open), y(b.close)), width: step * .6, height: Math.max(1, Math.abs(y(b.open) - y(b.close))), fill: color, stroke: "none" }), svg("title", {}, `${b.date} 开 ${b.open} 高 ${b.high} 低 ${b.low} 收 ${b.close}`)); plot.append(g);
      });
      for (const i of [0, bars.length - 1]) plot.append(svg("text", { x: x(i), y: 258, "text-anchor": i === 0 ? "start" : "end", fill: "#63747a", stroke: "none", "font-size": 11 }, bars[i].date));
      $("evidence-chart").replaceChildren(plot);
      text("evidence-status", `不复权 · ${data.source} · 红涨绿跌 · 阴影为信号日`);
      const o = trade.outcome, h = touch(trade);
      text("evidence-detail", `模拟入场 ${o.entry_date || "未入场"}，退出 ${o.exit_date || "未确定"}；${outcomes[o.status] || o.status}。${resolved(o) ? `净收益 ${fmt(o.net_pct, 3)}%，退出价 ${fmt(o.exit_price)}。` : "结果未决，不计入已完成交易收益。"}最高价净收益 ${h ? fmt(h.max_net_pct, 3) + "%" : "未记录 / 待核实"}；首次触达 2.5% / 3%：${touchDays(h)}（买入后交易日）。价格触达不要求收盘达标，也不等于实际成交。信号上影线 ${fmt(trade.features.upper_wick_ratio * 100)}%，ATR ${fmt(trade.features.atr14_pct)}%。`);
    } catch (e) { if (sequence === state.evidenceRequest && e.name !== "AbortError") { text("evidence-status", e.message); $("evidence-chart").replaceChildren(button("重试证据", () => evidence(trade))); } }
  }
  $("show-breaches").addEventListener("click", () => { $("trade-filter").value = "breaches"; state.page = 0; renderTrades(); $("trade-section").scrollIntoView({ block: "start" }); });
  $("refresh-experiments").addEventListener("click", refresh); $("lab-retry").addEventListener("click", refresh); $("detail-retry").addEventListener("click", () => select(state.id));
  $("experiment-search").addEventListener("input", renderList); $("experiment-filter").addEventListener("change", renderList);
  $("group-kind").addEventListener("change", renderGroups); $("trade-filter").addEventListener("change", () => { state.page = 0; renderTrades(); });
  $("trade-prev").addEventListener("click", () => { state.page--; renderTrades(); }); $("trade-next").addEventListener("click", () => { state.page++; renderTrades(); });
  window.addEventListener("popstate", () => { const id = new URL(location.href).searchParams.get("id") || state.records[0]?.id; if (id) select(id); });
  refresh();
})();
