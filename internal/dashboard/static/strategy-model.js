// Browser counterpart of scripts/calculate_strategy.sh. No network or storage.
// Rational BigInt arithmetic keeps quantity limits and price ticks exact.
class Decimal {
  constructor(n, d = 1n) {
    if (d === 0n) throw new Error("除数不能为零");
    if (d < 0n) { n = -n; d = -d; }
    let a = n < 0n ? -n : n, b = d;
    while (b) [a, b] = [b, a % b];
    this.n = n / a; this.d = d / a;
  }
  static from(value) {
    if (value instanceof Decimal) return value;
    const [whole, fraction = ""] = String(value).split(".");
    return new Decimal(BigInt(whole + fraction), 10n ** BigInt(fraction.length));
  }
  add(v) { v = Decimal.from(v); return new Decimal(this.n * v.d + v.n * this.d, this.d * v.d); }
  sub(v) { v = Decimal.from(v); return new Decimal(this.n * v.d - v.n * this.d, this.d * v.d); }
  mul(v) { v = Decimal.from(v); return new Decimal(this.n * v.n, this.d * v.d); }
  div(v) { v = Decimal.from(v); return new Decimal(this.n * v.d, this.d * v.n); }
  cmp(v) { v = Decimal.from(v); const diff = this.n * v.d - v.n * this.d; return diff < 0n ? -1 : diff > 0n ? 1 : 0; }
  floor() { return this.n / this.d - (this.n < 0n && this.n % this.d !== 0n ? 1n : 0n); }
  ceil() { return this.n / this.d + (this.n > 0n && this.n % this.d !== 0n ? 1n : 0n); }
  toJSON() {
    const n = this.n < 0n ? -this.n : this.n;
    const fraction = ((n % this.d) * 10n ** 40n / this.d).toString().padStart(40, "0").replace(/0+$/, "");
    return (this.n < 0n ? "-" : "") + n / this.d + (fraction ? "." + fraction : "");
  }
}

export class InputError extends Error {
  constructor(field, message) { super(message); this.field = field; }
}
const zero = Decimal.from(0);
const min = (...values) => values.reduce((a, b) => a.cmp(b) < 0 ? a : b);
const positive = (value) => value.cmp(0) > 0 ? value : zero;
const labels = {
  account: "账户总资产", cash: "可用现金", budget: "单股投入上限", max_loss: "最大亏损金额",
  risk_pct: "账户风险比例", stop: "策略止损触发价", slippage: "每股滑点预留", entry: "首次买入价",
  add_price: "计划加仓价", initial_risk_pct: "底仓风险分配", buy_fee: "新增每笔买入费用",
  sell_fee: "全部卖出费用", target_pct: "净止盈收益率", target_profit: "净止盈金额",
  min_shares: "最小买入股数", lot_step: "股数步长", tick: "报价单位",
};
const defaults = { risk_pct: "0.5", slippage: "0", initial_risk_pct: "50", buy_fee: "0", sell_fee: "0", min_shares: "200", lot_step: "1", tick: "0.01" };

function read(value, field, label = labels[field]) {
  if (typeof value !== "string" || !/^[0-9]+(?:\.[0-9]+)?$/.test(value) || value.length > 24) {
    throw new InputError(field, `${label}：请输入非负十进制数，不支持科学计数法或空值。`);
  }
  const number = Decimal.from(value);
  if (number.cmp("1000000000000") > 0 || (value.split(".")[1] || "").length > 8) {
    throw new InputError(field, `${label}：数值上限为 10^12，最多 8 位小数。`);
  }
  return number;
}

