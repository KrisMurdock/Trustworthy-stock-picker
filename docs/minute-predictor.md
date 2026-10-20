# 5 分钟价格预测

本模块针对当日日报的正式推荐或回退观察排名，使用本地已保存的 5 分钟线和日线训练树模型，滚动预测下一根 5 分钟 K 线的高点、低点和收盘价，并把结果聚合成飞书观察卡。它不接券商、不维护持仓、不下单，也不构成投资建议。

## 1. 边界

- 交易由用户手动完成；系统只输出预测。
- 预测对象优先是日报中 `target_trading_date=今天` 的 `Selected`；严格推荐为空时，回退到同一报告的 Top N 观察排名。没有有效日报或两者都为空时，不预测、不推送。
- 回退只使用当日目标报告已有的观察排名，不读取旧报告，也不扩大到全股池。
- 预测是点估计，不是保证区间；历史误差和样本数会随卡片展示。

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
- 生产模型当前仍使用单一训练区间：读取最近 400 个自然日的 5 分钟线，用截止日全量样本重训，卡片显示 walk-forward 指标。
- `predictor-eval` 提供离线的多训练窗口 rolling walk-forward 评估；它不改变生产模型权重，也不写入模型、预测库或飞书。

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

`predictions.db` schema 1 包含：

- `model_run`：模型版本、训练区间、有效期、指标和 checksum。
- `prediction`：每只股票、每个目标 bar 的不可变预测。
- `prediction_actual`：目标 bar 到达后的实际值和误差状态。
- `kalman_state`：每只股票、每个交易日的趋势滤波状态。

## 6. 推送

- 每个目标 bar 一张实时预测卡，默认包含当前已完成的推荐票预测；预测完成后立即入队，不等待上一根实际值。
- 预测卡头部使用“5 分钟价格预测”标题、预测窗口副标题和图表图标；顶部摘要展示预测窗口和标的数。
- 主表为名称、现价、预测高、预测低、预测收、信号（趋势 · 置信度）；价格后附相对百分比，数字列右对齐。
- 预测卡不嵌入上一根实际对比。上一根实际高/低/收与预测值到齐后，单独生成一张“上轮预测偏差”卡：顶部摘要展示对比窗口、标的数、已确认数和平均收盘误差，表格逐股展示实际高/低/收以及相对该根预测的价差和百分点偏差。
- 预测卡与偏差卡的模型状态区使用字段网格展示模型版本、训练截止日、样本数和验证 MAE，并单独列出基线、风险提示和实验性声明。
- 未优于基线或样本不足时显示警告，但仍按用户选择推送。
- 预测卡事件 ID 为 `intraday:<date>:<target_end_unix>`；偏差卡事件 ID 为 `intraday:<date>:<target_end_unix>:actual`。预测卡过期时间为目标 bar 结束时间；偏差卡在当前目标 bar 结束前有效。
- 偏差卡只有在对应预测卡已入队、全部入选标的的上一根实际值已确认后才入队，并使用更高的投递序号排在预测卡之后。
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

## 10. 限制

- 当前本地 5 分钟历史很短（约 17 个交易日、单股约 800 根），模型可能不稳定；卡片会显示样本数和基线对比。
- 5 分钟预测噪声高，点估计不代表概率或收益承诺。
- A 股 T+1：本模块不模拟日内买卖闭环；用户手动交易时需自行处理持仓和交易规则。
- 该模块不改变日报的可执行推荐、机会分或风险门槛。

## 11. 多窗口离线评估

```bash
go run ./cmd/stock-watcher predictor-eval \
  -as-of 2026-10-19 \
  -windows 40,60,120 \
  -validation-days 20 \
  -half-life 60
```

边界：

- 只读打开 `market.db` 和 `context.db`，不初始化 logger，不启动行情源、监控、通知或网页。
- 不创建 `predictions.db`，不写 `models/intraday/`、`state/` 或生产报告。
- 评估不发送通知；离线环境没有飞书凭证时可用 `STOCK_WATCHER_FEISHU_ENABLED=false` 启动，避免无关的配置校验失败。
- 输出一个确定性 JSON；数据不足时返回 `status=insufficient_history`、`skipped` 和 `notes`，不会自动缩短窗口。
- `B0` 会读取配置日历覆盖的全部交易日，长日历下比固定窗口更慢；这是 expanding 基线的代价。
- 股票池按验证日当时可见的报告恢复：优先 `report_archive` 中目标交易日 09:30 前生成的归档，缺失时才回退可变的 `selection_run`，并在 `universes[].report_source` 标明来源。
- 默认要求报告在目标交易日 09:30 前生成。历史报告是事后补生成时，可显式加 `-allow-late-reports`；它仍只接受 `as_of < target` 的记录，优先使用归档并标为 `archive-late`，归档不可读时才回退可变的 `selection_run` 并标为 `selection_run-late`，不会使用目标日数据。

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

输出中的 `windows`、`strategies`、`regimes` 和 `universes` 共同记录指标、B0 对比、方向一致过滤统计、市场状态和每日股票池，便于复核评估是否使用了正确的时间边界。
