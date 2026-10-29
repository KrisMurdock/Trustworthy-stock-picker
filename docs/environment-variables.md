# 配置与环境变量

业务参数和凭证全部来自环境变量。启动不读取 `config.yaml`，默认也不读取 `feishu.config`；只有显式设置 `STOCK_WATCHER_FEISHU_CREDENTIALS_FILE` 时才启用旧凭证文件兼容。观测股池因数量较多，仍独立保存在 `leaders.yaml`，由 `STOCK_WATCHER_POOL_FILE` 指定。根 `.env.example` 包含完整参数和部署示例。

## 加载方式

- 本地开发：复制 `.env.example` 为根 `.env`，执行 `make run`。启动脚本用 Bash 加载 `.env` 后运行 Go。直接运行二进制或 `make check/status` 不会自动读取 `.env`，可先执行 `set -a; source .env; set +a`。
- systemd：模板读取 `/etc/stock-watcher.env`。可从 `.env.example` 复制后删除仅 Docker 使用的变量，权限设为 `600`。使用 `KEY=value`，不要加 `export`。
- Docker Compose：默认读取仓库根 `.env`（`docker/compose.yaml` 中为 `../.env`），并从同一文件注入容器运行变量。执行 `docker compose --env-file .env -f docker/compose.yaml up -d --no-build`。如必须使用其他路径，设置 `STOCK_WATCHER_ENV_FILE` 为绝对路径。

`.env` 是私有文件，不得提交。Bash 加载时含 `;`、`#` 或空格的通知通道值必须使用双引号；Docker Compose 会去掉外层引号。含 `$` 的值在 Compose 中需写成 `$$`，或避免在同一个值中出现。

## 基础与运行

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_DATA_DIR | data | 行情与状态目录；默认本地 `data`，也可指向已挂载的 CIFS |
| STOCK_WATCHER_REQUIRE_CIFS | false | 默认本地存储；使用 Windows/CIFS 挂载盘时设为 true |
| STOCK_WATCHER_POLL_INTERVAL | 5m | 日线监控轮询间隔 |
| STOCK_WATCHER_REQUEST_TIMEOUT | 10s | 单次行情请求超时 |
| STOCK_WATCHER_POOL_FILE | leaders.yaml | 观测股池文件；相对路径按当前工作目录解析 |
| STOCK_WATCHER_WEB_ENABLED | true | 是否启动只读网页 |
| STOCK_WATCHER_WEB_ADDR | 127.0.0.1:8080 | 网页监听地址；容器内由 Compose 覆盖为 0.0.0.0:8080 |
| STOCK_WATCHER_WEB_PUBLIC_READ_ONLY | false | 公网匿名模式；只允许 GET/HEAD，并隐藏 AI 与预测写入口 |
| STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED | false | 临时 Cloudflare 公网隧道启动开关；仅 `scripts/run.sh` 读取，默认不开放公网 |
| STOCK_WATCHER_DAILY_RUN_AT | 15:00 | 每日收盘后调度时间，必须为 15:00 或之后 |
| STOCK_WATCHER_DAILY_HISTORY_DAYS | 365 | 分析窗口，允许 90～7300 天 |
| STOCK_WATCHER_DAILY_ADJUST | qfq | 分析复权口径：none/qfq/hfq |

## 股池

`leaders.yaml` 是启动时实际加载的已确认观测股池，格式为 `schema_version: 1` 加 `stocks` 列表；股票代码必须带 `sh`/`sz` 前缀，数量为 1～500。股池可能很大，因此不放进 `.env`，修改后重启服务即可。

`leaders.pending.yaml` 只是待人工确认的名单，不参与启动、行情同步或选股。确认后再合并到 `leaders.yaml`。

