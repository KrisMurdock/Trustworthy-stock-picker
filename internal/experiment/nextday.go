package experiment

import "errors"

func nextDaySpecification(rule string) (Spec, error) {
	labels := map[string][2]string{
		"baseline":        {"次日退出基线", "保留现有机会排序，仅把最长持有期限改为买入后第 1 个可卖出交易日。"},
		"upper-wick":      {"次日触达 · 上影线过滤", "信号日上影线占振幅 ≤ 30%，按原排序顺延选入。"},
		"low-volatility":  {"次日触达 · 低波动过滤", "信号日 ATR14 / 收盘价 ≤ 2%，检验降低波动能否减少超限，以及是否同时损失触达机会。"},
		"pullback":        {"次日触达 · 趋势回踩", "MA20 上行，距 MA20 为 -1% 至 3%，当日上涨、收盘位置 ≥ 60%，量比 ≤ 1.5。"},
		"breakout":        {"次日触达 · 放量突破", "距前 20 日最高价 -0.5% 至 3%，量比 ≥ 1.2、收盘位置 ≥ 65%、上影线 ≤ 30%，MA20 上行。"},
		"momentum":        {"次日触达 · 相对动量", "MA5 > MA10 > MA20，5 日涨幅为 0% 至 10%，强于当日股池，RSI14 为 50 至 75。"},
		"volume-recovery": {"次日触达 · 放量修复", "量比 1.2 至 2.5，当日涨幅 0% 至 4%，收盘位置 ≥ 70%，距 MA20 为 -2% 至 8%。"},
		"balanced":        {"次日触达 · 波动与趋势组合", "ATR14 ≤ 3%、上影线 ≤ 30%、MA20 上行、5 日涨幅 ≤ 10%、距 MA20 ≤ 8%。"},
		"risk-qualified":  {"次日触达 · 历史风险证据", "只选信号当时已成熟的次日历史证据通过推荐门槛的候选；无证据则空仓。"},
	}
	x, ok := labels[rule]
	if !ok {
		return Spec{}, errors.New("unknown next-day experiment policy")
	}
	return Spec{
		Title: x[0], Direction: "买入次日触达 2.5%／3%，争取 80% 成功率",
		Hypothesis:  "只用信号日已知指标筛选，检验能否提高买入后第一个可卖出交易日的最高价净盈利触达率，同时避免单笔净亏损达到 3%。",
		Change:      x[1] + " 首个可卖出日未止损或止盈则按收盘模拟退出；无法卖出的交易保留未决。",
		Criteria:    "硬条件：每笔投入扣费后净亏损严格 <3%，未决不能算达标。时间：只计买入后的第 1 个可卖出交易日（不使用持仓<1，不包含买入当日），最高价扣费及滑点后触达 2.5%／3% 分别统计，不要求收盘达标。争取两档成功率各 ≥80%，分母包括全部入场及未决；先后不明不算成功。至少 30 笔完成、10 个信号日且实际平均净收益为正；开发期达到目标也须冻结规则并独立验证，不能保证未来胜率。",
		NextDayRule: rule, RequireEvidence: rule == "risk-qualified",
	}, nil
}
