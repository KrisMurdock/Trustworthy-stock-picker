#!/usr/bin/env bash
# 独立、离线的仓位计算器。Python 仅用于此维护脚本，不进入生产 Go 运行链路。
set -euo pipefail
if ! command -v python3 >/dev/null 2>&1; then
  printf '%s\n' 'Error: python3 is required for decimal strategy calculations.' >&2
  exit 127
fi
# Python 从 heredoc 读取程序，保留原 stdin 供交互模式使用。
exec 3<&0
exec python3 - "$@" <<'PY'
import argparse
import json
import os
import re
import sys
from decimal import Decimal, ROUND_CEILING, ROUND_FLOOR, getcontext

getcontext().prec = 50
D = Decimal
ZERO = D(0)
HUNDRED = D(100)


def number(value):
    if not re.fullmatch(r"[0-9]+(?:\.[0-9]+)?", value) or len(value) > 24:
        raise argparse.ArgumentTypeError("请输入非负十进制数，不支持 NaN、Infinity 或科学计数法")
    result = D(value)
    if result > D("1000000000000") or result.as_tuple().exponent < -8:
        raise argparse.ArgumentTypeError("数值上限为 10^12，最多 8 位小数")
    return result


def integer(value):
    result = number(value)
    if result != result.to_integral_value() or result < 1:
        raise argparse.ArgumentTypeError("股数和交易单位必须是正整数")
    return int(result)


def holding(value):
    parts = value.split(":")
    if len(parts) not in (2, 3):
        raise argparse.ArgumentTypeError("持仓格式为 买入价:股数[:已付买入费用]")
    price, shares = number(parts[0]), integer(parts[1])
    if price <= 0:
        raise argparse.ArgumentTypeError("持仓买入价必须大于 0")
    return {"price": price, "shares": shares,
            "buy_fee": number(parts[2]) if len(parts) == 3 else ZERO}


parser = argparse.ArgumentParser(
    description="单股仓位计算：固定风险预算、分批买入、净止盈与资金止损边界（离线，不下单）。",
    epilog="百分比直接输入 0.5 或 3。无参数且连接终端时进入交互模式。详见 docs/单股价格策略.md。")
parser.add_argument("--account", type=number, help="账户总资产：现金＋股票当前市值（元）")
parser.add_argument("--cash", type=number, help="当前可用现金，已扣除历史买入支出（元）")
parser.add_argument("--budget", type=number, help="本股累计买入成交金额上限，不含费用（元）")
parser.add_argument("--max-loss", type=number, help="整笔交易最大亏损预算，包含费用（元）")
parser.add_argument("--risk-pct", type=number, default=D("0.5"), help="占账户风险比例，默认 0.5%%")
parser.add_argument("--stop", type=number, help="策略止损触发价，不由成本自动推断")
parser.add_argument("--slippage", type=number, default=ZERO, help="每股不利成交价差预留，默认 0 元")
parser.add_argument("--entry", type=number, help="首次计划买入价；已有持仓时不能使用")
parser.add_argument("--add-price", type=number, help="计划加仓价；有底仓才计算加仓")
parser.add_argument("--hold", type=holding, action="append", default=[], help="已有持仓 买入价:股数[:买入费用]，可重复")
parser.add_argument("--initial-risk-pct", type=number, default=D(50), help="底仓使用的可分配风险比例，默认 50%%")
target = parser.add_mutually_exclusive_group()
target.add_argument("--target-pct", type=number, help="净利润占实际买入支出的比例，默认 3%%")
target.add_argument("--target-profit", type=number, help="固定净利润目标（元），替代收益率目标")
parser.add_argument("--buy-fee", type=number, default=ZERO, help="每次新增买入的费用估算；默认 0，必须按实际填写")
parser.add_argument("--sell-fee", type=number, default=ZERO, help="最终全部卖出的总费用估算（含税）；默认 0")
parser.add_argument("--min-shares", type=integer, default=200, help="单笔最少买入股数，默认 200")
parser.add_argument("--lot-step", type=integer, default=1, help="买入股数步长，默认 1；主板通常配 100/100")
parser.add_argument("--tick", type=number, default=D("0.01"), help="价格最小变动单位，默认 0.01 元")
parser.add_argument("--json", action="store_true", help="输出 schema_version=1 的 JSON，金额使用十进制字符串")
parser.add_argument("--interactive", action="store_true", help="逐项交互输入，也可通过 stdin 提供答案")


