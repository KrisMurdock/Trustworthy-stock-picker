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

## 3. 模型

- 跨股票池 pooled，按目标拆成 high/low/close 三个 GBDT；每个 GBDT 是多棵回归树。
- 15/30/60 分钟只作为特征尺度，不单独训练四个模型，也不直接平均不同期限的价格。
- 纯 Go 训练和推理，固定种子、固定特征顺序和稳定 tie-break；模型 artifact 可确定性重放。
- 特征包括：多尺度收益/振幅/量比/波动率、收盘位置、影线、MA/RSI、时段、股池横截面宽度、上一交易日 qfq 日线上下文和缺失指示。
- 标签只取同交易时段、正好 +5 分钟的下一根 bar；11:30 和 15:00 不作为训练输入。
- 训练按日 expanding walk-forward 评估；生产模型用截止日全量样本重训，卡片显示 walk-forward 指标。

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

- 每个目标 bar 一张聚合飞书卡，默认包含所有推荐票。
- 卡片列：名称、现价、预测高、预测低、预测收、趋势、置信度；价格后附相对百分比。
- 从第二张卡片起增加“上一根实际对比”，逐股展示上一根实际高/低/收，以及相对该根预测的价差和百分点偏差；实际 K 线尚未确认时显示“待确认”。
- 页脚：模型版本、训练截止日、样本数、上一根 MAE、`基准价=上一根5分钟收盘`、实验性声明。
- 未优于基线或样本不足时，页脚显示警告，但仍按用户选择推送。
- 推送事件 ID 为 `intraday:<date>:<target_end_unix>`；过期时间为目标 bar 结束时间，过期后不再投递。
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
- CIFS 不可用：写入停止，不落本地目录，不发送旧预测。
- 飞书发送失败：沿用投递账本的重试/过期语义；超过目标 bar 结束时间即过期。
- 训练失败：保留失败原因；开盘前补训；仍无有效模型时不推送。

## 10. 限制

- 当前本地 5 分钟历史很短（约 17 个交易日、单股约 800 根），模型可能不稳定；卡片会显示样本数和基线对比。
- 5 分钟预测噪声高，点估计不代表概率或收益承诺。
- A 股 T+1：本模块不模拟日内买卖闭环；用户手动交易时需自行处理持仓和交易规则。
- 该模块不改变日报的可执行推荐、机会分或风险门槛。