## 分析、策略与筛选

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_ANALYSIS_ENGINE | auto | auto / leader_daily_v1 / short_horizon_v1 |
| STOCK_WATCHER_ANALYSIS_TIMEOUT | 10m | 单次分析超时 |
| STOCK_WATCHER_ANALYSIS_SYNC_TIMEOUT | 90m | 同步分析总超时 |
| STOCK_WATCHER_STRATEGY_ENABLED | true | 是否启用短周期策略 |
| STOCK_WATCHER_STRATEGY_TARGET_NET_PCT | 3 | 主目标净收益，2.5 或 3 |
| STOCK_WATCHER_STRATEGY_STOP_NET_PCT | -3 | 最大净亏损，必须小于 0 |
| STOCK_WATCHER_STRATEGY_MAX_HOLDING_DAYS | 5 | 最大持有交易日 |
| STOCK_WATCHER_STRATEGY_TRADE_CASH | 20000 | 单笔预算 |
| STOCK_WATCHER_STRATEGY_COMMISSION_RATE | 0.0001 | 佣金费率 |
| STOCK_WATCHER_STRATEGY_MIN_COMMISSION | 5 | 最低佣金 |
| STOCK_WATCHER_STRATEGY_TRANSFER_RATE | 0.00001 | 过户费率 |
| STOCK_WATCHER_STRATEGY_SELL_STAMP_RATE | 0.0005 | 卖出印花税率 |
| STOCK_WATCHER_STRATEGY_SLIPPAGE_RATE | 0.0005 | 单边滑点率 |
| STOCK_WATCHER_STRATEGY_MAX_ENTRY_GAP_PCT | 3 | 最大开盘偏离 |
| STOCK_WATCHER_STRATEGY_MIN_EVIDENCE | 60 | 最少买入样本 |
| STOCK_WATCHER_STRATEGY_MIN_TRAINING_DAYS | 40 | 最少训练信号日 |
| STOCK_WATCHER_STRATEGY_HOLDOUT_DAYS | 40 | 留出样本天数 |
| STOCK_WATCHER_STRATEGY_MIN_RANK_SCORE | 60 | 最低机会分 |
| STOCK_WATCHER_FIRST_STAGE_ENABLED | true | 是否计算第一层高召回初筛；要求 `daily_adjust=qfq` |
| STOCK_WATCHER_FIRST_STAGE_PUSH_ENABLED | true | 是否推送初筛池飞书卡；依赖初筛已启用 |
| STOCK_WATCHER_FIRST_STAGE_BUDGET | 95 | 初筛候选池上限，允许 1～500；默认预算 95 只 |
| STOCK_WATCHER_SECOND_STAGE_ENABLED | true | 是否在第一层候选池上运行加权模型 blend Top10 精排；要求第一层已启用 |
| STOCK_WATCHER_SECOND_STAGE_PUSH_ENABLED | true | 是否把精排结果生成公众号纯文本稿并投递到 `second_stage` 路由；启用微信公众号草稿时，同一稿件还会写入草稿箱；依赖精排已启用 |
| STOCK_WATCHER_SECOND_STAGE_TOP_N | 10 | 第二层输出候选数，允许 1～100 |
| STOCK_WATCHER_SECOND_STAGE_BLEND_ID | risk_all20_local035_eta1 | 写入报告的 blend 计划标识，便于冻结和比较实验口径 |
| STOCK_WATCHER_SECOND_STAGE_FIRST_STAGE_WEIGHT | 0.2 | 第一层池内百分位权重，范围 0～1；与所有模型权重之和必须为 1 |
| STOCK_WATCHER_SECOND_STAGE_MODELS | jev=0.2,laya_official=0.2,laya_multilingual=0.2,chinese_decision=0.2 | 逗号分隔的 `model=weight` 权重表；模型名允许小写字母、数字、点、下划线和连字符 |
| STOCK_WATCHER_SECOND_STAGE_GROUP_SIZE | 5 | 模型组内百分位与局部校准先验的分组大小，允许 1～100 |
| STOCK_WATCHER_SECOND_STAGE_CALIBRATION | local | 模型百分位校准方式；`none` 不校准，`local` 用组内第一层先验做局部收缩 |
| STOCK_WATCHER_SECOND_STAGE_CALIBRATION_PARAM | 0.35 | `local` 校准的收缩系数，范围 0～1 |
| STOCK_WATCHER_SECOND_STAGE_RISK_ETA | 1 | ATR14 百分位风险惩罚系数，范围 0～1；0 表示不调整 |
| STOCK_WATCHER_SECOND_STAGE_PUBLISH_BY | 15:28 | 精排等待离线 artifact 的最晚时刻；到点仍未齐则发送带 `degraded=true` 的中性降级结果 |
| STOCK_WATCHER_STRUCTURE_GUARD_MODE | shadow | shadow 只观察；enforce 才进入可执行推荐 |
| STOCK_WATCHER_SELECTION_ENABLED | true | 是否启用趋势筛选 |
| STOCK_WATCHER_SELECTION_MIN_SCORE | 70 | 趋势最低分 |
| STOCK_WATCHER_SELECTION_TOP_N | 5 | 最大入选数量 |
| STOCK_WATCHER_SELECTION_MIN_VOLUME_RATIO | 1.2 | 最低量比 |
| STOCK_WATCHER_SELECTION_RSI_MIN | 50 | RSI 下限 |
| STOCK_WATCHER_SELECTION_RSI_MAX | 80 | RSI 上限 |
| STOCK_WATCHER_SELECTION_MIN_DAY_CHANGE | -3 | 单日跌幅下限（百分比） |
| STOCK_WATCHER_SELECTION_MAX_DAY_CHANGE | 7 | 单日涨幅上限（百分比） |