def interactive(args):
    with os.fdopen(3, "r", encoding="utf-8", closefd=False) as source:
        def ask(label, default=None, convert=number):
            suffix = "" if default is None else " [" + str(default) + "]"
            print(label + suffix + ": ", end="", file=sys.stderr, flush=True)
            line = source.readline()
            if not line:
                parser.error("交互输入提前结束；未执行任何交易")
            value = line.strip()
            if not value and default is not None:
                return default
            try:
                return convert(value)
            except argparse.ArgumentTypeError as error:
                parser.error(str(error))

        print("金额单位：元；百分比填写 0.5、3 等。费用和滑点为 0 时不会计入预算。", file=sys.stderr)
        for key, label in (("account", "账户总资产"), ("cash", "当前可用现金"),
                           ("budget", "本股累计买入金额上限"), ("max_loss", "整笔最大亏损金额")):
            setattr(args, key, ask(label, getattr(args, key)))
        args.risk_pct = ask("账户风险比例（%）", args.risk_pct)
        args.stop = ask("策略止损触发价", args.stop)
        args.slippage = ask("每股不利成交价差", args.slippage)
        if not args.hold:
            value = ask("已有持仓（价:股数:费用，多个用逗号分隔；无则回车）", "", str)
            if value:
                try:
                    args.hold = [holding(item.strip()) for item in value.split(",")]
                except argparse.ArgumentTypeError as error:
                    parser.error(str(error))
        if not args.hold:
            args.entry = ask("首次计划买入价", args.entry)
            args.initial_risk_pct = ask("底仓分配风险比例（%）", args.initial_risk_pct)
        value = ask("计划加仓价（不测算则回车）", args.add_price if args.add_price is not None else "", str)
        if value != "":
            try:
                args.add_price = number(str(value))
            except argparse.ArgumentTypeError as error:
                parser.error(str(error))
        args.buy_fee = ask("每次新增买入费用估算", args.buy_fee)
        args.sell_fee = ask("最终卖出总费用估算（含税）", args.sell_fee)
        if args.target_profit is None:
            args.target_pct = ask("净止盈收益率（%）", args.target_pct if args.target_pct is not None else D(3))
        else:
            args.target_profit = ask("固定净利润目标", args.target_profit)
        args.min_shares = ask("单笔最少买入股数", args.min_shares, integer)
        args.lot_step = ask("买入股数步长", args.lot_step, integer)


def validate(args):
    for key in ("account", "cash", "budget", "max_loss", "stop"):
        if getattr(args, key) is None:
            parser.error("缺少参数 --" + key.replace("_", "-"))
    for key in ("account", "budget", "max_loss", "stop", "tick"):
        if getattr(args, key) <= 0:
            parser.error("--" + key.replace("_", "-") + " 必须大于 0")
    if args.cash > args.account or args.budget > args.account:
        parser.error("可用现金和单股投入上限均不能超过账户总资产")
    for key in ("risk_pct", "initial_risk_pct"):
        if not ZERO < getattr(args, key) <= HUNDRED:
            parser.error("--" + key.replace("_", "-") + " 必须在 (0, 100] 内")
    if args.min_shares % args.lot_step:
        parser.error("最小买入股数必须是股数步长的整数倍")
    if args.slippage >= args.stop:
        parser.error("每股不利成交价差必须小于止损触发价")
    if args.hold and args.entry is not None:
        parser.error("已有持仓请使用 --add-price，不能同时提供 --entry")
    if not args.hold and args.entry is None:
        parser.error("无持仓时必须提供 --entry")
    for key in ("entry", "add_price"):
        value = getattr(args, key)
        if value is not None and value <= args.stop:
            parser.error("计划买入价必须高于策略止损触发价")
    for key in ("entry", "add_price", "stop"):
        value = getattr(args, key)
        if value is not None and value % args.tick:
            parser.error("计划买入价和止损价必须符合 --tick 报价单位")
    if args.target_profit is None and args.target_pct is None:
        args.target_pct = D(3)
    value = args.target_profit if args.target_profit is not None else args.target_pct
    if value <= 0:
        parser.error("止盈金额或收益率必须大于 0")


