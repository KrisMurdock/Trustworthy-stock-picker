# 5 分钟价格预测

本模块针对当日日报的正式推荐或回退观察排名，使用本地已保存的 5 分钟线和日线训练树模型，滚动预测下一根 5 分钟 K 线的高点、低点和收盘价，并把结果聚合成飞书观察卡。它不接券商、不维护持仓、不下单，也不构成投资建议。

## 1. 边界

- 交易由用户手动完成；系统只输出预测。
- 预测对象优先是日报中 `target_trading_date=今天` 的 `Selected`；严格推荐为空时，回退到同一报告的 Top N 观察排名。没有有效日报或两者都为空时，不预测、不推送。
- 回退只使用当日目标报告已有的观察排名，不读取旧报告，也不扩大到全股池。
- 预测是点估计，不是保证区间；卡片只展示主指标、护栏指标和样本数，完整指标列表由评估或状态输出提供。

## 2. 预测目标与时间

对每只入选标的，在输入 5 分钟 bar 已完成（`final=1`）后，预测下一根同交易时段 5 分钟 bar：

| 目标 | 含义 |
| --- | --- |
| `high` | 下一根 bar 最高价 / 当前 bar 收盘价 - 1 |
| `low` | 下一根 bar 最低价 / 当前 bar 收盘价 - 1 |
| `close` | 下一根 bar 收盘价 / 当前 bar 收盘价 - 1 |

时间边界：

- 09:30 开盘不预测；09:35 完成后预测 09:40。
- 11:30 不预测，下一根是 13:05；13:05 完成后预测 13:10。
- 14:55 完成后预测 15:00；15:00 之后当天不再预测。
- 延迟、缺口、非 final bar 或目标 bar 已过期时跳过本轮，不补发旧预测。
- 预测结果按完成 bar 即时落库并推送；上一根实际 K 线确认后，再单独推送偏差对比卡。

## 3. 模型

- 跨股票池 pooled，按目标拆成 high/low/close 三个 GBDT；每个 GBDT 是多棵回归树。
- 15/30/60 分钟只作为特征尺度，不单独训练四个模型，也不直接平均不同期限的价格。
- 纯 Go 训练和推理，固定种子、固定特征顺序和稳定 tie-break；模型 artifact 可确定性重放。
- 特征包括：多尺度收益/振幅/量比/波动率、收盘位置、影线、MA/RSI、时段、股池横截面宽度、上一交易日 qfq 日线上下文和缺失指示。
- 标签只取同交易时段、正好 +5 分钟的下一根 bar；11:30 和 15:00 不作为训练输入。
- 生产模型当前仍使用单一训练区间：读取最近 400 个自然日的 5 分钟线，用截止日全量样本重训；评估输出并展示 walk-forward 指标，卡片只保留主指标与护栏指标。
- `predictor-eval` 提供离线的多训练窗口 rolling walk-forward 评估；它不改变生产模型权重，也不写入模型、预测库或飞书。

### 3.1 close 使用 L1 损失

- 主指标是 MAE，因此生产训练对 close 使用 L1 损失，对 high/low 继续使用 L2。L1 的初始值和叶值取残差中位数；high/low 保持均值和平方误差分裂，避免压缩包络。
- 2026-10-22 的 16 日 `config-diagnostic` 诊断中，仅 close 使用 L1 使 close MAE 从 `25.9211` 降到 `25.7643` bps（-0.1568 bps，95% CI `[-0.2990, -0.0319]`，12/16 天改善），方向准确率从 `52.34%` 升到 `54.26%`；high/low MAE、包络覆盖率和区间宽度不变。
- 全目标 L1 虽然进一步降低三目标 MAE，但把 high/low 包络覆盖率从 `22.67%` 压到 `12.19%`，并产生 200 条内部矛盾，因此不采用。详见 [close L1 损失评估](predictor-l1-loss-evaluation.md)。
- `ProductionTrainOptions` 固定 `close_loss=l1`；离线评估默认仍是 `l2`，可用 `-close-loss l1` 复现生产候选，用 `-loss l1` 复现全目标对照。把 `CloseLoss` 设为空可回退到原行为。

## 4. 卡尔曼滤波

