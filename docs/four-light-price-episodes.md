# 价格全红段内首次与再次触发研究

`four-light-study -price-episodes` 增加 `price_episodes` 报告与事件中的 `price_episode`，同时包含既有 `trigger_timing`。需要manifest v2及冻结行业分类；基础report、sector_context、trigger_timing和原事件字段保持一致，不改变四灯、未来波段或正式选股。

## 价格段与事件

策略标识为 `four_light_price_episode_v1`。逐股票沿完整冻结交易日扫描所有可用历史到信号日，每日用原始前复权输入及完整前缀计算 `four_light_approx_v1`。价格段是趋势、中期、短期三灯共同红的连续交易日段；不在研究起点、月初或旧灯龄60日边界重置。

- 任一价格灯已知绿即可确认结束；其他灯未知不妨碍这个结论。
- 缺交易日或价格灯未知切断可确认连续性，后续红段起点标为截断。不能承接缺口前的全红来判再次。
- 首个可计算日已经价格全红，不能证明该日是段起点。记录 `observed_start_date`；只有此前紧邻交易日已知价格非全红，才给出 `start_date` 和 `start_known=true`。
- 量能灯未知不切价格段，但可能隐藏全红或转换，因此触发次数和最近转换日期分别保留不确定性。

只对原 `first_all_red`（前一交易日四灯均可判且非全红，当前四灯全红）划分三个互斥组：

| 组 | 当时可用证据 |
| --- | --- |
| episode_first | 段起点已知，此前段内状态完整、未出现全红 |
| episode_repeat | 同一可确认连续价格红段内，此前已经观察到任意全红 |
| episode_unknown | 既不能确认首次，也未观察到同段更早全红 |

此前全红的转换未知，不妨碍今后确认再次；但不能把它补造为原首次全红事件。因此 `prior_all_red_observed=true` 与 `prior_confirmed_triggers=0` 可以同时成立。未知不会因未来成功、后来又亮灯或该段最终长度而重新分组。

`prior_confirmed_triggers` 只计当前之前的确定转换；`exact_prior_triggers` 在历史不完整时为null。`last_observed_trigger_date` 是此前最后一个观测到的确定转换日，不能用最近全红状态日替代。只有当前是确定转换、且此前最近转换仍可确定，才给 `trigger_gap_sessions`。累计次数失去精确性后不在同段恢复；后续新确定转换可以恢复最近转换日期的可靠性。

## 评价

首次／再次／未知完整分解原首次全红的selected、入场及未来窗口分母；每个已知价格段最多保留一次episode_first。保留所有原事件，不根据前一次未来是否成功、是否仍在持仓或5/10日窗口是否结束筛选。

五日为主、十日为辅，继续使用 `upward_wave_no_drawdown_v1`，不增加回撤门槛。报告包括全期、逐日、逐月、同日市场、同日所有首次全红、再次相对段内首次，以及同日同行业自排除对照。后者保持至少5个其他有效窗口、按候选行业构成加权、日期等权；缺少共同日期时不给差值。

上轮6+仅按这三个组拆解，核对重复构成；不新增灯龄、量比或价格路径组合门槛。不同价格段的未来窗口仍可能重叠，再次组也会重复评价同一上涨行情；统计不是独立交易或组合收益。

## 运行与失败边界

```bash
four-light-study -manifest manifest.json -database all-market.frozen.db \
  -classification classification.json -classification-sha256 <冻结摘要> \
  -price-episodes -events episode-events.jsonl -timeout 1h > episode-report.json
```

省略参数时沿用原行为。缺分类/摘要、旧manifest、取消、超时、来源或输出错误不返回部分成功报告；事件采用原子发布且禁止覆盖输入和既有文件。无账号、角色、HTTP或网页变化。使用只读数据库，价格读取止于manifest观察上限，预留区间继续封存。

本轮仍使用已反复观察的开发数据。行业分类滞后、活跃股池存活偏差、事后复权和多次比较限制不因增加分组而消失；月度方向和7月必须保留，不能宣称独立验证或因果入场优势。