第一层初筛只生成第二层精排候选，不改变 `selected`、正式推荐或风险门槛。结果写入日报的 `first_stage` 字段；`push_enabled=true` 时与日报同一数据日期投递，只展示综合分前 30 名并合并为一张卡片，卡片明确标注“仅供第二层精排，不构成交易建议”。初筛计算失败会记录在日报中，但不阻断主报告。

信号日后第 3 个交易日日报生成后，初筛机器人还会收到窗口回看卡：以 3 个交易日内最高价相对初筛日收盘价涨幅 `≥ +1%` 判定命中，展示全候选召回率、Top 30 覆盖率和 Top 30 精确率；飞书仍只展示 Top 30，第二层精排使用全部候选，期末收盘涨幅只作参考。回看按信号日和窗口结束日生成稳定事件 ID，同一日期组合只入队一次；窗口行情不完整时暂不推送并等待后续轮询。

第二层精排读取 `<$STOCK_WATCHER_DATA_DIR>/state/secondstage/<model>/<as_of>.json` 的离线分数；`jev` 兼容回退到 `state/jev/<as_of>.json`。Go 进程先计算模型组内百分位与组内第一层先验，再按 `local = clamp(prior + calibration_param × (within - 0.5), 0, 1)` 校准，最后按 `blend_score = first_stage_weight × first_stage_percentile + Σ(model_weight × local) - risk_eta × atr14_percentile` 输出 Top10，结果写入日报的 `second_stage` 字段。默认 `risk_all20_local035_eta1` 为第一层 20% + JEV/Laya official/Laya multilingual/Chinese Decision 各 20%，`group_size=5`、`calibration_param=0.35`、`risk_eta=1`；所有模型共用同一 artifact schema。新增模型需要增加 artifact，并重分配第一层与全部模型权重使总和仍为 1。收盘后 `scripts/secondstage/generate_daily.py` 从冻结初筛报告生成候选与特征，并行运行四路模型，只有全部成功才原子发布到通用 artifact 路径；Go 生产进程不调用 Python。某个模型缺失或过期时按中性 0.5 百分位参与并标记 `second_stage.degraded=true`，不会阻断日报；树模型只保留辅助预测字段。初筛和主报告先发布，精排等待至 `publish_by`（默认 `15:28`）；到点仍缺失则按中性分降级发布，避免阻塞 15:30 前的收盘后链路。

`second_stage.push_enabled=true` 时，同一数据日期会生成一份可直接复制的公众号纯文本稿，包含公众号标题、正文、精排名单、逐只数据解读、行业分布、使用边界和风险提示。稿件以纯文本飞书消息投递到独立的 `second_stage` 路由，不会混入 `selection` 的日报卡片或 `first_stage` 的初筛卡。启用微信公众号草稿后，同一份结构化文章还会写入公众号草稿箱，但仍不会自动发布或群发。精排降级时标题和正文都会明确标注，未通过第一层或没有候选时不生成文章。文章与精排报告共享稳定事件 ID 和 T+1 09:30 过期时间，重启不会重复发送。

## 同步、日历与行情源

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_SYNC_MIN_COVERAGE_PCT | 95 | 十年完整性覆盖率门槛 |
| STOCK_WATCHER_SYNC_MIN_ANALYZABLE_PCT | 90 | 当日可分析覆盖率门槛 |
| STOCK_WATCHER_SYNC_INCREMENTAL_BARS | 5 | 不复权增量回补柱数 |
| STOCK_WATCHER_SYNC_STATUS_REFRESH_DAYS | 30 | 停牌/交易日状态刷新天数 |
| STOCK_WATCHER_CALENDAR_VALID_FROM | 2026-01-01 | 内置交易日历起始日 |
| STOCK_WATCHER_CALENDAR_VALID_THROUGH | 2026-12-31 | 内置交易日历结束日 |
| STOCK_WATCHER_CALENDAR_HOLIDAYS | 内置 2026 节假日 | 逗号分隔的休市日期；空串表示无额外休市日 |
| STOCK_WATCHER_MARKET_PROVIDERS | tencent,sina,baostock,tushare,eastmoney | 行情源顺序 |
| STOCK_WATCHER_MARKET_REQUEST_INTERVAL | 1s | 单源请求间隔 |
| STOCK_WATCHER_MARKET_ATTEMPT_TIMEOUT | 60s | 单源整窗尝试超时 |
| STOCK_WATCHER_MARKET_FAILURE_THRESHOLD | 3 | 连续失败后暂时跳过该源 |
| STOCK_WATCHER_MARKET_FAILURE_COOLDOWN | 5m | 跳过后的恢复探测间隔 |
| STOCK_WATCHER_MARKET_TENCENT_ENDPOINT | 腾讯公开端点 | 腾讯日线端点覆盖 |
| STOCK_WATCHER_MARKET_SINA_ENDPOINT | 新浪公开端点 | 新浪复权因子/行情端点覆盖 |
| STOCK_WATCHER_MARKET_SINA_DAILY_ENDPOINT | 新浪公开 K 线端点 | 新浪状态/历史分钟 K 线端点覆盖 |
| STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS | public-api.baostock.com:10030 | BaoStock TCP 地址 |
| STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT | https://api.tushare.pro | Tushare 端点覆盖 |
| STOCK_WATCHER_MARKET_TUSHARE_TOKEN_ENV | TUSHARE_TOKEN | Tushare Token 所在环境变量名 |
| STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT | 东方财富公开端点 | 东方财富日线端点覆盖 |
| STOCK_WATCHER_MARKET_LISTING_ENDPOINT | 新浪发行资料端点 | 上市日期查询端点覆盖 |