- 每只股票、每个交易日维护一个局部线性趋势状态，观测为已完成 bar 的收盘价。
- 卡尔曼只输出趋势状态（偏强/震荡/偏弱）和置信度，不改写 high/low/close 点预测。
- 状态落 `predictions.db` 的 `kalman_state`，重启后从当天最新状态继续。

## 5. 训练与模型生命周期

- 默认每个交易日 15:30 训练下一交易日模型；启动时若没有 `valid_for=今天` 的模型，开盘前补训一次。
- 模型只对下一个交易日有效；过期、缺失或训练失败时不预测、不推送。
- 训练与预测使用同一标的集合：优先正式 `Selected`，为空时使用当日报告的 Top N 观察排名。
- 训练必须运行在持有数据目录写锁的 `run` 进程内。`train-predictor` CLI 只能在 `run` 停止时用于离线训练或排查。
- 模型文件、预测库、状态和开关全部位于 `<data_dir>`（生产为 CIFS），不回退到本地目录。
- 模型 artifact 使用 JSON，受 `storage.Store` 的 8 MiB 文档上限约束；当前模型预计远小于该上限。
- 模型 artifact 与 `predictor-eval` 输出使用 schema 2；schema 1 不再兼容，升级前需保留完整备份。
- `loss`/`close_loss` 是 artifact 的可选元数据；缺失时按 L2 处理。新 artifact 会记录生产实际使用的目标损失，便于审计。artifact 使用严格解码，回滚到旧二进制时需同时恢复旧 artifact 或完整数据备份。

目录布局：

```text
<data_dir>/
├── predictions.db
├── models/intraday/
│   ├── current.json
│   └── <model_id>/
│       ├── model.json
│       └── metadata.json
└── state/
    ├── predictor.json
    ├── predictor-status.json
    └── predictor-delivery.json
```

`predictions.db` schema 3 保留以下表：

- `model_run`：模型版本、训练区间、有效期、指标和 checksum；升级时清理旧 schema 记录，表结构继续服务新模型。
- `prediction`：每只股票、每个目标 bar 的不可变预测。
- `prediction_actual`：目标 bar 到达后的实际值和误差状态。
- `kalman_state`：每只股票、每个交易日的趋势滤波状态。

升级不会删除 `prediction`、`prediction_actual` 的预测审计记录，也不会重建 `kalman_state`。

## 6. 推送

- 每个目标 bar 一张实时预测卡，默认包含当前已完成的推荐票预测；预测完成后立即入队，不等待上一根实际值。
- 预测卡头部使用“5 分钟价格预测”标题、预测窗口副标题和图表图标；顶部摘要展示预测窗口和标的数。
- 主表为名称、现价、预测高、预测低、预测收、信号（趋势 · 置信度）；价格后附相对百分比，数字列右对齐。
- 预测卡不嵌入上一根实际对比。上一根实际高/低/收与预测值到齐后，单独生成一张“上轮预测偏差”卡：顶部摘要展示对比窗口、标的数、已确认数和平均收盘误差，表格逐股展示实际高/低/收以及相对该根预测的价差和百分点偏差。
- 预测卡与偏差卡的模型状态区使用字段网格展示模型版本、训练截止日、样本数、主指标和护栏指标，并单独列出基线、风险提示和实验性声明。
- 主指标未优于基线、护栏指标越界或可靠性为 `insufficient_data` 时显示警告，但仍按用户选择推送。
- 预测卡事件 ID 为 `intraday:<date>:<target_end_unix>`；偏差卡事件 ID 为 `intraday:<date>:<target_end_unix>:actual`。预测卡过期时间为目标 bar 结束时间；偏差卡在当前目标 bar 结束前有效。
- 偏差卡只有在对应预测卡已入队、全部入选标的的上一根实际值已确认后才入队，并使用更高的投递序号排在预测卡之后。
- 预测卡和偏差卡只投递到 `predictor_realtime` 路由通道；未设置路由的旧单通道配置仍接收全部阶段。
- 关闭推送只停飞书投递；预测继续落库，网页仍可查看。

## 7. 网页与接口

