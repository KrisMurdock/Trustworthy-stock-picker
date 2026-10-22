# 日常算法接入契约

日常同步与算法的唯一调用边界是 `selection.Engine`：`Descriptor()` 描述版本、周期、复权口径及缺失处理，`Analyze(ctx, Input)` 只计算并返回统一 `selection.Report`。框架负责采集、覆盖率、保存、任务期限和通知。现有 `leader_daily_v1`、`short_horizon_v1` 适配原算法，未修改评分或收益计算。

新增 Go 实现时，在 `selection.DefaultEngine` 注册对应版本，并在 `config.Analysis.Validate` 中增加版本选择。生产配置 `analysis.engine` 明确选择版本；`auto` 兼容现有 `strategy.enabled`。CLI、数据源、存储和飞书无需了解新增计算代码。`Monitor.Engine` 可注入实现进行隔离测试；`engine_test.go` 演示最小虚拟算法。

首版契约接受已收盘日线，声明 `Periods: ["day"]`、`FinalOnly: true`、`MissingPolicy: "exclude"`，复权口径必须匹配输入。短周期内置引擎声明 `Periods: ["day", "30", "5"]`，接收同一 `FetchedAt` 的已完成、不复权 30 分钟与 5 分钟输入。存储层一次读取原生 5m 并派生 30m，引擎验证来源、复权、基础周期、`FetchedAt` 和聚合值一致；15m 只用于诊断，不进入生产契约。`structure_guard.mode` 默认 `shadow`：报告记录日线结构、30m/5m 否决证据和 `eligible_rank`，但不产生可执行推荐，只输出观察排名和结构通过排名；旧 5/15/30 只作诊断，不影响正式结论。切换 `enforce` 后，只有结构通过池内的 `eligible_rank` 能产生正式推荐。结构通过只表示“未被过滤”，不是第一、二、三类买点。尚无分钟记录时结构门控落 `unknown`，不会静默绕过；未知结构只进入观察排名。框架排除未完成及晚于报告日期的分钟线，并将实际使用的多周期窗口与日线一并冻结，重放不会读取当前分钟表。需要模拟成交时声明 `RawRequired`，逐股验证不复权数据配对。其他周期和未知缺失策略会明确拒绝，不能静默忽略算法需求。新报告为 schema 3；schema 1/2 旧报告继续只读，不迁移、不回写。独立分钟实验仍走 experiment 快照路径，实验结果不会自动进入日常推送。

结构门控的数据门槛按本地 365 自然日分析窗口设为：日线 240 根、30m 96 根、5m 240 根。日线不足、分钟不足、错日、错源、复权不一致或 30m 与 5m 不同快照时，`guard_status=unknown`；不会把未知当通过。

实验性 `rebound_signal`（`strategy.EvaluateReboundSignal`）只实现 `watch/armed/confirmed/failed/expired` 状态机，使用日线 qfq 区间、同一冻结快照中的不复权 30m/5m 完成 K 线，并按信号日 qfq/raw 收盘比显式换算价格尺度。它不接管正式推荐，也不得表述为第一、二、三类买点；当前尚未接入 experiment 的逐笔报告与回放，只是策略包内的实验原语，接入前必须补齐快照冻结、逐笔证据和独立样本设计。

输入含按分析窗口裁剪的行情、股池、交易日历和计算配置；框架复制输入，剥离路径、通知凭证和网络客户端。引擎必须主动检查 context，遵守超时／取消；必须返回相同 as_of、目标交易日、版本／复权口径，结果代码只能来自有效输入，不能重复。未知字段、额外周期或不兼容输出需升级契约，不能绕过校验。

## 第一层高召回初筛

`first_stage` 是日报内的独立候选池，不是正式推荐。它只使用信号日及以前的前复权日线，以“未来 3 个交易日最高价相对信号日收盘触及 +1%”为历史标签，将现有机会分与 8 个横截面 z 特征的 `logistic_z` 模型分分别转为日内中位秩百分位，再按 0.44/0.56 的权重做 Rank-Gauss 融合。完全同分的候选共享组内平均秩，单候选池使用中性百分位 0.5，不再由股票代码字典序决定百分位。模型参数以版本化 JSON 编译进二进制，启动时校验 schema、特征顺序、数值范围和 SHA-256；当前模型 artifact SHA-256 为 `8a833e8d124c94fdd1487ad614a01d263ea579f2859d641e4120b02fb34f9988`。生产进程不调用 Python、子进程或外部推理服务。

