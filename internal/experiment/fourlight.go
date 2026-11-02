package experiment

import "errors"

func fourLightSpecification(rule string) (Spec, error) {
	labels := map[string][2]string{
		"all-red":  {"近似四灯 · 当前全红", "趋势、量能、中期、短期四盏近似灯在当前信号日全部为红；未知灯不参与通过。"},
		"flip-any": {"近似四灯 · 全红且至少一灯转红", "在当前四灯全红的基础上，至少有一盏灯相对上一交易日由绿转红。"},
		"flip-all": {"近似四灯 · 四灯同时转红", "四盏近似灯当前全红，且四盏都相对上一交易日由绿转红。"},
	}
	x, ok := labels[rule]
	if !ok {
		return Spec{}, errors.New("unknown four-light experiment policy")
	}
	return Spec{
		Title:         x[0],
		Direction:     "评价买入后五日内是否尽早出现连续性较强、回撤较浅的上涨波段；十日单独作为旁证",
		Hypothesis:    "在不使用未来行情的前提下，四灯近似共振可能过滤弱势信号；该研究信号只做 shadow 观察，不接管正式推荐，也不用于证明与目标软件对齐。",
		Change:        x[1] + " 本实验只评估后续选股旁证，不验证目标软件灯色是否一致；候选按原机会排序顺延，固定 Top N 槽位不足按现金 0。",
		Criteria:      "upward_wave_v0：T 日收盘信号，沿用下一交易日开盘成交及成本规则，买入日为 D1。主窗口 D1～D5，旁证 D1～D10；上涨段从 D1 或 D2 的上涨日开始，至少 3 个上涨日、上涨比例至少 75%、至少连续上涨 2 日，末端相对入场的扣费净涨幅至少为信号日 ATR14 百分比的 1 倍，入场至段末的最大盘中净浮亏和收盘最大回撤均不超过其 0.5 倍。固定使用最早达标段，完整路径不随旧止盈止损截断；缺数、除权或跨预留边界记 unknown。主报有效样本波段率、覆盖率及全部已入场分母的保守率，另外报告启动日、段长、效率、期末净标记与回吐。阈值是待验证研究定义；段末与峰值是事后标签，不代表可执行卖点或实现收益。旧交易风险单独披露；本轮不改变四灯公式、排名或正式推荐。",
		FourLightRule: rule,
	}, nil
}