- `GET /api/predictions?date=YYYY-MM-DD`：按日期返回已保存的预测记录；实际值和对账状态保存在 `predictions.db` 供诊断。
- `GET /api/predictor`：返回启用状态、推送开关、模型版本/有效期、最近预测和错误。
- `PUT /api/predictor/config`：`{"push_enabled": true|false}`。默认只允许 loopback、同源和带 `X-Stock-Watcher-Action: predictor-config` 的请求；`predictor.write_mode=off` 时禁止网页写入。
- `/rankings` 页展示最新预测区块和推送开关；接口和页面均为只读/受控写，不提供交易下单。

## 8. 配置

内置生产配置默认开启预测和飞书推送；如需临时关闭：

```bash
STOCK_WATCHER_PREDICTOR_ENABLED=false
# 或只关闭飞书、保留预测落库和网页展示
STOCK_WATCHER_PREDICTOR_PUSH_ENABLED=false
```

可选：

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `STOCK_WATCHER_PREDICTOR_TRAIN_AT` | `15:30` | 收盘后训练时间，15:10～23:00 |
| `STOCK_WATCHER_PREDICTOR_WRITE_MODE` | `loopback` | 网页开关写权限：off/loopback/all |
| `STOCK_WATCHER_PREDICTOR_MIN_TRAINING_DAYS` | `5` | 最少训练交易日 |
| `STOCK_WATCHER_PREDICTOR_VALIDATION_DAYS` | `3` | walk-forward 验证交易日 |
| `STOCK_WATCHER_PREDICTOR_KEEP_MODELS` | `10` | 保留模型代数 |

## 9. 失败行为

- 模型缺失/过期/`valid_for` 不是今天：不预测、不推送，`health` 显示原因。
- 日报缺失、`Selected` 与观察排名都为空、分钟 bar 未 final、目标时段不合法：本轮跳过。
- 任一入选标的缺少上一根实际值：预测卡照常发送，偏差卡继续等待；当前目标 bar 结束前仍不齐则本轮偏差卡不发送。
- CIFS 不可用：写入停止，不落本地目录，不发送旧预测。
- 飞书发送失败：沿用投递账本的重试/过期语义；超过目标 bar 结束时间即过期。
- 训练失败：保留失败原因；开盘前补训；仍无有效模型时不推送。

## 10. 评估指标契约

预测评估以统一指标记录表达，模型 artifact 和 `predictor-eval` 输出使用 schema 2。指标记录保持扁平，不按指标族嵌套；每条记录使用稳定、唯一的 `metric_id`，指标集合按稳定顺序输出为规范化 JSON 列表，并由 Go helper 类型统一构造、校验和读取，避免不同入口自行解释字段或默认值。

五类指标：

| 指标族 | 内容 | 适用状态 |
| --- | --- | --- |
| `point` | 点预测误差、偏差和 high/low 包络诊断 | 本轮主评估 |
| `direction` | close 方向命中，high/low 条件诊断 | 本轮主评估与诊断 |
| `interval` | 区间经验覆盖率、宽度、pinball loss 和 CRPS | 仅定义 schema；point-only 模型为 `not_applicable` |
| `ranking` | 横截面排序质量 | 预留；预测器为 `not_applicable` |
| `trading` | 交易收益、回撤等 | 预留；预测器为 `not_applicable` |

每条指标同时记录以下正交维度，不能把维度信息塞进 `metric_id` 或自然语言名称来代替结构化字段：

| 维度 | 含义 |
| --- | --- |
| `subject` | 被评估的模型、变体或对照对象 |
| `target` | `high`、`low`、`close` 或适用的汇总目标 |
| `space` | 收益空间、价格空间或其他明确空间 |
| `aggregation` | `day_equal` 或 `pooled` |
| `slice` | 全样本、市场状态或其他预定义分组 |
| `baseline` | 独立基线标识；无基线时明确标记 |