默认预算 95 只，飞书只展示综合分前 30 名并合并为一张卡片，第二层消费全部候选。2026-08-27 至 2026-10-15 的 30 个 point-in-time 信号窗口、104 只历史股池评估中，95 只候选的全候选平均召回为 94.28%（开发 20 窗口 94.26%，留出 10 窗口 94.32%），最差单窗口为 91.36%；Top 30 平均精确率为 80.67%，平均覆盖全池达标股票 33.08%。该结果来自“中位秩百分位 + Rank-Gauss 0.44/0.56”的融合算子；旧字典序 0.5/0.5 基线的平均召回为 94.06%，最差单窗口为 91.36%。同一窗口改用只使用当日以前已完成 3 日标签的 walk-forward 重训口径，95 只候选平均召回为 93.86%，最差单窗口为 89.19%。90 只候选的平均召回只有 90.30%，余量不足，因此没有继续压缩。评估按交易日截断特征，不使用未来 K 线，并按各版本 `leaders.yaml` 还原历史股池。104 只历史股池下 95 只候选的压缩率为 8.65%；2026-10-20 的 165 只股池实测 148 只可计算，95 只候选的压缩率为 35.81%。这些是历史回测统计，不是未来收益承诺。第一层不改变 `Selected`、`Ranking`、结构门控或风险门槛；初筛异常写入 `first_stage.status=failed`，主报告仍继续生成。`first_stage.push_enabled=true` 时，Top 30 卡片投递到 `first_stage` 路由通道；日报主卡和详情卡投递到 `selection` 路由通道，两类机器人按各自账本重试，互不阻塞。

信号日后第 3 个交易日日报生成后，`first_stage` 机器人会收到窗口回看卡。主口径与模型标签一致：统计信号日后 3 个交易日内最高价相对信号日收盘价的涨幅，达到 `+1%` 即命中。评价同时给出三个指标：全候选召回率为 `全部候选命中数 / 全池窗口达标数`；Top 30 覆盖率为 `Top 30 命中数 / 全池窗口达标数`；Top 30 精确率为 `Top 30 命中数 / 已评估 Top 30 数量`。飞书仍只展示 Top 30，第二层精排使用全部候选。期末收盘涨幅只作参考，不参与命中判断。回看卡只使用已保存的前复权日线，按 `信号日 + 窗口结束日` 生成稳定事件 ID，同一组日期不会重复入队；行情尚未完整时不推送，后续轮询会重试。回看股池从初筛报告的候选与排除项还原，不使用回看时的最新 `leaders.yaml`。

每份新报告保存 `input_id`、`revision`、算法描述、计算配置摘要与具体参数、构建版本。`replay -revision` 以同一构建恢复已冻结输入，独立于之后的复权修订、换源与同日重跑；它只计算，不保存或发送。若构建不同或旧报告缺少来源证据，明确拒绝精确重放，运维须取回记录的制品。配置变更或切换版本后重启；每个制品及数据版本都需保留可恢复备份。

## 第二层 Top10 精排

`second_stage` 只消费第一层已冻结的候选池，按“未来 3 个交易日最高价相对信号日收盘的最大涨幅”连续标签排序，默认输出 Top10。该标签用于排序研究，不是可成交收益；第二层也不改变 `selected`、正式推荐、结构门控、风险门槛或交易建议。

生产推理只运行编译进二进制的 Go 代码。离线阶段用本地 JEV-CPU、Laya official / multilingual 与 Chinese Decision 0.8B 对第一层候选按固定小组做 listwise 打分；当前生产 blend 使用相邻 5 只分组。生产进程不导入 Python、不启动子进程、不调用外部推理服务。模型只覆盖第一层全池，不能只看第一层 Top15，否则会丢失理想 Top10 中的大部分候选。
当 `second_stage.push_enabled=true` 时，精排完成后会基于同一份冻结报告生成公众号纯文本稿。稿件包含公众号标题、正文、Top N 名单、逐只技术特征解读、行业分布、数据口径、降级状态和风险提示，并投递到独立的 `second_stage` 飞书路由。文章是结构化报告的确定性转写，不调用 LLM，不重新计算排名；精排名单仍不等于可执行推荐。