### 指数与板块同步

默认 `sync`、`run` 启动同步和到点日线同步都会在同一轮中并发下载核心指数与东方财富可发现的全部行业/概念板块。市场环境数据写入独立的 `<data_dir>/context.db`；板块发现或单板块失败只标记该库为 `partial/failed`，不会阻断股票行情和推荐报告。仅在补跑或排障时执行 `stock-watcher sync-context`。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_CONTEXT_ENABLED | true | 是否随默认同步流程采集指数与板块 |
| STOCK_WATCHER_CONTEXT_REQUEST_INTERVAL | 350ms | 板块列表与日线请求间隔，允许 350ms～1m |
| STOCK_WATCHER_CONTEXT_WORKERS | 3 | 板块日线并发数，允许 1～8 |
| STOCK_WATCHER_CONTEXT_HISTORY_LIMIT | 10000 | 单个板块首次回补的最大日线根数，允许 640～10000 |
| STOCK_WATCHER_CONTEXT_EASTMONEY_LIST_ENDPOINT | 东方财富板块列表端点 | 板块发现端点覆盖；仅允许 HTTPS 或回环 HTTP |
| STOCK_WATCHER_CONTEXT_EASTMONEY_KLINE_ENDPOINT | 东方财富板块日线端点 | 板块日线端点覆盖；额外允许指定的东方财富公开 HTTP 端点 |
| STOCK_WATCHER_CONTEXT_TENCENT_ENDPOINT | 腾讯指数日线端点 | 核心指数日线端点覆盖；仅允许 HTTPS 或回环 HTTP |
| STOCK_WATCHER_CONTEXT_KOREA_ENDPOINT | Naver 韩国指数日线端点 | KOSPI/KOSDAQ 日线端点覆盖；仅允许 HTTPS 或回环 HTTP |
| STOCK_WATCHER_CONTEXT_HANG_SENG_ENDPOINT | 腾讯恒生指数日线端点 | 恒生指数影子特征端点覆盖；仅允许 HTTPS 或回环 HTTP |
| STOCK_WATCHER_CONTEXT_KOREA_MINUTE_ENDPOINT | Naver 韩国指数分钟端点 | KOSPI/KOSDAQ 前向分钟缓存端点覆盖；仅允许 HTTPS 或回环 HTTP |

## 分钟行情

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_INTRADAY_ENABLED | true | 是否启用 5 分钟采集 |
| STOCK_WATCHER_INTRADAY_POLL_INTERVAL | 1m | 分钟采集轮询间隔 |
| STOCK_WATCHER_INTRADAY_REQUEST_INTERVAL | 350ms | 分钟请求间隔 |
| STOCK_WATCHER_INTRADAY_WORKERS | 3 | 分钟采集并发数 |
| STOCK_WATCHER_INTRADAY_CALIBRATION_AT | 15:10 | 每日分钟数据校准时间 |
| STOCK_WATCHER_INTRADAY_TENCENT_ENDPOINT | 腾讯分钟端点 | 分钟行情端点覆盖 |

## 5 分钟预测

预测模块只读取已保存的分钟线和日线，针对今日日报入选股票输出下一根 5 分钟 K 线的点估计。模型、预测库和开关都落在 `<data_dir>`，不连接券商、不自动下单。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_PREDICTOR_ENABLED | true | 是否启动 5 分钟预测循环 |
| STOCK_WATCHER_PREDICTOR_PUSH_ENABLED | true | 是否向飞书推送聚合预测卡；关闭后仍保存预测 |
| STOCK_WATCHER_PREDICTOR_TRAIN_AT | 15:30 | 收盘后训练下一交易日模型的时间，允许 15:10～23:00 |
| STOCK_WATCHER_PREDICTOR_WRITE_MODE | loopback | 网页推送开关写权限：off/loopback/all |
| STOCK_WATCHER_PREDICTOR_MIN_TRAINING_DAYS | 5 | 训练所需最少交易日 |
| STOCK_WATCHER_PREDICTOR_VALIDATION_DAYS | 3 | expanding walk-forward 验证交易日 |
| STOCK_WATCHER_PREDICTOR_KEEP_MODELS | 10 | 保留的模型代数 |