- 默认主聚合为 `day_equal`：先计算每个验证日的指标，再对交易日等权汇总；`pooled` 仅用于论文式全样本对比，不能替代主结论。
- 置信区间使用按交易日整块重采样的配对 bootstrap：95%、10,000 次重采样、固定种子；参与比较的对象共享同一组重采样索引。有效验证日少于 10 个时，依赖该区间的指标状态为 `insufficient_data`。
- 方向主指标只评价 `close`：在实际收益非零的样本上计算涨/跌二分类准确率，预测为 0 计为错误，实际为 0 不进入该二分类分母。`high`/`low` 方向只在上涨、下跌两类各不少于 30 个样本且各占至少 10% 时作为条件诊断；不满足时为 `insufficient_data`。精确三分类、±5/±10 bps 死区三分类和 30/70 分位数三分类仅作为诊断或论文变体，不替代 close 主指标。
- `point` 指标同时保留收益空间与价格空间：收益空间报告 MAE、RMSE、MedAE、误差 P90 和 Bias；价格空间报告 MAE、RMSE、MAPE、R²，并可报告 SMAPE。high/low 包络命中率、包络宽度和包络矛盾率属于 `point` 诊断项，不是 `interval` 指标。
- `applicability` 只允许 `ok`、`not_applicable`、`insufficient_data`。只有 `ok` 提供数值；另外两种状态的 `value` 必须为 `null` 并附 `reason`，不得写成 0。point-only 模型的 `interval` 指标必须返回 `not_applicable`，不能省略或用包络诊断替代。
- 指标角色为 `primary`、`guardrail`、`diagnostic`、`paper`；卡片只展示 `primary` 与 `guardrail`，完整指标列表属于评估或状态输出。
- 本轮为破坏性升级：模型 artifact 与评估 schema 从 1 升至 2，`predictions.db` 从 2 升至 3；升级清理旧 `model_run` 记录，保留 `prediction` 与 `prediction_actual` 预测审计记录。旧 artifact、评估 JSON 和旧二进制不兼容新 schema，升级与回滚均需完整备份。

## 11. 论文对比口径

2026-10-20 的快速文献核对没有发现跨论文统一的“股价预测 SOTA”。下表只用于约束可比较量级，不能直接当作本项目目标：