每个离线模型的原始分数只在同一组内可比。2026-10-22 起，第二层生产主线改为“组先验局部校准 + 加权百分位 + ATR 风险惩罚”：

```text
within_m   = model_within_group_percentile
prior_g    = mean(first_stage_percentile within group g)
local_m    = clamp(prior_g + calibration_param × (within_m - 0.5), 0, 1)
weighted   = first_stage_weight × first_stage_percentile + Σ(model_weight × local_m)
blend_score = weighted - risk_eta × percentile(atr14_pct)
```

权重必须非负且总和为 1；模型权重必须大于 0。`first_stage_percentile` 是综合分在第一层候选池内的百分位，`within_m` 是模型原始分在同一相邻组内的百分位，两者都按“分数越高百分位越高、同分取中位百分位”计算。`atr14_pct` 的百分位在单日全候选池内计算。按最终 `blend_score` 降序取 TopN，同分按第一层 rank、代码排序。缺失模型仍使用中性 0.5 百分位并触发降级，不借用组先验掩盖 artifact 缺失。

生产计划为 `risk_all20_local035_eta1`：第一层 20%，`jev`、`laya_official`、`laya_multilingual`、`chinese_decision` 各 20%；`group_size=5`、`calibration=local`、`calibration_param=0.35`、`risk_eta=1.0`。报告写入 `ranker=blend_weighted_v1`、`blend.id`、`blend.models` 和上述参数；候选同时写入 `weighted_score`（扣风险惩罚前）、`atr_percentile`、`risk_penalty` 与最终 `blend_score`，每个模型写入原始组内 `percentile` 和校准后 `calibrated_percentile`。旧 `jev_*` 字段保留并镜像 `models.jev`。新增模型只需在计划中加入名称和权重、重分配其余权重使总和仍为 1，并提供同 schema 的 artifact，不需要修改排序代码。树模型仍加载并输出 `predicted_max_high_return_pct` 辅助字段，但不再决定顺序；树模型 JSON 继续校验 schema、特征顺序、树结构、数值范围和 SHA-256。

树模型 artifact 现在支持 `feature_version=1` 的 27 维冻结特征和 `feature_version=2` 的 31 维特征集。V2 在 v1 前缀后追加 `rank_jump_adjacent15`、`geo_blend_jev`、`harmonic_blend_jev`、`return_to_atr_ratio`；JEV 缺失时，三个教师交互项固定为中性 0，波动率调整动量仍独立计算。生产 `second_stage_gbdt_v1` 继续使用 v1 特征和旧 artifact，只输出辅助预测字段；V2 双目标候选已完整评估，但因验证指标未跑赢生产模型而未切换。

通用 artifact 路径为 `<data_dir>/state/secondstage/<model>/<as_of>.json`，schema 为 `schema_version: 1`，必须精确匹配信号日并且包含候选的 `adjacent15` 与 `full15_top12` 分数。`jev` 在通用路径不存在时兼容读取 `<data_dir>/state/jev/<as_of>.json`。某个模型缺失、过期或路径为空时，该模型对所有候选贡献中性 0.5 百分位，`degraded=true`，并在候选 `reasons` 标记 `model_missing:<model>`（`jev` 继续使用兼容原因 `jev_missing`）；部分候选缺失时只对缺失行使用中性 0.5 百分位。artifact 存在但 JSON/schema/日期/数值非法时标记对应模型状态为 `invalid`，同样降级而不是静默通过。

### 收盘后 artifact 生成与延迟发布

交易日 15:00 收盘后，Go 进程按 `daily.run_at` 同步当日行情，完成第一层初筛和主报告后即可先投递；精排不阻塞主报告。`deploy/stock-watcher-secondstage.timer` 在 15:00、15:05、15:10、15:15、15:20 触发离线生成服务，`scripts/secondstage/generate_daily.py` 等待当日冻结初筛报告，生成 `candidates_stage2_top95.json`、`dataset.json`，再并行运行 JEV、Laya official、Laya multilingual 和 Chinese Decision。四路全部成功后才把四份 artifact 原子发布到通用路径；任一失败不会留下部分发布。

