# 日内高低价预测评估指标

`internal/dayrange` 只评估已经冻结的预测和已经核对的实际结果，不训练模型、不读取数据库、不下载行情。盘前、盘中剩余时段和盘中全天预测必须使用不同的 `mode`，不同预测截点也不会混合统计。

## 输入口径

每条 `Observation` 包含股票、目标交易日、预测生成时间、预测模式、参考价、可选历史波动尺度、最高/最低价预测、实际高低价和可选的同样本基线。价格统一使用不复权口径；`Actual=nil` 表示窗口尚未核对或数据不可用，必须带 `UnresolvedReason`，不能当作零误差。

`premarket` 要求预测时间早于 09:30；`remaining` 和 `full_day` 要求已结束的 5 分钟边界，且不能使用收盘后的数据。调用方负责保证 `remaining` 的实际值只覆盖预测时点之后的剩余交易时段。

## 指标

价格空间使用 CNY，收益空间使用相对 `ReferencePrice` 的基点（bps）。主聚合为 `day_equal`：先在每个交易日内对股票等权，再对交易日等权。`pooled` 只用于辅助对照，不能替代主结论。

- `point.mae`、`point.rmse`、`point.bias`：分别评价最高价和最低价。bias 保留正负号，MAE/RMSE 为绝对误差。
- `point.skill_mae`、`point.skill_rmse`：`1 - model_error / baseline_error`，只在同一股票、日期、模式、截点和参考价存在基线时计算；基线误差为零时为 `not_applicable`。
- `point.mae` + `normalization=volatility`：以预测时已知的历史波动尺度归一化误差。
- `interval.empirical_coverage`：实际极值落入各自区间的比例。
- `interval.joint_coverage`：最高价和最低价同时落入各自区间的比例。它不等于两个边际覆盖率的平均值。
- `interval.envelope_coverage`：实际最低价不低于低价区间下界，且实际最高价不高于高价区间上界。它用于诊断整个外包络是否覆盖，不替代联合覆盖率。
- `interval.width_mean`：区间平均宽度；`interval.score`：Winkler 区间评分，区间越宽或突破越远，分数越差。
- `interval.lower_breach_rate`、`interval.upper_breach_rate`：下界、上界突破比例；`interval.lower_exceedance_mean`、`interval.upper_exceedance_mean`：突破时的平均价格距离。
- `interval.pinball_loss`：每个预测分位数的分位数损失。分位数和区间分别保存，不能把区间覆盖率伪装成分位数准确率。

价格与收益空间均保留；收益空间用于跨价位股票比较，价格空间用于实际价格误差展示。所有数值都必须有限。指标没有有效样本时使用 `insufficient_data` 或 `not_applicable` 和 `null`，不写零。

## 置信区间

`day_equal` 指标使用固定种子的按交易日整块 bootstrap，重采样单位是交易日，不是股票；少于10个已核对交易日时标记 `insufficient_data`。同一交易日复制更多股票不会增加置信区间的独立样本数。

## 结果使用

模型比较必须使用相同股票、日期、预测模式、截点、参考价和实际值的配对样本。区间模型同时报告高价边际覆盖、低价边际覆盖、联合覆盖和区间宽度/评分；只提高覆盖率但显著扩大区间不算改进。指标仅描述历史样本外表现，不代表未来价格或收益保证。