function validate(input) {
  const args = {};
  for (const field of ["account", "cash", "budget", "max_loss", "stop", ...Object.keys(defaults)]) {
    args[field] = read(input[field] === undefined ? defaults[field] : input[field], field);
  }
  for (const field of ["entry", "add_price", "target_pct", "target_profit"]) {
    args[field] = input[field] === undefined || input[field] === null ? null : read(input[field], field);
  }
  for (const field of ["account", "budget", "max_loss", "stop", "tick"]) {
    if (args[field].cmp(0) <= 0) throw new InputError(field, `${labels[field]}必须大于 0。`);
  }
  for (const field of ["cash", "budget"]) {
    if (args[field].cmp(args.account) > 0) throw new InputError(field, `${labels[field]}不能超过账户总资产。`);
  }
  for (const field of ["risk_pct", "initial_risk_pct"]) {
    if (args[field].cmp(0) <= 0 || args[field].cmp(100) > 0) throw new InputError(field, `${labels[field]}必须大于 0 且不超过 100%。`);
  }
  for (const field of ["min_shares", "lot_step"]) {
    if (args[field].cmp(0) <= 0 || args[field].d !== 1n) throw new InputError(field, `${labels[field]}必须为正整数。`);
  }
  if (args.min_shares.n % args.lot_step.n) throw new InputError("min_shares", "最小买入股数必须为股数步长的整数倍。");
  if (args.slippage.cmp(args.stop) >= 0) throw new InputError("slippage", "每股滑点预留必须小于止损触发价。");
  if (!Array.isArray(input.hold || []) || (input.hold || []).length > 100) throw new InputError("hold", "持仓批次必须是数组，最多 100 批。");
  args.hold = (input.hold || []).map((lot, i) => {
    const field = `hold_${i}`;
    const price = read(lot.price, `${field}_price`, "持仓买入价");
    const shares = read(lot.shares, `${field}_shares`, "持仓股数");
    const buy_fee = read(lot.buy_fee === undefined ? "0" : lot.buy_fee, `${field}_buy_fee`, "已付买入费用");
    if (price.cmp(0) <= 0) throw new InputError(`${field}_price`, "持仓买入价必须大于 0。");
    if (shares.cmp(0) <= 0 || shares.d !== 1n) throw new InputError(`${field}_shares`, "持仓股数必须为正整数。");
    return { price, shares: shares.n, buy_fee };
  });
  if (args.hold.length && args.entry !== null) throw new InputError("entry", "已有持仓时请填写加仓价，不应填写首次买入价。");
  if (!args.hold.length && args.entry === null) throw new InputError("entry", "首次建仓必须填写买入价。");
  for (const field of ["entry", "add_price"]) {
    if (args[field] !== null && args[field].cmp(args.stop) <= 0) throw new InputError(field, `${labels[field]}必须高于策略止损触发价。`);
  }
  for (const field of ["entry", "add_price", "stop"]) {
    if (args[field] !== null && args[field].div(args.tick).d !== 1n) throw new InputError(field, `${labels[field]}必须符合报价单位。`);
  }
  if (args.target_profit !== null && args.target_pct !== null) throw new InputError("target_profit", "止盈金额和收益率只能选择一种。");
  if (args.target_profit === null && args.target_pct === null) args.target_pct = Decimal.from(3);
  if ((args.target_profit || args.target_pct).cmp(0) <= 0) throw new InputError(args.target_profit !== null ? "target_profit" : "target_pct", "止盈金额或收益率必须大于 0。");
  return args;
}