def calculate(args):
    risk_budget = min(args.max_loss, args.account * args.risk_pct / HUNDRED)
    execution_stop = args.stop - args.slippage

    def ceil_price(value):
        return (max(ZERO, value) / args.tick).to_integral_value(rounding=ROUND_CEILING) * args.tick

    def totals(lots):
        shares = sum(lot["shares"] for lot in lots)
        invested = sum((lot["price"] * lot["shares"] for lot in lots), ZERO)
        fees = sum((lot["buy_fee"] for lot in lots), ZERO)
        # 不用盈利批次的浮盈抵消其他批次风险。
        price_risk = sum((max(ZERO, lot["price"] - execution_stop) * lot["shares"] for lot in lots), ZERO)
        return shares, invested, fees, price_risk

    def position(lots):
        shares, invested, fees, price_risk = totals(lots)
        if not shares:
            return None
        cost = invested + fees
        profit = args.target_profit if args.target_profit is not None else cost * args.target_pct / HUNDRED
        target_price = ceil_price((cost + profit + args.sell_fee) / shares)
        risk = price_risk + fees + args.sell_fee
        return {
            "shares": shares, "invested": invested, "buy_fees": fees,
            "cost_including_buy_fees": cost, "average_price": invested / shares,
            "account_position_pct": invested / args.account * HUNDRED,
            "budget_used_pct": invested / args.budget * HUNDRED,
            "risk_used": risk, "risk_remaining": risk_budget - risk,
            "risk_exceeded": risk > risk_budget, "budget_exceeded": invested > args.budget,
            "target_profit": profit, "target_price": target_price,
            "net_profit_at_target": target_price * shares - args.sell_fee - cost,
            "loss_budget_execution_price": ceil_price((cost - risk_budget + args.sell_fee) / shares),
            "loss_budget_trigger_price": ceil_price((cost - risk_budget + args.sell_fee) / shares + args.slippage),
        }

    def order(lots, cash, price, initial=False):
        _, invested, paid_fees, price_risk = totals(lots)
        fees = paid_fees + args.buy_fee + args.sell_fee
        if initial:
            # 计划包含加仓时，预留第二笔买入费用，然后分配底仓风险。
            fees += args.buy_fee if args.add_price is not None else ZERO
            available_risk = max(ZERO, risk_budget - fees) * args.initial_risk_pct / HUNDRED
        else:
            available_risk = max(ZERO, risk_budget - fees - price_risk)
        limits = {
            "risk": int((available_risk / (price - execution_stop)).to_integral_value(rounding=ROUND_FLOOR)),
            "budget": int((max(ZERO, args.budget - invested) / price).to_integral_value(rounding=ROUND_FLOOR)),
            "cash": int((max(ZERO, cash - args.buy_fee) / price).to_integral_value(rounding=ROUND_FLOOR)),
        }
        raw = min(limits.values())
        shares = raw // args.lot_step * args.lot_step
        if shares < args.min_shares:
            shares = 0
        blockers = [key for key, limit in limits.items() if limit < args.min_shares]
        new_lots = list(lots)
        if shares:
            new_lots.append({"price": price, "shares": shares, "buy_fee": args.buy_fee})
        spent = price * shares + (args.buy_fee if shares else ZERO)
        return {
            "kind": "initial" if initial else "add", "price": price, "shares": shares,
            "status": "eligible" if shares else "blocked", "limits_shares": limits,
            "blocked_by": blockers, "binding_limits": [key for key, limit in limits.items() if limit == raw],
            "purchase_amount": price * shares, "cash_required": spent,
            "account_allocation_pct": price * shares / args.account * HUNDRED,
            "budget_allocation_pct": price * shares / args.budget * HUNDRED,
            "cash_after": cash - spent, "position_after": position(new_lots),
        }, new_lots, cash - spent

    lots = list(args.hold)
    cash = args.cash
    current = position(lots)
    orders = []
    if not lots:
        item, lots, cash = order(lots, cash, args.entry, initial=True)
        orders.append(item)
    if args.add_price is not None:
        if lots:
            item, lots, cash = order(lots, cash, args.add_price)
            orders.append(item)
        else:
            orders.append({"kind": "add", "price": args.add_price, "shares": 0,
                           "status": "blocked", "blocked_by": ["no_initial_position"]})
    warnings = [
        "计算结果是预算内的数量上限；加仓仍需满足预设信号，脚本不预测行情、不下单。",
        "止损不保证成交；T+1、跳空和流动性不足可能使损失超过预算。",
        "费用是固定金额估算；交易规模变化后应重新核对费用，未计融资和部分卖出。",
    ]
    if args.sell_fee == 0 or (args.buy_fee == 0 and orders) or any(lot["buy_fee"] == 0 for lot in args.hold):
        warnings.append("存在费用为 0 的项目：未填费用不会自动补算，实际净收益可能低于目标。")
    if args.slippage == 0:
        warnings.append("滑点预留为 0：风险预算未覆盖不利成交价差。")
    if current and current["risk_exceeded"]:
        warnings.append("现有持仓已超过整笔风险预算，不允许继续加仓。")
    if current and current["budget_exceeded"]:
        warnings.append("现有持仓已超过本股投入上限，不允许继续加仓。")
    return {"schema_version": 1, "inputs": vars(args), "risk_budget": risk_budget,
            "assumed_stop_execution_price": execution_stop, "current_position": current,
            "orders": orders, "final_position": position(lots), "cash_remaining": cash,
            "warnings": warnings}


