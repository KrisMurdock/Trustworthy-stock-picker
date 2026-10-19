package experiment

import (
	"errors"
	"stock-watcher/internal/strategy"
)

func conditionalSpecification(rule string) (Spec, error) {
	spec := Spec{Title: "条件低吸 · 买入日浮盈与次日止盈", Direction: "第二天到价反弹买入，第三天触达净盈利2.5%／3%", Hypothesis: "预先确定低吸区间，在当时已确认的反弹后入场，检验买入日收盘扣成本浮盈为正以及第三天止盈的联合成功率。", Change: "信号日原始收盘价下方0.5%～2%为区间；完成5分钟收盘回到区间且从当时已知日内低点反弹≥0.3%，下一根5分钟开盘含滑点仍在区间才买，最迟14:30；此前5分钟收盘跌破下沿或首次触发后开盘跳出区间则放弃。候选在前一日固定，不按未来触发或盈亏补名额。", Criteria: "每笔净亏损严格<3%；买入日收盘估值扣买入成本、预留卖出税费及滑点后>0，非当日涨幅为正。第三天仅计退出前最高价净触达2.5%／3%，联合成功率争取≥80%，分母为全部入场（含未决与买入日亏损）。至少30笔完成和10个信号日，缺失分钟和未决不按安全处理；不保证未来收盘或实盘收益。未指定区间时保留最新3个分钟交易日；显式区间不声明独立留出，短历史仅固定规则试验。", NextDayRule: "baseline", EntryMode: strategy.ConditionalEntryMode}
	if rule == "" {
		return spec, nil
	}
	if rule == strategy.ChanSignalRule {
		spec.Title = "条件低吸 · 5/15/30 分钟缠论共振"
		spec.Direction = "检验多级别底分型确认与中枢未破位是否改善低吸入场质量"
		spec.Hypothesis = "用固定、可复现的缠论结构过滤原机会排序：30 分钟保持中枢下沿之上或最近完成笔向上，15 分钟最近分型为已确认底分型且被收盘收复，5 分钟最近分型为已确认底分型且被收盘收复，再进入原有低吸路径。"
		spec.SignalRule = rule
		spec.Change = "信号日只读取截至 15:00 的已完成 5 分钟 K 线；15/30 分钟由这些 5 分钟线逐日聚合，不保存独立派生表。包含关系按前两根合并 K 线方向处理，分型要求三根合并 K 线高低点严格成立；笔固定最少 6 根合并 K 线，中枢由至少 3 笔重叠区间构成。30m 有中枢时要求收盘不低于下沿，没有中枢时要求最近完成笔向上；15m 与 5m 均要求最近分型为底分型且收盘收复其高点。结构缺失或样本不足记为 unknown，不当作通过。" + spec.Change
		spec.Criteria = "所有级别必须通过才进入原 Top N 排序；被过滤的原 Top N 保留逐级状态、最近笔方向、分型位置和中枢上下沿。任一必要级别缺失、信号日 5 分钟路径不完整或尚无有效结构时不补选。沿用原有买入日浮盈、次日 2.5%／3% 触达、净亏损严格<3%及样本门槛。" + spec.Criteria
		return spec, nil
	}
	labels := map[string][2]string{
		"rsi":              {"RSI 回升", "Wilder RSI14 为40～65，且高于前一交易日。"},
		"macd":             {"MACD 增强", "DIF>DEA，且2×(DIF−DEA)高于前一交易日；EMA12/26，信号EMA9，沿用SMA初始化。"},
		"bollinger":        {"布林线修复", "20日SMA±2倍总体标准差，%B为0.1～0.6且高于前一交易日；带宽为零不通过。"},
		"confluence":       {"两项指标共振", "RSI回升、MACD增强、布林线修复三项中至少满足两项。"},
		"all-three":        {"三项指标共振", "RSI回升、MACD增强、布林线修复三项全部满足。"},
		"guard":            {"波动与量能约束", "ATR14≤3%，距MA20在−5%～5%，量比为0.8～2.5。"},
		"confluence-guard": {"两项共振加波动量能约束", "至少两项指标满足，且ATR14≤3%、距MA20在−5%～5%、量比为0.8～2.5。"},
	}
	label, ok := labels[rule]
	if !ok {
		return Spec{}, errors.New("unknown conditional indicator policy")
	}
	spec.Title = "条件低吸 · " + label[0]
	spec.Direction = "检验" + label[0] + "是否改善买入日浮盈与次日联合触达"
	spec.Hypothesis = "只改变信号日指标筛选，检验能否减少买入日负浮盈及亏损超限，并提高买入日为正且次日净触达2.5%／3%的联合比例；同时记录减少或替换的样本。"
	spec.SignalRule = rule
	spec.Change = "信号日先过滤，再按原机会排序取前N；全部候选仅使用截至信号日的前复权日线。" + label[1] + " 共振子条件固定为：RSI14在40～65且回升；DIF>DEA且柱增强；布林%B在0.1～0.6且回升。成交仍用不复权价格。" + spec.Change
	return spec, nil
}
