"use strict";
(() => {
  const $ = id => document.getElementById(id);
  const state = { records: [], current: null, parent: null, baseline: null, id: "", request: 0, page: 0, evidenceRequest: 0 };
  const verdicts = { research: "研究结果快照", entry_day_rejected: "买入日浮盈不达标", entry_day_unverified: "买入日证据待核实", below_goal: "次日目标未达到", goal_25_only: "仅 2.5% 档观察达标", goal_observed: "两档观察达标 · 待独立验证", risk_rejected: "风险不达标", risk_unverified: "风险待核实", tradeoff: "收益／周期有取舍", historical: "旧口径结论", baseline: "基线", promising: "开发候选", no_improvement: "未改善", insufficient: "证据不足", not_comparable: "口径不同", pending: "尚无结论" };
  const statuses = { running: "运行中", completed: "运行成功", failed: "运行失败", canceled: "已取消", interrupted: "运行中断" };
  const outcomes = { signal_filtered: "信号日指标过滤，未入场", chan_filtered: "缠论结构不通过，未入场", chan_unknown: "缠论结构缺失，未入场", entry_path_unknown: "买入分钟缺失，路径未知", entry_invalidated: "跌破买入区间，放弃", entry_price_outside_zone: "触发后跳出区间，放弃", entry_not_triggered: "未满足到价反弹，放弃", target: "达到目标", stop: "止损", stop_gap: "跳空止损", timeout: "到期退出", unresolved: "无法判定", corporate_action: "除权窗口未决", entry_skipped: "跳过入场", missing_entry_session: "入场日缺失", missing_exit_session: "退出日缺失", cash_insufficient: "预算不足", risk_budget_insufficient: "交易成本已占满亏损额度" };
  const fmt = (v, digits = 2) => Number.isFinite(v) ? v.toLocaleString("zh-CN", { minimumFractionDigits: digits, maximumFractionDigits: digits }) : "—";
  const timestamp = v => v ? new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", dateStyle: "short", timeStyle: "short", hour12: false }).format(new Date(v)) : "—";
  const node = (tag, text, cls) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (cls) n.className = cls; return n; };
  const show = (id, visible) => { $(id).hidden = !visible; };
  const text = (id, value) => { $(id).textContent = value; };
  const list = value => Array.isArray(value) ? value : [];
  const short = id => (id || "").slice(0, 8);
  const stats = r => r?.current_review?.stats || r?.result?.development?.research?.development?.top_ranked;
  const researchSnapshot = r => r?.result_snapshot?.source === "no_stop_speed";
  const resolved = outcome => ["target", "stop", "stop_gap", "timeout"].includes(outcome.status);
  const touch = trade => resolved(trade.outcome) && trade.outcome.high_touch?.schema_version === 1 && trade.outcome.high_touch.observed_days > 0 ? trade.outcome.high_touch : null;
  const touchDays = h => h ? `${h.first_2_5_day || "未触达"}${h.first_2_5_order_unknown ? "（顺序不明）" : ""} / ${h.first_3_day || "未触达"}${h.first_3_order_unknown ? "（顺序不明）" : ""}` : "未记录 / 待核实";
  const breaches = trade => trade.selected && resolved(trade.outcome) && trade.outcome.net_pct <= -3 + 1e-9;
  const currentVerdict = r => {
    if (r.status !== "completed") return r.verdict;
  if (researchSnapshot(r)) return "research";
    const risk = r.current_review?.risk;
    if (risk?.status === "breached") return "risk_rejected";
    if (risk?.status !== "observed_pass") return "risk_unverified";
    return r.engine !== "indicator_selection_v6" ? "historical" : r.verdict;
  };
  const comparable = (a, b) => a?.status === "completed" && b?.status === "completed" && a.data.id === b.data.id && a.engine === b.engine && a.code_version === b.code_version && !!a.spec.next_day_rule === !!b.spec.next_day_rule && (a.spec.entry_mode || "") === (b.spec.entry_mode || "") && (a.spec.signal_range?.from || "") === (b.spec.signal_range?.from || "") && (a.spec.signal_range?.through || "") === (b.spec.signal_range?.through || "");
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
      const summary = defaultSnapshotRow(r);
      if (summary) b.append(node("span", `结果快照 · ${fmt(summary.target_net_pct, 1)}%目标`, "item-meta"), node("span", `达标 ${summary.mean_target_days == null ? "暂无" : fmt(summary.mean_target_days) + "日"} · 最大亏损 ${summary.max_realized_loss_pct == null ? "未记录" : fmt(summary.max_realized_loss_pct) + "%"}`, "item-meta"));
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
    text("run-dates", evaluation?.from ? `${evaluation.from} → ${evaluation.through}` : researchSnapshot(r) ? `${r.spec.signal_range.from} → ${r.spec.signal_range.through}` : "尚无有效验证区间");
    text("run-validation-label", r.spec.signal_range ? "指定区间 · 未声明独立验证集" : "最终保留区间 · 本轮未评估");
    text("run-holdout", r.spec.signal_range ? `${r.spec.signal_range.from} → ${r.spec.signal_range.through}（信号日）` : dev?.holdout_from ? `${dev.holdout_from} → ${dev.holdout_through}` : "待划分");
    for (const field of ["direction", "hypothesis", "change", "criteria"]) text(`run-${field}`, r.spec[field]);
    const p = dev?.research?.parameters || r.data.parameters;
    if (r.spec.entry_mode) $("backtest-policy").value = `conditional-${r.spec.signal_rule || "entry"}`;
    text("backtest-command", ""); text("backtest-command-status", "选择信号日期与规则后生成命令。");
    if (!$("backtest-from").value) $("backtest-from").value = r.spec.signal_range?.from || evaluation?.from || "";
    if (!$("backtest-through").value) $("backtest-through").value = r.spec.signal_range?.through || evaluation?.through || "";
    $("run-data").replaceChildren(...[
      `日线公共起始边界 ${r.data.from}，截止 ${r.data.as_of}；日历 ${r.data.calendar.from} 至 ${r.data.calendar.through}。实际信号区间还需扣除指标预热、训练与持仓边界。`,
      researchSnapshot(r) ? `每日选择前 ${r.data.top_n} 名；不设价格止损，净盈利目标、分批上限与期限见各结果分组。` : `每日选择前 ${r.data.top_n} 名；模拟止盈目标 ${fmt(p.target_net_pct)}%，配置止损目标 ${fmt(p.stop_net_pct)}%，买入后最多 ${p.max_holding_days} 个可卖出交易日。当前模型在 3% 硬限额内预留费用、滑点与价格取整；触发价不能保证实际成交价。`,
      `单股参考预算 ${fmt(p.trade_cash)} 元；双边佣金 ${fmt(p.commission_rate * 100, 4)}%，最低佣金 ${fmt(p.min_commission)} 元；过户费 ${fmt(p.transfer_rate * 100, 4)}%，卖出印花税 ${fmt(p.sell_stamp_rate * 100, 4)}%，滑点 ${fmt(p.slippage_rate * 100, 4)}%。`,
      r.spec.signal_range ? `显式回测信号区间 ${r.spec.signal_range.from} 至 ${r.spec.signal_range.through}；指标预热和退出行情另外读取，没有声明独立留出集。` : r.spec.entry_mode ? `固定规则分钟试运行，最近 3 个分钟交易日留出；短历史不能证明 80% 成功率。` : `开发期使用前复权特征与不复权成交模拟；预留 ${p.holdout_days} 个末段信号日期，跨边界持仓样本剔除。最终区间未参与本轮评价。`,
      r.data.minute_stocks ? `冻结分钟行情 ${r.data.minute_from} 至 ${r.data.minute_through}，${r.data.minute_stocks} 支。` : "此快照没有分钟路径；开盘买入结果不能视作条件低吸的验证。",
      `引擎 ${r.engine} · 代码版本 ${r.code_version}`, `数据快照 ${r.data.id}`
    ].map(x => node("p", x)));
    const reasons = { missing_saved_pair: "缺少已保存的复权/原始行情", invalid_saved_pair: "行情元数据未通过校验", unaligned_saved_pair: "复权与原始日期不一致" };
    $("run-excluded").replaceChildren(...list(r.data.excluded).map(x => node("li", `${x.name || x.code} ${x.code}：${reasons[x.reason] || x.reason}`)));
    show("run-failure", !!r.error || r.status === "running"); text("run-failure", r.error || "实验正在运行，完成后刷新记录可查看结果。异常退出的任务会在下次本地任务启动时标为中断。");
    show("run-results", !!result && r.status === "completed"); show("risk-review", !!result && r.status === "completed");
  show("validation-rules", !researchSnapshot(r));
  renderSnapshot();
    if (!result) return;
    renderComparison(); renderGroups(); renderTrades(); renderRisk(); renderNextDay(); renderEntryDay();
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

  function defaultSnapshotRow(r) {
    if (r?.status !== "completed" || r?.result_snapshot?.schema_version !== 1) return null;
    const rows = list(r.result_snapshot.rows);
    return rows.find(x => x.period.startsWith("后段") && x.label.includes("板块") && x.target_net_pct === 3 && x.rule.startsWith("macd_contraction")) || rows[0];
  }
  function snapshotRule(value) {
    const names = { any: "任一底部信号", macd_contraction: "MACD绿柱收缩", price_reclaim: "收盘≥MA5", momentum_turn: "RSI与MACD柱改善", price_and_momentum: "收盘≥MA5且动量改善", none: "原信号" };
    return value.split(" / ").map(x => names[x] || x).join(" · ");
  }
  function renderSnapshot() {
    const r = state.current, s = r.result_snapshot, available = r.status === "completed" && s?.schema_version === 1 && list(s.rows).length > 0;
    show("snapshot-content", available); show("snapshot-empty", !available); $("snapshot-download").disabled = !available;
    $("snapshot-row").replaceChildren(); $("snapshot-table").replaceChildren(); $("snapshot-notes").replaceChildren();
    text("snapshot-meta", available ? `结果 #${short(s.id)} · ${timestamp(s.finished_at)} · 原始结果固定保存` : "每次运行独立保存，后续规则变化不覆盖旧结果。");
    if (!available) {
      text("snapshot-empty", r.status === "running" ? "实验运行中，尚未生成结果快照。" : r.status !== "completed" ? "本次实验未成功完成，没有可用的结果快照。" : "此档案未记录可用的结果快照，不能按零收益或零亏损处理。");
      return;
    }
    for (const row of s.rows) {
      const option = node("option", `${row.period} · ${row.label} · ${fmt(row.target_net_pct, 1)}% · ${row.max_batches}批 · ${snapshotRule(row.rule)}`); option.value = row.id; $("snapshot-row").append(option);
      const tr = node("tr");
      for (const value of [`${row.period} / ${row.label} / ${snapshotRule(row.rule)} / ${row.max_batches}批`, `${fmt(row.target_net_pct, 1)}%`, row.targets == null ? "未记录" : `${row.targets} / ${row.samples}`, row.mean_target_days == null ? "—" : `${fmt(row.mean_target_days)}日`, row.max_realized_loss_pct == null ? "未记录" : `${fmt(row.max_realized_loss_pct)}%`]) tr.append(node("td", value));
      const cell = node("td"); cell.append(button("查看指标", () => { $("snapshot-row").value = row.id; renderSnapshotMetrics(); })); tr.append(cell); $("snapshot-table").append(tr);
    }
    $("snapshot-row").value = defaultSnapshotRow(r).id;
    $("snapshot-notes").replaceChildren(...list(s.notes).map(x => node("li", x)));
    renderSnapshotMetrics();
  }
  function renderSnapshotMetrics() {
    const s = state.current?.result_snapshot, row = list(s?.rows).find(x => x.id === $("snapshot-row").value);
    if (!row) return;
    const stop = row.stop_loss_pct == null ? "止损未记录" : row.stop_loss_pct === 0 ? "无价格止损" : `止损目标 ${fmt(row.stop_loss_pct)}%`;
    text("snapshot-policy", `${row.from && row.through ? row.from + " → " + row.through : "暂无有效信号区间"} · 净目标 ${fmt(row.target_net_pct, 1)}% · ${stop} · 最多 ${row.max_batches} 批 / ${row.sellable_days} 个可卖出日`);
    text("snapshot-target-days", row.mean_target_days == null ? row.targets === 0 ? "暂无达标交易" : "未记录" : `${fmt(row.mean_target_days)} 日`);
    text("snapshot-max-loss", row.max_realized_loss_pct == null ? "暂无已结束样本" : `${fmt(row.max_realized_loss_pct)}%`);
    text("snapshot-hit-rate", row.target_rate == null ? row.samples === 0 ? "暂无样本" : "未记录" : `${fmt(row.target_rate * 100)}%`);
    text("snapshot-hit-count", row.targets == null ? `样本 ${row.samples} 笔 · 触达证据未记录` : `${row.targets} / ${row.samples} 笔 · 分母含未决`);
    text("snapshot-holding-days", row.mean_resolved_holding_days == null ? "暂无已结束样本" : `${fmt(row.mean_resolved_holding_days)} 日`);
    const values = [["已结束 / 未决 / 跳过入场", `${row.resolved} / ${row.unresolved} / ${row.skipped_entries} 笔`], ["平均已实现净收益", row.mean_resolved_net_pct == null ? "未记录" : `${fmt(row.mean_resolved_net_pct)}%`], ["最大持仓不利变动", row.max_adverse_loss_pct == null ? "未记录" : `${fmt(row.max_adverse_loss_pct)}%`], ["含失败等待分", row.mean_target_days_failure_21 == null ? "本轮未记录" : `${fmt(row.mean_target_days_failure_21)} 日（未达标 / 未决记21日）`]];
    $("snapshot-secondary").replaceChildren(...values.map(([label, value]) => { const box = node("div"); box.append(node("dt", label), node("dd", value)); return box; }));
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
  const entryMark = trade => trade.outcome.status !== "corporate_action" && trade.outcome.entry_day?.schema_version === 1 ? trade.outcome.entry_day : null;
  function renderEntryDay() {
    const r = state.current, s = stats(r), known = s?.samples > 0 && s.entry_day_known > 0;
    text("entry-mode-description", r.spec.entry_mode ? r.spec.change : "本轮按开盘价模拟买入；下列数据可评价买入日浮盈，但不能代表到价反弹低吸。旧记录未保存入场信息时显示未知。");
    text("entry-day-rate", known ? `${fmt(s.entry_day_positive_rate * 100)}% · ${s.entry_day_positive} / ${s.samples}` : "缺少买入日浮盈证据");
    text("entry-day-failures", `不为正 ${s?.entry_day_nonpositive ?? "未知"} 笔；浮盈未知 ${s?.entry_day_unknown ?? "未知"} 笔；入场路径未知 ${s?.unknown_entry_paths ?? "未知"} 条。`);
    text("joint-rates", known ? `${fmt(s.joint_2_5_rate * 100)}% / ${fmt(s.joint_3_rate * 100)}%` : "联合成功率待验证");
    text("joint-counts", known ? `${s.joint_2_5_count} / ${s.samples} 与 ${s.joint_3_count} / ${s.samples}；分母含买入日亏损和未决。` : "不能按当天收盘为正的样本重新缩小分母。");
    text("joint-verdict", verdicts[r.current_review?.joint_verdict] || "证据待核实");
  }
  function renderNextDay() {
    const r = state.current, s = stats(r);
    text("next-day-mode", r.spec.entry_mode ? "本轮到价反弹买入；以下为第三天触达率，买入日正浮盈及联合成功率见上方。" : r.spec.next_day_rule ? "本轮为次日退出实验：首个可卖出日未止损或止盈，按收盘模拟退出。" : "本轮为原持仓期限档案：这里派生次日触达比例，原交易和退出期限保持原样，不能等同于次日退出实验。");
    for (const [id, key, count] of [["25", "next_day_2_5_rate", "next_day_2_5_count"], ["30", "next_day_3_rate", "next_day_3_count"]]) {
      const known = s?.samples > 0 && s.high_touch_samples > 0 && Number.isFinite(s[key]);
      text(`next-day-${id}`, known ? `${fmt(s[key] * 100)}% · ${s[count]} / ${s.samples}` : "暂无可核实样本");
      const gap = known ? 80 - s[key] * 100 : 0;
      text(`next-day-${id}-gap`, known ? gap > 0 ? `距 80% 还差 ${fmt(gap)} 个百分点` : "观察比例已达 80%，继续核对风险、样本量及独立验证" : "无入场或缺少触达证据，不能记为 0% 或 100%。");
    }
    const verdict = r.current_review?.next_day_verdict;
    text("next-day-verdict", verdicts[verdict] || "证据待核实");
    text("next-day-evidence", `已完成 ${s?.resolved ?? "未知"} 笔 / ${s?.distinct_signal_dates ?? "未知"} 个信号日；至少需要 30 笔 / 10 日。超限 ${s?.net_loss_limit_breaches ?? "未知"} 笔，未决 ${s?.unresolved ?? "未知"} 笔。`);
  }
  function renderComparison() {
    const r = state.current, same = comparable(r, state.parent), baseSame = comparable(r, state.baseline);
    text("comparison-note", r.parent_id ? same ? "数据快照、代码版本与评价口径一致，可以逐项比较。先检查 3% 亏损硬条件，再比较最高价触达率与首次触达天数，实际退出收益另列。" : "与父实验的代码或数据口径不同，不展示提升差值；请在相同口径下重跑基线。" : "本轮建立基线，后续实验将在此基础上比较。指定区间请单独核对验证状态。");
    const selection = r.result?.development?.signal_selection;
    text("signal-selection-summary", selection ? `信号日候选 ${selection.candidates} 条 → 规则通过 ${selection.eligible} 条 → 预先入选 ${selection.selected} 条；原 Top N 被过滤 ${selection.filtered_original_top} 条。未触发不按未来行情补选，实际入场数见下方。` : "旧档案没有保存规则筛选覆盖统计，不能补造过滤数量。");
    const metrics = [["买入日收盘扣成本浮盈为正", "entry_day_positive_rate", 100, "%", 2], ["买入日为正且第三天触达 ≥2.5%", "joint_2_5_rate", 100, "%", 2], ["买入日为正且第三天触达 ≥3%", "joint_3_rate", 100, "%", 2], ["买入日浮盈不为正", "entry_day_nonpositive", 1, "", 0], ["买入日浮盈未知", "entry_day_unknown", 1, "", 0], ["入场分钟路径未知", "unknown_entry_paths", 1, "", 0], ["次日确认触达 ≥2.5%（含未决）", "next_day_2_5_rate", 100, "%", 2], ["次日确认触达 ≥3%（含未决）", "next_day_3_rate", 100, "%", 2], ["次日确认触达 2.5% 笔数", "next_day_2_5_count", 1, "", 0], ["次日确认触达 3% 笔数", "next_day_3_count", 1, "", 0], ["完成交易数", "resolved", 1, "", 0], ["入场样本数", "samples", 1, "", 0], ["独立信号日期", "distinct_signal_dates", 1, "", 0], ["平均单笔净收益", "mean_resolved_net_pct", 1, "%", 3], ["盈利比例（已完成）", "resolved_profit_rate", 100, "%", 2], ["模拟止盈成交率（含未决）", "target_rate", 100, "%", 2], ["最差单笔净收益", "worst_resolved_net_pct", 1, "%", 3], ["最差持仓不利变动", "worst_max_adverse_net_pct", 1, "%", 3], ["止损次数", "stops", 1, "", 0], ["未决交易", "unresolved", 1, "", 0], ["跳过入场", "skipped_entries", 1, "", 0], ["净亏损达到或超过 3%", "net_loss_limit_breaches", 1, "", 0], ["实际退出净盈利 ≥2.5%（含未决）", "net_profit_2_5_rate", 100, "%", 2], ["实际退出净盈利 ≥3%（含未决）", "net_profit_3_rate", 100, "%", 2], ["平均持仓（买入后交易日）", "mean_resolved_holding_days", 1, " 天", 2], ["已确认最高价触达 ≥2.5%（含未决）", "high_touch_2_5_rate", 100, "%", 2], ["已确认最高价触达 ≥3%（含未决）", "high_touch_3_rate", 100, "%", 2], ["首次触达 2.5% 平均天数", "mean_first_high_touch_2_5_days", 1, " 天", 2], ["首次触达 3% 平均天数", "mean_first_high_touch_3_days", 1, " 天", 2], ["触达证据缺失 / 未决", "high_touch_unknown", 1, "", 0], ["退出日触达先后顺序不明", "high_touch_order_unknown", 1, "", 0]];
    $("comparison-body").replaceChildren(...metrics.map(([label, key, scale, unit, digits]) => {
      const row = node("tr"); row.append(node("td", label));
      const value = x => {
        const s = stats(x);
        if (!s || (unit && !s.resolved)) return undefined;
        if ((key.startsWith("joint_") || key === "entry_day_positive_rate") && !s.entry_day_known) return undefined;
        if ((key === "high_touch_2_5_rate" || key === "high_touch_3_rate" || key.startsWith("next_day_")) && !s.high_touch_samples) return undefined;
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
  const signalIndicators = f => f?.signal_indicators?.schema_version === 1 ? f.signal_indicators : null;
  const chanEvidence = t => t?.chan?.schema_version === 1 ? t.chan : null;
  function indicatorSummary(f) {
    const x = signalIndicators(f);
    return x ? `RSI ${fmt(f.trend_metrics.rsi14)} · MACD柱 ${fmt(x.macd_histogram, 4)} · 布林 %B ${fmt(x.bollinger.percent_b, 3)}` : "未记录";
  }
  function chanSummary(t) {
    const c = chanEvidence(t);
    return c ? c.levels.map(l => `${l.period_minutes}m ${l.status === "pass" ? "通过" : l.status === "unknown" ? "缺失" : "不通过"}`).join(" · ") : null;
  }
  function chanDetail(t) {
    const c = chanEvidence(t);
    if (!c) return "";
    const levels = c.levels.map(l => { const z = l.latest_zone; return `${l.period_minutes}m：${l.status === "pass" ? "通过" : l.status === "unknown" ? "缺失" : "不通过"}；完成笔 ${l.finished_strokes}，最近笔 ${l.last_finished_stroke || "—"}${l.last_fractal ? `，最近分型 ${l.last_fractal === "bottom" ? "底" : "顶"} @ ${fmt(l.last_fractal_high)}` : ""}${z ? `，中枢 ${fmt(z.lower)}～${fmt(z.upper)}` : ""}${l.reason ? `；${l.reason}` : ""}`; });
    return `缠论 ${c.rule}（信号日收盘截止）：${levels.join("；")}。总体${c.status === "pass" ? "通过" : c.status === "unknown" ? "缺失" : "不通过"}${c.reason ? `：${c.reason}` : ""}。`;
  }
  function renderTrades() {
    const filter = $("trade-filter").value;
    const trades = list(state.current?.result?.development?.trades).filter(t => filter === "all" || (filter === "selected" && t.selected) || (filter === "rejected" && !t.selected) || (filter === "loss" && t.selected && resolved(t.outcome) && t.outcome.net_pct < 0) || (filter === "breaches" && breaches(t)) || (filter === "entry-negative" && t.selected && entryMark(t) && entryMark(t).close_net_pct <= 0) || (filter === "joint25" && t.selected && entryMark(t)?.close_net_pct > 0 && touch(t)?.first_2_5_day === 1 && !touch(t).first_2_5_order_unknown) || (filter === "joint30" && t.selected && entryMark(t)?.close_net_pct > 0 && touch(t)?.first_3_day === 1 && !touch(t).first_3_order_unknown) || (filter === "next25" && t.selected && touch(t)?.first_2_5_day === 1 && !touch(t).first_2_5_order_unknown) || (filter === "next30" && t.selected && touch(t)?.first_3_day === 1 && !touch(t).first_3_order_unknown) || (filter === "touch25" && t.selected && touch(t)?.first_2_5_day > 0 && !touch(t).first_2_5_order_unknown) || (filter === "touch30" && t.selected && touch(t)?.first_3_day > 0 && !touch(t).first_3_order_unknown));
    const pages = Math.max(1, Math.ceil(trades.length / 20)); state.page = Math.min(state.page, pages - 1);
    $("trade-body").replaceChildren(...trades.slice(state.page * 20, state.page * 20 + 20).map(t => {
      const tr = node("tr"); tr.dataset.trade = t.id;
      tr.append(node("td", `${t.date} · ${t.code}`), node("td", t.selected ? outcomes[t.outcome.status] || t.outcome.status : "被本轮过滤"), node("td", chanSummary(t) || indicatorSummary(t.features)), node("td", entryMark(t) ? `${fmt(entryMark(t).close_net_pct, 3)}%` : "未知"), node("td", resolved(t.outcome) ? `${fmt(t.outcome.net_pct, 3)}%` : "—"), node("td", touch(t) ? `${fmt(touch(t).max_net_pct, 3)}%` : "未记录 / 待核实"), node("td", touchDays(touch(t))), node("td", `${fmt(t.features.upper_wick_ratio * 100)}%`));
      const cell = node("td"); cell.append(button("查看 K 线", () => evidence(t))); tr.append(cell); return tr;
    }));
    if (!trades.length) emptyRow("trade-body", 9, "当前筛选条件下没有交易记录。");
    text("trade-page", `${trades.length} 条 · ${state.page + 1} / ${pages} 页`); $("trade-prev").disabled = state.page === 0; $("trade-next").disabled = state.page === pages - 1;
  }
  function svg(tag, attrs, value) { const n = document.createElementNS("http://www.w3.org/2000/svg", tag); for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v); if (value !== undefined) n.textContent = value; return n; }
  async function evidence(trade) {
    state.evidenceController?.abort(); state.evidenceController = new AbortController(); const sequence = ++state.evidenceRequest, id = state.id;
    show("evidence-panel", true); $("evidence-chart").replaceChildren(); text("evidence-detail", "");
    const indicators = signalIndicators(trade.features), m = trade.features.trend_metrics;
    const indicatorText = indicators ? `信号日已完成前复权日线指标（图中成交证据是不复权价）：RSI14 ${fmt(indicators.previous_rsi14)} → ${fmt(m.rsi14)}；MACD DIF ${fmt(m.dif, 4)}，DEA ${fmt(m.dea, 4)}，柱 ${fmt(indicators.previous_macd_histogram, 4)} → ${fmt(indicators.macd_histogram, 4)}。布林线(20,2) 下轨 ${fmt(indicators.bollinger.lower, 4)}，中轨 ${fmt(indicators.bollinger.middle, 4)}，上轨 ${fmt(indicators.bollinger.upper, 4)}；%B ${fmt(indicators.previous_percent_b, 3)} → ${fmt(indicators.bollinger.percent_b, 3)}，带宽 ${fmt(indicators.bollinger.width_pct)}%；—表示零带宽等导致未定义。ATR14 ${fmt(trade.features.atr14_pct)}%，距MA20 ${fmt(trade.features.ma20_distance_pct)}%，量比 ${fmt(trade.features.volume_ratio)}。` : "旧档案未保存信号日 RSI/MACD 变化与布林线证据，不从未来行情补算。";
    text("evidence-indicators", `${indicatorText}${chanDetail(trade) ? ` ${chanDetail(trade)}` : ""}`);
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
      const mark = entryMark(trade), zone = trade.entry_zone;
      text("evidence-detail", `买入区间 ${zone ? `${fmt(zone.lower)}～${fmt(zone.upper)} 元，反弹 ≥${fmt(zone.rebound_pct)}%` : "开盘买入"}；成交 ${mark ? `${fmt(mark.fill_price)} 元 / ${mark.entry_at || o.entry_date}，买入日收盘净浮盈 ${fmt(mark.close_net_pct, 3)}%` : "入场浮盈信息未知"}。模拟入场 ${o.entry_date || "未入场"}，退出 ${o.exit_date || "未确定"}；${outcomes[o.status] || o.status}。${resolved(o) ? `净收益 ${fmt(o.net_pct, 3)}%，退出价 ${fmt(o.exit_price)}。` : "结果未决，不计入已完成交易收益。"}最高价净收益 ${h ? fmt(h.max_net_pct, 3) + "%" : "未记录 / 待核实"}；首次触达 2.5% / 3%：${touchDays(h)}（买入后交易日）。价格触达不要求收盘达标，也不等于实际成交。信号上影线 ${fmt(trade.features.upper_wick_ratio * 100)}%，ATR ${fmt(trade.features.atr14_pct)}%。`);
    } catch (e) { if (sequence === state.evidenceRequest && e.name !== "AbortError") { text("evidence-status", e.message); $("evidence-chart").replaceChildren(button("重试证据", () => evidence(trade))); } }
  }
  $("backtest-settings").addEventListener("submit", event => {
    event.preventDefault();
    const from = $("backtest-from").value, through = $("backtest-through").value, policy = $("backtest-policy").value, r = state.current;
    text("backtest-command", "");
    if (!/^\d{4}-\d{2}-\d{2}$/.test(from) || !/^\d{4}-\d{2}-\d{2}$/.test(through) || from > through) { text("backtest-command-status", "请填写有效且顺序正确的信号起止日期。"); return; }
    const frozen = r && !researchSnapshot(r) && (!policy.startsWith("conditional-") || r.data.minute_stocks > 0);
    const requiresParent = !["conditional-entry", "next-day-baseline"].includes(policy);
    if (requiresParent && !frozen) { text("backtest-command-status", "该对照规则需要可复用的父实验；条件指标对照请先建立含分钟数据的区间低吸基线。"); return; }
    text("backtest-command", `./bin/stock-watcher experiment -config config.yaml -policy ${policy}${frozen ? ` -parent ${r.id}` : ""} -from ${from} -through ${through}`);
    text("backtest-command-status", frozen ? "复用当前快照；明确指定信号区间。区间变化时不展示父实验提升差值。" : "创建包含所需行情的新快照；条件低吸需要已保存的5分钟路径，缺失时不会假设最低价成交。");
  });
  $("show-breaches").addEventListener("click", () => { $("trade-filter").value = "breaches"; state.page = 0; renderTrades(); $("trade-section").scrollIntoView({ block: "start" }); });
  $("snapshot-row").addEventListener("change", renderSnapshotMetrics);
  $("snapshot-download").addEventListener("click", () => {
    if (state.current?.status !== "completed" || !state.current.result_snapshot) return;
    const blob = new Blob([JSON.stringify(state.current.result_snapshot, null, 2) + "\n"], { type: "application/json;charset=utf-8" });
    const url = URL.createObjectURL(blob), link = node("a"); link.href = url; link.download = `experiment-result-${state.current.id}.json`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  $("refresh-experiments").addEventListener("click", refresh); $("lab-retry").addEventListener("click", refresh); $("detail-retry").addEventListener("click", () => select(state.id));
  $("experiment-search").addEventListener("input", renderList); $("experiment-filter").addEventListener("change", renderList);
  $("group-kind").addEventListener("change", renderGroups); $("trade-filter").addEventListener("change", () => { state.page = 0; renderTrades(); });
  $("trade-prev").addEventListener("click", () => { state.page--; renderTrades(); }); $("trade-next").addEventListener("click", () => { state.page++; renderTrades(); });
  window.addEventListener("popstate", () => { const id = new URL(location.href).searchParams.get("id") || state.records[0]?.id; if (id) select(id); });
  refresh();
})();
