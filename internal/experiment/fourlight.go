package experiment

import "errors"

func fourLightSpecification(rule string) (Spec, error) {
	labels := map[string][2]string{
		"all-red":  {"四灯代理 · 当前全红", "趋势、量能、中期、短期四盏代理灯在当前信号日全部为红；未知灯不参与通过。"},
		"flip-any": {"四灯代理 · 全红且至少一灯转红", "在当前四灯全红的基础上，至少有一盏灯相对上一交易日由绿转红。"},
		"flip-all": {"四灯代理 · 四灯同时转红", "四盏代理灯当前全红，且四盏都相对上一交易日由绿转红。"},
	}
	x, ok := labels[rule]
	if !ok {
		return Spec{}, errors.New("unknown four-light experiment policy")
	}
	return Spec{
		Title:         x[0],
		Direction:     "观察四灯状态机是否提高现有五日交易模型的命中与净收益",
		Hypothesis:    "在不使用未来行情和资金流字段的前提下，四灯代理共振可能过滤弱势信号；该研究信号只做 shadow 观察，不接管正式推荐。",
		Change:        x[1] + " 资金流字段未接入，因此本实验只能验证价量四灯代理；候选按原机会排序顺延，固定 Top N 槽位不足按现金 0。",
		Criteria:      "信号仅使用 T 日已完成前复权日线，T+1 按现有开盘、跳空、成交和涨跌停规则模拟买入；买入日不卖出，沿用现有止盈、止损、费用、滑点与最长持有期限。比较相对 baseline 的样本数、信号日数、平均净收益、目标率、最差单笔、跳过成交数和 unknown 比例。四灯不改变 Recommended、Selected、eligible_rank、机会分或结构门控；只有固定开发/留出区间验证通过后，才讨论接入正式排序。历史统计不代表未来收益。",
		FourLightRule: rule,
	}, nil
}