Go 进程在 `second_stage.publish_by`（默认 15:28）之前每分钟重试读取 artifact：到齐后只基于冻结初筛重算精排、替换同日派生报告并投递精排稿，不重新下载行情或改变 `report_input`。到点时先做最后一次读取；仍未齐则释放中性降级结果并标记 `degraded=true`。Python 只存在于离线 artifact 生成链，生产 Go 二进制仍不启动 Python、子进程或外部推理服务。安装与排障命令见 [部署、升级与排障](deployment.md)。

### Laya 离线打分

`scripts/secondstage/laya_scores.py` 提供与 JEV 相同的离线 listwise 协议：按候选分组、对三种选项排列取平均，并输出相同 schema 的 `adjacent15` / `full15_top12` artifact。它支持两个 checkpoint：`official` 对应 `convaiinnovations/laya`（ModernBERT-large，英文官方版），`multilingual` 对应 `convaiinnovations/laya-multilingual`（mmBERT 多语言版）。两者使用完全相同的 prompt、分组和评分代码，便于在同一历史样本上比较。official checkpoint 自带 `max_len=512`，日报管线显式传入 `--max-len 4096 --head-max-len 256`，避免 5 人组状态被静默截断；截断响应会直接让该路生成失败，不会写入 artifact。

脚本默认按相邻 15 只分组；研究评估可在四个模型上统一使用 `--group-size 5`，以控制上下文长度和 CPU 开销，此时 artifact 的 `model.group_size` 与 `prompt_version` 会记录实际口径，但字段名仍保持 `adjacent15` / `full15_top12` 以兼容 Go 读取契约。Laya 概率未经校准，跨组不可直接比较；消费方必须先做组内百分位或等价归一化，不能把概率解释为成功概率、收益承诺或投资建议。

### Chinese Decision 离线打分

`scripts/secondstage/chinese_decision_scores.py` 适配 `quincyqiang/chinese-decision-0.8b`。该模型基于 Qwen3.5-0.8B，按官方协议把上下文和候选组织成中文 prompt，读取最后一个位置上候选项字母的 logits，使用 `decision_config.json` 中的温度校准后直接得到 choice 概率，不生成文本。适配器复用 Laya 脚本的分组、三种选项排列平均和 artifact schema，因此输出可以直接作为 `chinese_decision` 模型参与 Go 端 blend。

通过 `--model <local-path>` 指向已下载的模型目录，生产进程仍只读取 artifact，不依赖 PyTorch、Transformers 或 Python 子进程。该模型是面向通用中文选择、评分和是非判断的零样本决策模型，金融训练数据为 choice / score / noul 各 8,000 条，并不是股票专用模型；组内概率同样只用于排序归一化，不代表上涨概率、成功概率或收益承诺。

下载模型、研究训练模型、特征集和评估结果统一保存在共享盘的 `download_models/`、`trained_models/`、`build_features/` 和 `evaluations/` 根目录；阶段标识和当前路径见 [模型、特征与评估产物存储](model-storage.md)。脚本使用显式本地路径，不依赖共享盘内符号链接。

Go 生产进程仍只读取离线 artifact，不导入 Laya 或 Chinese Decision、不启动 Python 子进程；将同 schema artifact 写到通用模型路径并在 blend 计划中配置权重即可参与多模型融合。模型概率同样先做组内百分位归一化；树模型辅助预测仍按 JEV 分布训练，不能把 Laya、Chinese Decision 概率或树模型预测解释为校准后的收益。

离线验收以交易日等权的“Top10 平均未来三日最大高点涨幅”为主指标，`NDCG@10` 为辅助指标；同时记录 Top10 第三日收盘收益和 `+1%` 命中率作为风险护栏，并按交易日做 block bootstrap。历史结果只描述样本内/样本外排序表现，不是成功概率、收益承诺或投资建议。

### 主线 blend 验证与旧树模型留出

- 2026-10-22 起生产主线为 `ranker=blend_weighted_v1`，计划 `risk_all20_local035_eta1`。下列 2026-09-24 至 2026-10-14 的 9 个交易日保留了旧单模型与旧等权 blend 的历史对照（5 人组、同向且同分取中位百分位）；它们不是新生产计划本身的验证样本：