模型缺失、过期或 `valid_for` 不是今天时不会预测，也不会推送旧结果。预测结果实时落库并推送预测卡；上一根实际 K 线确认后，再单独推送偏差对比卡。网页开关只控制飞书投递，不停止预测和落库。

## 全天高低价预测

该模块默认关闭，只生成可审计的盘前高低价观察，不进入正式选股分或推荐门槛。启用后 `run` 会在交易日按内置时间执行训练、盘前预测和收盘核对，结果写入独立的 `<data_dir>/dayrange.db`；`init` 会创建或校验该库。阶段六的盘中剩余时段模型尚未实现，API 和网页会明确返回 `phase_six_not_implemented`，不会把盘前结果冒充盘中更新。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_DAYRANGE_ENABLED | false | 是否启用盘前高低价模型、预测档案、收盘核对和网页入口 |
| STOCK_WATCHER_DAYRANGE_PUSH_ENABLED | true | 是否把盘前结果推送到 `predictor_dayrange` 命名通道；关闭后仍保存预测 |
| STOCK_WATCHER_DAYRANGE_TRAIN_AT | 15:40 | 收盘后训练时间，允许 15:10～23:00 |
| STOCK_WATCHER_DAYRANGE_PREDICT_AT | 08:45 | 盘前预测时间，必须早于 09:30 |
| STOCK_WATCHER_DAYRANGE_RECONCILE_AT | 18:35 | 收盘核对时间，允许 15:10～23:59 |
| STOCK_WATCHER_DAYRANGE_HISTORY_DAYS | 365 | 训练数据历史窗口，允许 180～7300 个自然日 |
| STOCK_WATCHER_DAYRANGE_MIN_TRAIN_DAYS | 120 | 最少训练交易日，允许 60～`HISTORY_DAYS` |
| STOCK_WATCHER_DAYRANGE_CALIBRATION_DAYS | 60 | 时间顺序校准窗口，允许 10～250 个交易日 |
| STOCK_WATCHER_DAYRANGE_VALIDATION_DAYS | 20 | 独立验证窗口，允许 10～120 个交易日 |
| STOCK_WATCHER_DAYRANGE_KEEP_MODELS | 10 | 保留的候选模型代数；仍被预测引用的模型和基线不会被清理 |
| STOCK_WATCHER_DAYRANGE_STOCK_FEATURES | true | 启用个股日线特征层 |
| STOCK_WATCHER_DAYRANGE_MARKET_FEATURES | true | 启用指数与市场广度特征层 |
| STOCK_WATCHER_DAYRANGE_SECTOR_FEATURES | true | 启用行业/参考池相对强弱特征层；三个特征层不能同时关闭 |

模型按时间顺序拆分训练、校准和验证，候选模型只有通过覆盖率、区间宽度和样本门槛才会标记为 `validated`；否则保留历史波幅基线作为正式结果，并把候选留在研究档案。模型缺失、过期、checksum 无效、校准不足或共享盘不可用时不会发布旧预测，也不会回退到服务器本地目录。`train-dayrange -offline` 使用临时库训练和评估，不占生产写锁，也不写生产 `dayrange.db`。

盘前预测按目标交易日和页码生成稳定事件 ID，分页卡片只投递到 `route=predictor_dayrange` 的通道，账本位于 `<data_dir>/state/dayrange-delivery.json`。同一目标日、同一页重启或重复执行不会重复发送；临时网络错误按共享投递账本规则退避重试，09:30 后未完成卡片过期。手工执行 `predict-dayrange` 也遵循同一开关和账本。

盘前预测保存后还会按同一目标日生成独立执行确认卡：只读取对应信号日的精排 Top N，关联预测高/低、参考买入价、净目标价、止损价和正式推荐状态，输出“可执行／观察／放弃”。卡片只投递到 `route=premarket_execution`，账本为 `<data_dir>/state/premarket-execution-delivery.json`，稳定事件 ID 为 `premarket-execution:<目标日>`，09:30 后过期。它与 `predictor_dayrange` 分页卡、`second_stage` 公众号稿和 `selection` 正式日报相互独立；`STOCK_WATCHER_DAYRANGE_PUSH_ENABLED=false` 只关闭盘前分页卡，不关闭执行确认卡。结构影子期或筛选未启用时不会产生“可执行”，精排降级、预测缺失和低于目标价等情形会在卡片中说明，不对应交易指令。

