"use strict";

(() => {
  const byId = id => document.getElementById(id);
  const form = byId("strategy-form");
  const control = name => form.elements.namedItem(name);
  const node = (tag, text, className) => {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = text;
    if (className) element.className = className;
    return element;
  };
  // Formatting does not convert financial amounts to binary floating point.
  function display(value, digits = 2) {
    const negative = value.startsWith("-");
    const [whole, fraction = ""] = value.replace(/^-/, "").split(".");
    const scale = 10n ** BigInt(digits);
    const rounded = BigInt(whole) * scale + BigInt((fraction + "0".repeat(digits)).slice(0, digits) || "0") + (Number(fraction[digits] || "0") >= 5 ? 1n : 0n);
    const integer = (rounded / scale).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ",");
    return (negative && rounded ? "-" : "") + integer + (digits ? "." + (rounded % scale).toString().padStart(digits, "0") : "");
  }
  const money = value => display(value) + " 元";
  // Exit prices must retain at least the precision of the selected quotation tick.
  const priceText = value => display(value, Math.max(2, (control("tick").value.split(".")[1] || "").length)) + " 元";
  function addHolding(lot = {}) {
    const row = node("div", undefined, "holding-row");
    const heading = node("div", undefined, "holding-heading");
    heading.append(node("strong", "历史买入"));
    const remove = node("button", "删除", "button button-secondary");
    remove.type = "button";
    remove.addEventListener("click", () => {
      const next = row.nextElementSibling || row.previousElementSibling;
      row.remove();
      if (!byId("holdings").children.length) addHolding();
      renameHoldings(); invalidate();
      (next?.querySelector("input") || byId("holdings").querySelector("input")).focus();
    });
    heading.append(remove);
    row.append(heading);
    const fields = node("div", undefined, "fields-grid");
    for (const [key, label, fallback] of [["price", "买入价（元）", ""], ["shares", "股数", ""], ["buy_fee", "已付买入费用（元）", "0"]]) {
      const wrapper = node("label", label, "field");
      const input = node("input");
      input.dataset.key = key;
      input.inputMode = key === "shares" ? "numeric" : "decimal";
      input.value = lot[key] ?? fallback;
      input.required = true;
      wrapper.append(input); fields.append(wrapper);
    }
    row.append(fields); byId("holdings").append(row); renameHoldings();
  }
  function renameHoldings() {
    [...byId("holdings").children].forEach((row, i) => {
      row.querySelector("strong").textContent = `第 ${i + 1} 批买入`;
      row.querySelector("button").setAttribute("aria-label", `删除第 ${i + 1} 批持仓`);
      row.querySelectorAll("input").forEach(input => { input.name = `hold_${i}_${input.dataset.key}`; });
    });
    byId("add-holding").disabled = byId("holdings").children.length >= 100;
  }
  function modes() {
    const initial = control("mode").value === "initial";
    byId("initial-fields").hidden = !initial;
    byId("initial-fields").disabled = !initial;
    byId("holding-fields").hidden = initial;
    byId("holding-fields").disabled = initial;
    const pct = control("target_mode").value === "pct";
    byId("target-pct-field").hidden = !pct;
    byId("target-profit-field").hidden = pct;
    control("target_pct").disabled = !pct;
    control("target_profit").disabled = pct;
  }
  function invalidate() {
    byId("strategy-results").hidden = true;
    byId("result-empty").hidden = false;
    byId("result-empty").textContent = "参数已变更，请点击「计算仓位」查看本次结果。";
    byId("result-tag").textContent = "等待计算";
    byId("form-error").hidden = true;
    form.querySelectorAll("[aria-invalid]").forEach(input => { input.removeAttribute("aria-invalid"); input.removeAttribute("aria-describedby"); });
  }
  function inputValues() {
    const values = {};
    for (const [name, value] of new FormData(form)) {
      if (!name.startsWith("hold_") && !["mode", "target_mode"].includes(name)) values[name] = value.trim();
    }
    if (!values.add_price) delete values.add_price;
    if (control("mode").value === "holding") {
      values.hold = [...byId("holdings").children].map(row => Object.fromEntries([...row.querySelectorAll("input")].map(input => [input.dataset.key, input.value.trim()])));
    }
    return values;
  }
  function detailsList(entries) {
    const list = node("dl", undefined, "result-values");
    for (const [label, value, field] of entries) {
      const row = node("div");
      const output = node("dd", value, field === "target_price" ? "target-value" : undefined);
      if (field) output.dataset.result = field;
      row.append(node("dt", label), output); list.append(row);
    }
    return list;
  }
  function render(result) {
    byId("risk-budget").textContent = money(result.risk_budget);
    byId("result-empty").hidden = true;
    byId("strategy-results").hidden = false;
    byId("result-tag").textContent = "已计算";
    byId("result-status").textContent = "以下为当前参数下的测算。修改参数后需重新计算。";
    byId("order-results").replaceChildren();
    const reasons = { risk: "风险额度", budget: "单股投入额度", cash: "可用现金", no_initial_position: "底仓未能建立" };
    for (const order of result.orders) {
      const card = node("article", undefined, "order-result");
      card.dataset.order = order.kind;
      card.append(node("h3", `${order.kind === "initial" ? "底仓" : "加仓"} · ${priceText(order.price)}`));
      const quantity = node("div", `最多 ${display(order.shares, 0)} 股`, "order-quantity");
      quantity.dataset.result = "shares"; card.append(quantity);
      if (order.status === "blocked") {
        card.append(node("p", order.blocked_by.includes("no_initial_position") ? "底仓未能建立，不计算后续加仓。" : `${order.blocked_by.map(k => reasons[k]).join("、")}不足以组成最小买入数量，不向上凑单。`, "blocked"));
      } else {
        card.append(detailsList([
          ["追加现金（含买入费用）", money(order.cash_required)],
          ["占账户总资产", display(order.account_allocation_pct) + "%"],
          ["占单股投入上限", display(order.budget_allocation_pct) + "%"],
        ]));
      }
      byId("order-results").append(card);
    }
    const p = result.final_position;
    byId("position-results").hidden = !p;
    if (p) {
      byId("position-values").replaceWith(Object.assign(detailsList([
        ["总持仓", display(p.shares, 0) + " 股", "shares"],
        ["成交均价", display(p.average_price, 4) + " 元", "average_price"],
        ["买入支出（含费用）", money(p.cost_including_buy_fees), "cost_including_buy_fees"],
        ["风险占用", money(p.risk_used), "risk_used"],
        ["剩余风险", money(p.risk_remaining), "risk_remaining"],
        ["剩余现金", money(result.cash_remaining), "cash_remaining"],
        ["净利润目标", money(p.target_profit), "target_profit"],
        ["目标卖出成交价", priceText(p.target_price), "target_price"],
        ["策略止损触发价", priceText(control("stop").value)],
        ["假设止损成交价", priceText(result.assumed_stop_execution_price)],
        ["资金止损边界成交价", priceText(p.loss_budget_execution_price), "loss_budget_execution_price"],
        ["含滑点的资金止损边界", priceText(p.loss_budget_trigger_price), "loss_budget_trigger_price"],
      ]), { id: "position-values" }));
    }
    byId("result-warnings").replaceChildren(...result.warnings.map(message => node("li", message)));
  }
  function setup(calculateStrategy) {
    addHolding(); modes();
    byId("calculator-controls").disabled = false;
    byId("calculator-loading").hidden = true;
    form.addEventListener("input", invalidate);
    form.addEventListener("change", () => { modes(); invalidate(); });
    byId("add-holding").addEventListener("click", () => {
      if (byId("holdings").children.length >= 100) return;
      addHolding(); invalidate(); byId("holdings").lastElementChild.querySelector("input").focus();
    });
    byId("load-example").addEventListener("click", () => {
      form.reset();
      const example = { account: "100000", cash: "80000", budget: "20000", max_loss: "500", risk_pct: "0.5", stop: "45", slippage: "0.10", add_price: "46", buy_fee: "0", sell_fee: "20", mode: "holding", target_mode: "pct", target_pct: "3" };
      for (const [key, value] of Object.entries(example)) control(key).value = value;
      byId("holdings").replaceChildren(); addHolding({ price: "47.05", shares: "215", buy_fee: "10" });
      modes(); invalidate();
      byId("result-empty").textContent = "已载入演算示例。账户资金、45 元止损与费用均为假设，请核对后计算。";
    });
    form.addEventListener("reset", event => {
      event.preventDefault();
      for (const element of form.querySelectorAll("input")) {
        if (element.type === "radio") element.checked = element.defaultChecked;
        else element.value = element.defaultValue;
      }
      control("target_mode").value = "pct";
      byId("holdings").replaceChildren(); addHolding(); modes(); invalidate();
    });
    form.addEventListener("submit", event => {
      event.preventDefault(); invalidate();
      try { render(calculateStrategy(inputValues())); }
      catch (error) {
        byId("form-error").textContent = error.field ? error.message : "无法完成计算，请核对输入后重试。";
        byId("form-error").hidden = false;
        const field = control(error.field);
        if (field?.focus) {
          const details = field.closest("details");
          if (details) details.open = true;
          field.setAttribute("aria-invalid", "true"); field.setAttribute("aria-describedby", "form-error"); field.focus();
        }
      }
    });
    // Browsers may restore form values on back/forward navigation; clear them
    // when this document is restored from the back-forward cache.
    window.addEventListener("pageshow", event => { if (event.persisted) form.reset(); });
  }
  byId("reload-calculator").addEventListener("click", () => location.reload());
  let timer;
  Promise.race([
    import("/assets/strategy-model.js"),
    new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("加载超时，请检查网络后重新加载。")), 10000); }),
  ]).then(module => setup(module.calculateStrategy)).catch(error => {
    byId("calculator-loading").className = "notice notice-error";
    byId("calculator-loading").setAttribute("role", "alert");
    byId("calculator-loading").querySelector("strong").textContent = "计算器加载失败";
    byId("calculator-loading").querySelector("p").textContent = error.message.includes("超时") ? error.message : "请检查网络或访问权限后重新加载。";
  }).finally(() => clearTimeout(timer));
})();
