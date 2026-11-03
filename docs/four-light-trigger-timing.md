# 首次全红触发时点诊断

`four-light-study -trigger-timing` 在原全市场研究上增加独立的 `trigger_timing` 报告，要求manifest v2与冻结行业分类。基础 `report` 和 `sector_context` 完整保持原样；事件增加 `trigger_context`，原灯色、生命周期、成交与未来波段不变。功能只用于研究，不接入正式排名或推荐，不增加回撤阈值。

## 信号日前特征

`four_light_trigger_timing_v1` 以信号日为t，逐灯统计截至t−1的连续红灯交易日数，同时统计趋势、中期、短期三盏价格灯共同全红的连续日数。每个历史状态重新使用冻结的 `four_light_approx_v1` 及该日完整价格前缀，不把信号日算作已等待的一天。

最多向前回看60个交易日，但每个被评价的历史日仍保留自身公式所需的60根预热，不能把原始行情也截成60根。日历缺日、历史不足或对应灯色未知会截断计数；量能灯未知不会自动破坏价格三灯共同红龄。

每项计数保存 `exact_days`、`observed_days`、`censored` 和原因。只有观察到一盏相应绿灯，才能确认红串的精确长度；否则保存下界，不将左截断或缺日当作绿灯。价格三灯中一盏已知绿即可证明此前不全红。

首次全红按价格三灯红龄分为0、1～2、3～5、6+和unknown。前三组需要精确终点，6+可以由至少6天的下界证明。非首次全红不加入这些候选。正年龄天然对应量能补红，因此报告同时比较同日 `first_energy_only`，避免把价格灯触发与量能补红的构成差异误当等待时间效果。

另一独立维度为截至t−1的5日和20日收盘收益：

```text
prior5  = C[t−1] / C[t−6]  − 1
prior20 = C[t−1] / C[t−21] − 1
```

要求t−21到t共22个冻结交易日完整存在，前21根收盘价有效；t仅验证日期存在，不参与收益或先验价格有效性。按20日和5日收益正/非正组成四组，零归非正，缺失另列unknown。两个窗口重叠，这只是价格路径描述，不能据此认定严格反转、上涨启动或回调反抽。信号日涨跌单独描述，不参与四象限。

灯龄与价格路径分别统计，不交叉组合，不与量比拼成过滤条件。触发标签 `price_preexisting`、`energy_preexisting`、`multi_light_trigger` 可以重叠，不能相加当作互斥事件。

## 评价与边界

- 五日波段为主，十日独立报告，继续使用 `upward_wave_no_drawdown_v1`。
- 所有首次全红保留原selected；年龄分组及路径分组各自加总回全部首次全红，unknown保留完整入场与路径漏斗。未来是否完整、是否成功不得决定分组。
- 输出全期、逐日、逐月；同时比较市场、首次全红，以及正年龄组相对量能补红。同行业匹配复用自排除、按候选有效样本行业构成加权、日期等权及至少5个其他已知窗口的口径。
- 缺行业、同行业参照不足及未配对日期单列；不能直接相减不同日期集合的差值。
- 全部窗口严格在原观察上限内，预留区间不读取。分组在本轮结果生成前冻结，但原开发期已反复观察，本轮仍是探索；移动块区间不是独立验证，也未作多重比较修正。

## 运行

```bash
four-light-study -manifest manifest.json -database all-market.frozen.db \
  -classification classification.json -classification-sha256 <冻结摘要> \
  -trigger-timing -events trigger-events.jsonl -timeout 1h > trigger-report.json
```

省略新参数时保持原行为。输入、摘要、版本、错误、取消、超时及事件原子发布沿用[全市场研究](four-light-study.md)和[行业对照](four-light-sector-context.md)的契约。工具离线运行，不修改行情、分类或实验档案。

价格三灯连续全红段内的首次与再次触发诊断见[价格段研究](four-light-price-episodes.md)，通过 `-price-episodes` 单独启用。