| 初筛排序器 | Hit@10 | NDCG@10 | MaxHigh@10 | Close3@10 |
|---|---:|---:|---:|---:|
| first-stage baseline | 63.3% | 0.638 | +3.47% | -6.44% |
| JEV-CPU | 58.9% | 0.573 | +3.17% | -5.11% |
| Laya official | 64.4% | 0.622 | +1.94% | -5.02% |
| Laya multilingual | 64.4% | 0.613 | +2.01% | -4.92% |
| Chinese Decision 0.8B | 62.2% | 0.627 | +2.48% | -3.64% |
| JEV 50/50 blend | 58.9% | 0.573 | +3.28% | -5.11% |
| Laya official 50/50 blend | 65.6% | 0.630 | +2.04% | -4.90% |
| Laya multilingual 50/50 blend | 64.4% | 0.613 | +2.15% | -4.84% |
| Chinese Decision 50/50 blend | 63.3% | 0.634 | +2.68% | -3.76% |
| all-model blend 50% + 4×12.5% | 63.3% | 0.619 | +2.60% | -4.83% |
| all-model blend 20% + 4×20% | 64.4% | 0.640 | +2.05% | -3.94% |
| 4-model equal, no first-stage | 61.1% | 0.592 | +2.04% | -3.60% |

- 精排阶段固定 first-stage Top95：

| 精排排序器 | Hit@10 | NDCG@10 | MaxHigh@10 | Close3@10 |
|---|---:|---:|---:|---:|
| first-stage baseline | 63.3% | 0.638 | +3.47% | -6.44% |
| JEV-CPU | 57.8% | 0.573 | +2.54% | -4.90% |
| Laya official | 56.7% | 0.573 | +3.08% | -4.79% |
| Laya multilingual | 57.8% | 0.585 | +3.09% | -4.77% |
| Chinese Decision 0.8B | 73.3% | 0.694 | +2.84% | -2.37% |
| JEV 50/50 blend | 61.1% | 0.594 | +3.04% | -5.60% |
| Laya official 50/50 blend | 56.7% | 0.571 | +3.10% | -5.76% |
| Laya multilingual 50/50 blend | 64.4% | 0.629 | +3.51% | -5.92% |
| Chinese Decision 50/50 blend | 67.8% | 0.651 | +2.76% | -3.67% |
| all-model blend 50% + 4×12.5% | 57.8% | 0.572 | +3.05% | -5.40% |
| all-model blend 20% + 4×20% | 63.3% | 0.596 | +3.39% | -3.86% |
| 4-model equal, no first-stage | 63.3% | 0.613 | +2.85% | -3.41% |