## 通知与凭证

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_FEISHU_ENABLED | false（示例） | 是否启用 legacy 飞书通道 |
| FEISHU_WEBHOOK | 空 | legacy 飞书自定义机器人 webhook；只从环境读取 |
| FEISHU_SECRET | 空 | 飞书签名密钥；未启用签名可省略 |
| FEISHU_*_WEBHOOK | 空 | 命名通道路由引用的独立 webhook 环境变量，例如 `FEISHU_PREMARKET_EXECUTION_WEBHOOK` |
| STOCK_WATCHER_FEISHU_CREDENTIALS_FILE | 空 | 显式启用旧版 `feishu.config` 兼容；新部署应改用环境变量 |
| STOCK_WATCHER_NOTIFY_CHANNELS | 空 | 可选多通道；格式见下 |
| STOCK_WATCHER_WECHAT_ENABLED | false | 是否把第二层精排文章和盘前观察稿写入微信公众号草稿箱；只创建草稿，不发布、不群发 |
| STOCK_WATCHER_WECHAT_CREDENTIALS_FILE | 空 | 可选的私有凭证文件；支持 `AppID`/`AppSecret` 成对格式或 `app_id=...` 形式，相对路径按配置基准目录解析 |
| WECHAT_APP_ID | 空 | 公众号 AppID；优先从环境读取，也可由凭证文件提供 |
| WECHAT_APP_SECRET | 空 | 公众号 AppSecret；优先从环境读取，也可由凭证文件提供，必须保持私密 |
| WECHAT_THUMB_MEDIA_ID | 空 | 可选封面永久素材 media_id；留空时首次发送自动上传内置封面 |
| STOCK_WATCHER_WECHAT_AUTHOR | 空 | 可选草稿作者名，最多 16 个字符 |
| TUSHARE_TOKEN | 空 | Tushare Token；缺失时跳过 Tushare |
命名通道使用 `name|type|enabled|webhook_env|secret_env|route`，条目之间用 `;` 分隔；命名通道的 `type` 当前只支持 `feishu`，微信公众号草稿由 `STOCK_WATCHER_WECHAT_ENABLED` 自动创建为内置 `wechat-draft` 渠道。`route` 可选 `all`、`selection`、`first_stage`、`second_stage`、`premarket_execution`、`predictor_realtime`、`predictor_dayrange`、`wechat_premarket`；多个路由可用英文逗号分隔，留空表示兼容旧配置并向所有阶段广播：

```dotenv
STOCK_WATCHER_FEISHU_ENABLED=false
STOCK_WATCHER_NOTIFY_CHANNELS="first-stage|feishu|true|FEISHU_FIRST_STAGE_WEBHOOK||first_stage;second-stage|feishu|true|FEISHU_SECOND_STAGE_WEBHOOK||second_stage;selection|feishu|true|FEISHU_SELECTION_WEBHOOK||selection;premarket-execution|feishu|true|FEISHU_PREMARKET_EXECUTION_WEBHOOK||premarket_execution;predictor-realtime|feishu|true|FEISHU_PREDICTOR_REALTIME_WEBHOOK||predictor_realtime;predictor-dayrange|feishu|true|FEISHU_PREDICTOR_DAYRANGE_WEBHOOK||predictor_dayrange"
```

路由含义：`first_stage` 只接收第一层初筛池及回看卡；`second_stage` 只接收第二层精排公众号稿；`selection` 只接收正式日报主卡和详情卡；`premarket_execution` 只接收盘前执行确认卡；`predictor_realtime` 接收 5 分钟实时预测及偏差卡；`predictor_dayrange` 只接收全天高低价盘前预测；`wechat_premarket` 只接收盘前公众号草稿。不同路由的投递依赖彼此独立，不会因为另一个机器人失败而互相阻塞。微信公众号草稿使用内置的 `wechat-draft` 渠道，同时绑定 `second_stage` 与 `wechat_premarket`，不会接收初筛、正式日报、实时预测或飞书专用执行卡。

凭证只来自环境变量或权限受控的私有文件，不写入日志、镜像或仓库。关闭通知时显式设置 `STOCK_WATCHER_FEISHU_ENABLED=false` 和 `STOCK_WATCHER_WECHAT_ENABLED=false`，并确保没有设置 `STOCK_WATCHER_NOTIFY_CHANNELS` 的启用通道。

### 微信公众号草稿