def json_value(value):
    if isinstance(value, D):
        return format(value, "f")
    if isinstance(value, dict):
        return {key: json_value(item) for key, item in value.items()}
    if isinstance(value, list):
        return [json_value(item) for item in value]
    return value


def show(result):
    print("单股仓位策略测算（金额单位：元）")
    print(f"整笔风险预算：{result['risk_budget']:.2f}；假设止损成交价：{result['assumed_stop_execution_price']:.4f}")
    labels = {"risk": "风险额度", "budget": "投入额度", "cash": "现金",
              "no_initial_position": "底仓未能建立"}
    for item in result["orders"]:
        title = "底仓" if item["kind"] == "initial" else "加仓"
        print(f"\n{title}买入价：{item['price']}；允许买入：{item['shares']} 股")
        if item["status"] == "blocked":
            print("不可买入：" + "、".join(labels[key] for key in item["blocked_by"]) + "不足或不满足最小买入股数")
        else:
            print(f"需要现金（含买入费用）：{item['cash_required']:.2f}；占账户：{item['account_allocation_pct']:.2f}%；占计划投入：{item['budget_allocation_pct']:.2f}%")
    final = result["final_position"]
    if final:
        print(f"\n测算后持仓：{final['shares']} 股；成交均价：{final['average_price']:.4f}；买入支出（含费用）：{final['cost_including_buy_fees']:.2f}")
        print(f"风险占用：{final['risk_used']:.2f}；剩余风险：{final['risk_remaining']:.2f}；剩余现金：{result['cash_remaining']:.2f}")
        print(f"净利润目标：{final['target_profit']:.2f}；目标卖出成交价：{final['target_price']}")
        print(f"资金止损边界成交价：{final['loss_budget_execution_price']}；含滑点对应触发价：{final['loss_budget_trigger_price']}")
        print("资金止损边界仅表示亏损预算，不替代输入的策略失效价；所有价格均依赖费用与成交假设。")
    for warning in result["warnings"]:
        print("提示：" + warning)


try:
    args = parser.parse_args()
    if args.interactive or (len(sys.argv) == 1 and os.isatty(3)):
        interactive(args)
    validate(args)
    result = calculate(args)
    if args.json:
        print(json.dumps(json_value(result), ensure_ascii=False, indent=2))
    else:
        show(result)
except KeyboardInterrupt:
    print("\n已取消。", file=sys.stderr)
    sys.exit(130)
PY