| 研究 | 市场与任务 | 方向准确率 | 误差口径 | 可比性 |
| --- | --- | ---: | --- | --- |
| [HMG-TF](https://doi.org/10.24963/ijcai.2020/640), IJCAI 2020 | CSI 500，15 分钟 bar 预测 1 个交易日后涨跌 | 58.70% | 未报告；MCC 0.1487 | A 股短周期强参考，但任务不是下一根 5 分钟 bar |
| [Search-Then-Forecast](https://www2.econ.osaka-u.ac.jp/econ_society/dp/2606.pdf), Osaka DP 2026 | SSE 180 单票，预测未来 20 个交易日 | ARMAGARCH 58.43% | MAPE 7.13% | 单票、20 日周期，方向口径不同 |
| [GNN-LSTM](https://doi.org/10.30598/barekengvol20iss2pp0981-1000), 2026 | 100 只 ASX 股票，预测下一日收盘 | 66.8%～67.0% | MAPE 4.92%～4.98%；MAE 0.74～0.75 AUD | 多股票图模型，价格单位不可跨市场比较 |
| 新闻/情绪或已知盘中路径研究 | 下一日收益符号、开盘到收盘方向 | 70% 以上 | 各不相同 | 输入信息或预测窗口不同，不与本预测器横比 |
| Attention GRU / 越南地产深度学习 | 单票或小股票池日频方向 | 45%～46% | 摘要未统一报告 | 说明弱信号市场低于 50% 也常见 |

- 当前项目的 5 分钟下一根 bar 任务没有找到可直接对标的公开 A 股 SOTA；应优先比较同频 last-price/range 基线和多数类方向基线。
- 当前 3 个验证日、690 个样本的本地回测：方向准确率 44.35%，多数类 46.40%；非零方向 48.42%，非零多数类 50.63%；close MAE 0.1979%，last-price 0.1912%；high/low MAE 相对基线改善约 19.0%/28.8%。样本天数不足，不能据此宣称长期失效。
- 论文对比优先使用方向准确率及其多数类基线、收益空间 MAE/RMSE 及 skill、价格空间 MAPE/R²；不要只比较不同单位下的原始 MAE。

## 12. 限制

- 当前实时采集只保留腾讯最近窗口；训练前可用 `backfill-minutes` 从 BaoStock 或新浪补齐历史 5 分钟线。新浪单次最多覆盖约 104 个完整交易日，BaoStock 适合更长窗口。即使补齐，5 分钟标签噪声仍高，模型可能不稳定；卡片会显示样本数和基线对比。
- 5 分钟预测噪声高，点估计不代表概率、置信区间或收益承诺；high/low 包络诊断也不是区间保证。
- A 股 T+1：本模块不模拟日内买卖闭环；用户手动交易时需自行处理持仓和交易规则。
- 该模块不改变日报的可执行推荐、机会分或风险门槛。

## 13. 多窗口离线评估

```bash
go run ./cmd/stock-watcher predictor-eval \
  -as-of 2026-10-19 \
  -windows 40,60,120 \
  -validation-days 20 \
  -half-life 60 \
  -loss l2 \
  -close-loss l1
```

边界：

- 只读打开 `market.db` 和 `context.db`，不初始化 logger，不启动行情源、监控、通知或网页。
- 不创建 `predictions.db`，不写 `models/intraday/`、`state/` 或生产报告。
- 评估不发送通知；离线环境没有飞书凭证时可用 `STOCK_WATCHER_FEISHU_ENABLED=false` 启动，避免无关的配置校验失败。
- 输出一个确定性 JSON；指标按“评估指标契约”的规范化列表返回，不适用的指标用 `not_applicable`/`insufficient_data` 和 `null` 表达。数据不足时整体返回 `status=insufficient_history`、`skipped` 和 `notes`，不会自动缩短窗口。
- `B0` 会读取配置日历覆盖的全部交易日，长日历下比固定窗口更慢；这是 expanding 基线的代价。
- 股票池按验证日当时可见的报告恢复：优先 `report_archive` 中目标交易日 09:30 前生成的归档，缺失时才回退可变的 `selection_run`，并在 `universes[].report_source` 标明来源。
- 默认要求报告在目标交易日 09:30 前生成。历史报告是事后补生成时，可显式加 `-allow-late-reports`；它仍只接受 `as_of < target` 的记录，优先使用归档并标为 `archive-late`，归档不可读时才回退可变的 `selection_run` 并标为 `selection_run-late`，不会使用目标日数据。
- `-loss` 选择全部目标的训练损失（`l2` 或 `l1`），`-close-loss` 只覆盖 close 目标；生产候选是 `-loss l2 -close-loss l1`。两个参数都只属于 `predictor-eval`。

窗口与策略：

| 名称 | 定义 |
| --- | --- |
| `window-N` | 只用验证日之前最近 N 个交易日训练一个独立模型；任一 required session 缺失时整个 fold 跳过 |
| `B0` | expanding 基线，使用验证日之前全部可用交易日历史训练单模型 |
| `N0-last` | 朴素基线：下一根 high/low 延续当前 bar 的 high/low 相对收盘价，close 为 0 |
| `N0-range` | 朴素基线：high/low 取当前 bar 历史振幅的一半，close 为 0 |
| `E0` | 多窗口等权平均 |
| `E1` | 多窗口固定指数权重平均 |
| `E1-filtered` | 只在所有窗口 close 预测方向一致时保留 E1 结果 |
| `B0-filtered` | 使用同一方向一致过滤后的 B0 结果，作为过滤后对照 |

`E1` 权重按 `2^(-N/half_life)` 计算后归一化。默认 40/60/120 和 `half-life=60` 时约为 `0.4565/0.3624/0.1811`，短窗口权重更高。权重在评估前固定，不根据验证结果反复调参，避免验证集泄漏。

市场状态：

- 使用 `sh000300` 沪深 300；对每个验证日，只取该日之前 20 个交易日的收盘价计算 MA20。
- 验证日前最后一个收盘价相对 MA20 高于 `+1%` 为 `up`，低于 `-1%` 为 `down`，其余为 `range`。
- 缺失、缺口、陈旧数据或不足 20 个交易日为 `unknown`。
- 市场状态只用于 walk-forward 结果分组比较；当前不改变生产实时权重。

输出中的 `windows`、`strategies`、`regimes` 和 `universes` 只保留折次、可得性、过滤统计、市场状态和每日股票池元数据；所有数值指标统一放在 `metrics`，通过 `subject`、`slice`、`baseline` 关联到窗口、策略、市场状态和基线。指标状态、`day_equal` 主聚合和 bootstrap 区间以“评估指标契约”为准。