`STOCK_WATCHER_WECHAT_ENABLED=true` 时，系统会通过 `draft/add` 写入两类公众号草稿：收盘后的第二层精排文章，以及盘前 08:45 保存预测后生成的“盘前观察”稿。盘前稿把精排候选、执行状态、预测高/低点、边际区间、技术指标、缠论与波浪视角合并为一篇；每只进入文章的候选还会附带一张最近 80 个交易日的日 K 结构图，叠加顶/底分型、缠论笔、笔重叠区和简化波浪关键价位，图内摘要同步展示分型/笔数量、重叠区、波浪阶段、回撤和最近上下方结构位。图表由原生 Go 渲染成 PNG，经 `media/uploadimg` 上传后嵌入草稿正文；单图超过 1 MiB、渲染失败或上传失败都不会降级为无图稿。精排队列暂缺时按已保存预测降级生成，并明确标注序号不是推荐排名。初筛、正式日报、实时预测和飞书专用执行卡不会进入草稿箱。启用该通道要求第二层精排推送或 dayrange 盘前推送至少有一个开启，否则 `check-config` 会拒绝启动。

公众号后台需要提供 AppID 和 AppSecret，可放入 `WECHAT_APP_ID`/`WECHAT_APP_SECRET` 环境变量，或设置 `STOCK_WATCHER_WECHAT_CREDENTIALS_FILE` 指向权限为 `0600` 的私有文件；环境变量优先于文件。凭证文件支持 `AppID` 下一行 `AppSecret` 两段式，也支持 `app_id=...` 配 `app_secret=...` 的形式，单个文件最多读取 64 KiB。`wechat.config` 已被 Git 忽略，不得提交。

还需要把实际发起请求主机的公网出口 IP 加入 IP 白名单。容器、systemd 和本地运行可能使用不同出口 IP，以实际请求为准；如果出口 IP 变化，微信会返回 `40164`，修正白名单后执行 `retry wechat-draft` 解锁账本中的永久错误。`check-config` 只校验配置格式，不访问微信接口。

该实现只调用草稿接口，不包含 `freepublish/submit` 或 `message/mass/sendall`。官方文档中新增草稿不要求“仅认证”，但发布草稿和群发要求认证；因此未认证公众号可以把文章整理到草稿箱，仍需在公众号后台人工检查并发布或群发。账本中的 `delivered` 只表示草稿已创建，不表示文章已发布、群发或送达粉丝。

未设置 `WECHAT_THUMB_MEDIA_ID` 时，首次发送会自动上传内置 900×506 PNG 封面并缓存 media_id；进程重启后会重新上传。长期运行建议先在公众号后台取得永久图片素材的 media_id，再设置 `WECHAT_THUMB_MEDIA_ID`，避免重复上传。草稿正文由确定性纯文本转换为安全 HTML，不包含外部图片、外链或脚本。

草稿事件与精排文章共享 T+1 09:30 过期时间；盘前稿最晚在目标交易日 09:30 前有效。文章未生成、精排降级到没有候选或事件已过期时不会创建草稿，也不临时编造价格。投递账本采用至少一次语义；若微信已创建草稿但本地在记录成功前崩溃，重启后可能重复创建草稿，需要在草稿箱人工清理重复项。

## AI 与大师复盘配置

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_LLM_ENABLED | true | 是否允许 AI 问答与归因；关闭后仍可使用确定性复盘，但不能生成模型回答 |
| STOCK_WATCHER_LLM_BASE_URL | https://api.openai.com/v1 | OpenAI 兼容端点；生产必须 HTTPS，HTTP 仅允许回环主机 |
| STOCK_WATCHER_LLM_API_KEY | 空 | 模型 API Key；必须同时配置端点和模型才能问答 |
| STOCK_WATCHER_LLM_MODEL | gpt-4.1-mini | 模型名称 |
| STOCK_WATCHER_LLM_TIMEOUT | 60s | 单次模型请求超时，允许 1s～5m |
| STOCK_WATCHER_LLM_CONFIG_FILE | llm.config | 网页保存配置的私有文件，相对当前工作目录解析；不要放在 CIFS 数据目录内 |
| STOCK_WATCHER_LLM_WRITE_MODE | loopback | 网页写配置范围：off / loopback / all；容器或反向代理远程使用时通常需显式设为 all |

AI 配置按字段合并，优先级为环境变量 > `llm.config` > 内置默认值。环境变量提供的字段会被锁定，网页只能修改未锁定的字段；页面的“测试连接”只发送固定探测请求，不包含用户问题或复盘证据。默认模型为 OpenAI 兼容接口，也可填写自建网关的 HTTPS `base_url`。

`llm.config` 使用 `schema_version: 1`，最多包含 `base_url`、`api_key`、`model` 和 `timeout`；文件权限应为 `0600`。它独立于 `STOCK_WATCHER_DATA_DIR`，不会随行情库进入 CIFS，也不得提交到 Git。网页只显示密钥是否已配置和末四位，不会回显明文；保存采用同目录临时文件、`fsync` 和原子替换。备份或迁移时应把该文件与数据目录、`feishu.config` 一并纳入，并保持权限。