// All externally visible numeric values are decimal strings, including quantities.
// Non-terminating averages/ratios are truncated at 40 decimal places for display;
// share limits and rounded exit prices are calculated using exact fractions.
export function calculateStrategy(input) {
  const a = validate(input);
  const riskBudget = min(a.max_loss, a.account.mul(a.risk_pct).div(100));
  const executionStop = a.stop.sub(a.slippage);
  const ceilPrice = (v) => a.tick.mul(positive(v).div(a.tick).ceil());
  function totals(lots) {
    return lots.reduce((t, l) => ({
      shares: t.shares + l.shares, invested: t.invested.add(l.price.mul(l.shares)),
      fees: t.fees.add(l.buy_fee), priceRisk: t.priceRisk.add(positive(l.price.sub(executionStop)).mul(l.shares)),
    }), { shares: 0n, invested: zero, fees: zero, priceRisk: zero });
  }
  function position(lots) {
    const t = totals(lots);
    if (!t.shares) return null;
    const cost = t.invested.add(t.fees);
    const profit = a.target_profit || cost.mul(a.target_pct).div(100);
    const target = ceilPrice(cost.add(profit).add(a.sell_fee).div(t.shares));
    const risk = t.priceRisk.add(t.fees).add(a.sell_fee);
    return {
      shares: t.shares, invested: t.invested, buy_fees: t.fees, cost_including_buy_fees: cost,
      average_price: t.invested.div(t.shares), account_position_pct: t.invested.div(a.account).mul(100),
      budget_used_pct: t.invested.div(a.budget).mul(100), risk_used: risk, risk_remaining: riskBudget.sub(risk),
      risk_exceeded: risk.cmp(riskBudget) > 0, budget_exceeded: t.invested.cmp(a.budget) > 0,
      target_profit: profit, target_price: target, net_profit_at_target: target.mul(t.shares).sub(a.sell_fee).sub(cost),
      loss_budget_execution_price: ceilPrice(cost.sub(riskBudget).add(a.sell_fee).div(t.shares)),
      loss_budget_trigger_price: ceilPrice(cost.sub(riskBudget).add(a.sell_fee).div(t.shares).add(a.slippage)),
    };
  }
  let lots = [...a.hold], cash = a.cash;
  const current = position(lots), orders = [];
  function order(price, initial) {
    const t = totals(lots);
    let fees = t.fees.add(a.buy_fee).add(a.sell_fee);
    let available;
    if (initial) {
      if (a.add_price !== null) fees = fees.add(a.buy_fee);
      available = positive(riskBudget.sub(fees)).mul(a.initial_risk_pct).div(100);
    } else available = positive(riskBudget.sub(fees).sub(t.priceRisk));
    const limits = {
      risk: available.div(price.sub(executionStop)).floor(),
      budget: positive(a.budget.sub(t.invested)).div(price).floor(),
      cash: positive(cash.sub(a.buy_fee)).div(price).floor(),
    };
    const raw = Object.values(limits).reduce((x, y) => x < y ? x : y);
    let shares = raw / a.lot_step.n * a.lot_step.n;
    if (shares < a.min_shares.n) shares = 0n;
    if (shares) lots.push({ price, shares, buy_fee: a.buy_fee });
    const purchase = price.mul(shares), spent = purchase.add(shares ? a.buy_fee : zero);
    cash = cash.sub(spent);
    orders.push({
      kind: initial ? "initial" : "add", price, shares, status: shares ? "eligible" : "blocked", limits_shares: limits,
      blocked_by: Object.keys(limits).filter(k => limits[k] < a.min_shares.n),
      binding_limits: Object.keys(limits).filter(k => limits[k] === raw), purchase_amount: purchase, cash_required: spent,
      account_allocation_pct: purchase.div(a.account).mul(100), budget_allocation_pct: purchase.div(a.budget).mul(100),
      cash_after: cash, position_after: position(lots),
    });
  }
  if (!lots.length) order(a.entry, true);
  if (a.add_price !== null) {
    if (lots.length) order(a.add_price, false);
    else orders.push({ kind: "add", price: a.add_price, shares: 0n, status: "blocked", blocked_by: ["no_initial_position"] });
  }
  const warnings = [
    "数量是预算允许的上限，加仓仍需满足你预设的交易条件。",
    "T+1、跳空和流动性不足可能使实际亏损超过预算，止损不保证成交。",
    "费用使用固定金额估算，交易规模变化后需重新核对；未计融资和部分卖出。",
  ];
  if (a.sell_fee.cmp(0) === 0 || (a.buy_fee.cmp(0) === 0 && orders.length) || a.hold.some(l => l.buy_fee.cmp(0) === 0)) warnings.push("存在费用为 0 的项目，未填费用不会自动补算，实际净收益可能低于目标。");
  if (a.slippage.cmp(0) === 0) warnings.push("滑点预留为 0，风险预算未覆盖不利成交价差。");
  if (current?.risk_exceeded) warnings.push("现有持仓已超过整笔风险预算，不允许继续加仓。");
  if (current?.budget_exceeded) warnings.push("现有持仓已超过单股投入上限，不允许继续加仓。");
  return JSON.parse(JSON.stringify({
    schema_version: 1, risk_budget: riskBudget, assumed_stop_execution_price: executionStop,
    current_position: current, orders, final_position: position(lots), cash_remaining: cash, warnings,
  }, (_, value) => typeof value === "bigint" ? String(value) : value));
}