- 初筛阶段没有单一模型在所有指标上胜出：Laya 两版的 `Hit@10` 均为 64.4%，Laya official 50/50 融合为 65.6%，但 `MaxHigh@10` 都低于 first-stage baseline 的 +3.47%；Chinese Decision 的 `Close3@10` 最好（-3.64%），最大高点为 +2.48%。
- 精排阶段 Chinese Decision 单模型的 `Hit@10`、`NDCG@10` 和 `Close3@10` 都最好，分别为 73.3%、0.694、-2.37%，但 `MaxHigh@10` 只有 +2.84%；`MaxHigh@10` 最高的是 Laya multilingual 50/50（+3.51%）。命中率、NDCG、最大高点和收盘回撤并不指向同一个赢家，不能只用一个指标宣称全面胜出。
- 精排 Chinese Decision 对 JEV 的配对差值为：`Hit@10` +15.6 个百分点，95% CI `[+7.8, +25.6]`；`NDCG@10` +0.121，CI `[+0.002, +0.250]`；`MaxHigh@10` +0.30 个百分点，CI `[-0.67, +1.24]`；`Close3@10` +2.52 个百分点，CI `[+1.28, +3.72]`。回撤改善在 8/9 个交易日为正，但最大高点差异不显著。
- 50/50 融合的 Chinese Decision 对 JEV：`Hit@10` +6.7 个百分点，CI `[-5.6, +17.8]`；`NDCG@10` +0.057，CI `[-0.080, +0.198]`；`MaxHigh@10` -0.28 个百分点，CI `[-1.42, +0.69]`；`Close3@10` +1.92 个百分点，CI `[+1.09, +2.67]`。只有收盘回撤改善在这组样本中稳定，命中率增量仍不显著。
- 四个模型一起 blend 后，初筛命中率最高的全模型方案是第一层 20% + 四模型各 20%（64.4%），精排也是该方案最高（63.3%）；仍低于 Chinese Decision 单模型的 73.3% 和 Chinese Decision 50/50 的 67.8%。精排 first-stage 50% + 四模型各 12.5% 相对 Chinese Decision 50/50：`Hit@10` -10.0 个百分点，CI `[-15.6, -3.3]`；`Close3@10` -1.73 个百分点，CI `[-2.48, -0.95]`，两项都显著变差。全模型等权会稀释单一强信号，并非天然优于少模型融合。
- 两个 Laya checkpoint 的差异很小。精排 official 对 multilingual：`Hit@10` -1.1 个百分点，CI `[-3.3, 0.0]`；`NDCG@10` -0.012，CI `[-0.037, 0.000]`；`MaxHigh@10` -0.01 个百分点，CI `[-0.26, +0.22]`；`Close3@10` -0.01 个百分点，CI `[-0.15, +0.10]`。若只能选一个，精排可优先 multilingual，但不能声称有显著优势。
- 精排 Top10 特征均值显示偏好差异：first-stage baseline 的 ATR14 为 6.38%、5 日收益为 -1.11%；Laya official 为 5.49% / -3.31%；Laya multilingual 为 5.49% / -3.13%；Chinese Decision 为 3.61% / +1.99%。Chinese Decision 更偏向低波动、正短期动量的候选，这与它在下跌样本中回撤更小、最大高点弹性较低一致。
- 生产计划已由旧 `blend_50_50` 切换为 `risk_all20_local035_eta1`。该计划以测试期指标优先选择：开发期 `Hit@10=64.00%`、`NDCG@10=0.6589`、`MaxHigh@10=+3.026%`、`Close3@10=-3.563%`；历史测试日 2026-10-14 为 `80.00% / 0.7827 / +5.291% / -0.458%`；未参与选择的 10-15/10-16 后置留出均值为 `75.00% / 0.7290 / +2.424% / -4.897%`。测试期只有 1 个交易日，不能把这次“按测试期选择”表述为长期独立样本外验证。
- 上述对比只有 9 个交易日、处于下跌市、第一层模型训练覆盖该区间、四个模型统一使用 5 人组而非生产默认 15 人组，且未计入滑点、涨跌停、T+1、容量和交易成本；不能把结果外推为长期收益或生产 15 人组结论。

### 2026-10-15 至 2026-10-16 后置时间影子验证

在两日已有完整三日标签的信号日上补跑四模型，候选固定为 first-stage Top95（10-15 实际只有 58 只可分析候选），仍按 5 人组、三种选项排列和组内百分位评估。两日只保留 `Hit@10`、`NDCG@10`、`MaxHigh@10`、`Close3@10` 四项主指标；完整逐日结果与 Top10/Top30 组合保存在 `evaluations/reranking/chinese-decision-holdout-20261015-16/comparison.json`。

| 精排排序器 | Hit@10 | NDCG@10 | MaxHigh@10 | Close3@10 |
|---|---:|---:|---:|---:|
| first-stage baseline | 90.0% | 0.888 | +4.06% | -2.19% |
| JEV-CPU | 85.0% | 0.871 | +3.78% | -0.52% |
| Laya official | 80.0% | 0.831 | +3.72% | -0.54% |
| Laya multilingual | 85.0% | 0.874 | +3.95% | -0.09% |
| Chinese Decision 0.8B | 90.0% | 0.929 | +3.79% | -1.20% |
| JEV 50/50 blend | 85.0% | 0.866 | +3.58% | -1.80% |
| Laya official 50/50 blend | 80.0% | 0.834 | +3.81% | -1.89% |
| Laya multilingual 50/50 blend | 85.0% | 0.866 | +4.07% | -1.19% |
| Chinese Decision 50/50 blend | 90.0% | 0.930 | +4.28% | -1.41% |
| first-stage 30% + Chinese Decision 70% | 90.0% | 0.929 | +4.08% | -1.12% |
| first-stage 20% + Chinese Decision 80% | 90.0% | 0.929 | +3.79% | -1.20% |
| all-model blend 50% + 4×12.5% | 85.0% | 0.884 | +3.61% | -2.04% |
| all-model blend 20% + 4×20% | 85.0% | 0.859 | +3.90% | -1.60% |
| 4-model equal, no first-stage | 85.0% | 0.880 | +3.99% | -0.62% |