网页写配置受 `STOCK_WATCHER_LLM_WRITE_MODE` 控制：`off` 禁止写入和测试，`loopback` 只允许回环来源，`all` 允许所有来源写入。`all` 不提供认证，远程部署必须同时使用受控网络或已有认证反向代理，并在代理层验证未登录与无权限请求。所有写请求还要求同源、JSON 和固定动作头；跨站请求会被拒绝。

问答与复盘归因只在用户点击后发起。上下文只包含选定的报告修订、复盘事件证据、试验档案摘要和最近三轮可见对话；不会读取网页、新闻、公告或隐藏提示词，也不会生成 SQL。模型请求默认 60 秒超时，流式回答可取消；未配置模型时页面会保留确定性证据，并明确提示无法生成回答。

## 日志

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_LOG_DIR | 仓库根目录/logs | 未设置时向上识别 `go.mod`/`.git` 并统一写入项目根目录；显式相对路径仍按当前工作目录解析 |
| STOCK_WATCHER_LOG_MAX_MB | 10 | 单文件大小上限 MB，整数 1～1000 |
| STOCK_WATCHER_LOG_MAX_DAYS | 7 | 轮转日志保留天数，整数 1～1000 |
| STOCK_WATCHER_LOG_BACKUPS | 10 | 轮转文件保留份数，整数 1～1000，gzip 压缩 |
| STOCK_WATCHER_LOG_JSON | false | true 输出结构化日志，false 输出可读文本 |
| STOCK_WATCHER_LOG_COLOR | auto | auto / force / off，仅控制 stdout；文件和 JSON 无颜色 |

## Docker Compose 变量

| 变量 | 示例/默认 | 含义 |
| --- | --- | --- |
| STOCK_WATCHER_IMAGE | stock-watcher:local | 构建、启动所用镜像；发布使用明确提交 SHA 标签 |
| STOCK_WATCHER_VERSION | development | 写入二进制与镜像 revision 的版本；发布设为完整提交 SHA |
| STOCK_WATCHER_WEB_BIND | 127.0.0.1:8080 | Compose 网页的宿主机绑定地址与端口，容器内固定 8080 |
| STOCK_WATCHER_HOST_DATA | ../data | 宿主机数据目录；本地目录或已挂载 Windows 共享 |
| STOCK_WATCHER_HOST_POOL | ../leaders.yaml | 宿主机股池文件路径，只读挂载到 `/app/leaders.yaml`；相对路径以 `docker/compose.yaml` 目录为基准 |
| STOCK_WATCHER_HOST_LOGS | ../logs | 已创建的宿主机日志目录；相对路径解析方式同上 |
| STOCK_WATCHER_ENV_FILE | ../.env（相对 Compose 文件） | 容器运行变量文件路径 |

Compose 固定容器内数据 `/data`、股池 `/app/leaders.yaml`、日志 `/logs`，并关闭颜色。绑定挂载的相对路径按 Compose 文件所在目录解析，默认示例使用 `../` 指回仓库根目录；生产建议改为绝对路径。宿主路径必须预先存在，Compose 不会自动创建。本地目录部署保持 `STOCK_WATCHER_REQUIRE_CIFS=false`，共享盘部署设为 `true`。

`run` 同时启动监控任务和网页，默认只允许本机访问。设置 `STOCK_WATCHER_WEB_ENABLED=false` 可以关闭网页。`STOCK_WATCHER_WEB_PUBLIC_READ_ONLY=true` 表示网页将面向匿名公网：应用只接受 GET/HEAD，隐藏 AI 与预测写入口，并对配置、模型对话、归因和 Predictor 写入返回 404。该开关不提供认证或限流，普通远程部署仍应使用受控网络或已有认证反向代理。`STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED` 默认 `false`，只由源码仓库的 `scripts/run.sh` 启动器读取；设为 `true` 后 `make run` 会改用 Cloudflare Quick Tunnel 启动匿名只读入口。Compose 和 systemd 直接运行二进制，不会启动该隧道。容器内固定监听 `0.0.0.0:8080`，并用 `STOCK_WATCHER_WEB_BIND` 控制宿主机入口。应用使用根路径 `/`，反向代理应保留 `/api/` 和 `/assets/` 路径；不支持直接挂到子路径前缀。

Compose 长挂载与 env_file 语义参见 [Docker 官方服务配置](https://docs.docker.com/reference/compose-file/services/)。本配置要求 Docker Compose 2.30.0 或更高版本。