- Chinese Decision 单模型相对 first-stage baseline：`Hit@10` 持平，`NDCG@10` +0.041，`Close3@10` +0.99 个百分点，但 `MaxHigh@10` -0.27 个百分点。它仍是最稳的单模型之一，但不能表述为全指标全面胜出。
- Laya multilingual 相对 official：`Hit@10` +5.0 个百分点，`NDCG@10` +0.043，`MaxHigh@10` +0.23 个百分点，`Close3@10` +0.45 个百分点。结合 9 日结果，若两版只能选一个，优先 multilingual；两日样本仍不足以证明长期稳定优势。
- 四模型旧等权 blend 在两日样本中没有超过 Chinese Decision 单模型：`Hit@10` 低 5.0 个百分点，`NDCG@10` 低 0.045，`Close3@10` 低 0.83 个百分点。该旧结果说明“模型越多越好”不成立；新主线的 5 人组局部校准与 ATR 风险惩罚是在后续消融中单独选择，不能与本段旧等权结果混为一谈。
- 该验证不是独立长期样本外证明：first-stage 内嵌模型声明 `trained_through=2026-10-15`，10-15 信号日处于训练覆盖边缘；两个信号日的三日标签窗口重叠，不能做有意义的 bootstrap。生产计划按用户指定的“测试期指标优先”切换为 `risk_all20_local035_eta1`，但测试期只有 1 个交易日，仍需后续更长独立留出区间复核。

- 旧树模型 `second_stage_gbdt_v1`：80 棵深度 2 的回归树，27 个冻结特征，`max_features=0.4`，SHA-256 为 `21cb6c2a19706381cdb0ce2598ceda743b81227b22391cb5f92ba429d9d4e960`；现在只提供辅助预测字段。
- 训练区间 2026-09-03 至 2026-09-21，共 1885 行；只使用标签在 2026-09-28 验证起点前成熟的样本，避免训练标签跨入留出区间。
- 在更新后的中位秩 Rank-Gauss 第一层输入上，旧树模型 2026-09-28 至 2026-10-14 的 8 日验证 Top10 为 `MaxHigh@10=4.835%`、`NDCG@10=0.738`、`Close3@10=-4.851%`、`Hit1@10=71.25%`。任务书中的 4.29% / 0.360 / -6.12% / 71.2% 是旧字典序线性第一层输入下的历史结果，不能与当前输入组合混用。
- `FeatureVersion=2` 已在 Go 侧支持 4 个教师交互特征，并在 JEV 缺失时提供中性降级；2026-08-27 至 09-21 的 18 个训练日通过 purged walk-forward 选中 ATR 防守双头树，但 09-28 至 10-14 验证仅得到 `MaxHigh@10=3.859%`、`NDCG@10=0.707`、`Close3@10=-6.369%`、`Hit1@10=70.0%`，未通过预设门禁，也未跑赢现有生产树的 4.835% / 0.738 / -4.851% / 71.25%。因此生产 `second_stage_gbdt_v1` 和 artifact SHA-256 均未切换；V2 双头候选及完整报告归档在 Windows 训练目录。
- V2 候选的 Python 训练器与 Go `Model.Predict` 在 2660 行训练、验证和留出样本上逐行比对，最大绝对误差为 0，满足 `< 1e-9` 的导出契约。
- 按 3 交易日循环块、2000 次 bootstrap 比较 V2 ATR 防守模型与现有生产树：`MaxHigh@10` 差值 95% CI `[-2.075, -0.040]` 个百分点，`Close3@10` 差值 95% CI `[-2.967, -0.503]` 个百分点，均不支持切换。
- JEV 原始分数不能跨相邻组直接比较；旧树模型使用组内百分位、组内相对强度和 top12 组内占比，新主线同样先做组内百分位，而不是把 JEV 当作跨组概率。
- 留出区间只有 8 个交易日且处于下跌市，以上结果只描述该样本的排序表现，不外推为未来收益、成功概率或交易建议。

算法历史研究结果属于计算输出，框架修复不会把“证据不足”“验证失败”直接改成推荐通过。停牌未知缺口继续拒绝，真实停牌元数据接入需另行验证权威来源，不能靠放松完整性条件解决。
