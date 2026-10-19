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
| STOCK_WATCHER_DAILY_RUN_AT | 18:05 | 每日调度时间，必须为 16:00 之后 |
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
| STOCK_WATCHER_STRUCTURE_GUARD_MODE | shadow | shadow 只观察；enforce 才进入可执行推荐 |
| STOCK_WATCHER_SELECTION_ENABLED | true | 是否启用趋势筛选 |
| STOCK_WATCHER_SELECTION_MIN_SCORE | 70 | 趋势最低分 |
| STOCK_WATCHER_SELECTION_TOP_N | 5 | 最大入选数量 |
| STOCK_WATCHER_SELECTION_MIN_VOLUME_RATIO | 1.2 | 最低量比 |
| STOCK_WATCHER_SELECTION_RSI_MIN | 50 | RSI 下限 |
| STOCK_WATCHER_SELECTION_RSI_MAX | 80 | RSI 上限 |
| STOCK_WATCHER_SELECTION_MIN_DAY_CHANGE | -3 | 单日跌幅下限（百分比） |
| STOCK_WATCHER_SELECTION_MAX_DAY_CHANGE | 7 | 单日涨幅上限（百分比） |

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
| STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS | public-api.baostock.com:10030 | BaoStock TCP 地址 |
| STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT | https://api.tushare.pro | Tushare 端点覆盖 |
| STOCK_WATCHER_MARKET_TUSHARE_TOKEN_ENV | TUSHARE_TOKEN | Tushare Token 所在环境变量名 |
| STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT | 东方财富公开端点 | 东方财富日线端点覆盖 |
| STOCK_WATCHER_MARKET_LISTING_ENDPOINT | 新浪发行资料端点 | 上市日期查询端点覆盖 |

## 分钟行情

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_INTRADAY_ENABLED | true | 是否启用 5 分钟采集 |
| STOCK_WATCHER_INTRADAY_POLL_INTERVAL | 1m | 分钟采集轮询间隔 |
| STOCK_WATCHER_INTRADAY_REQUEST_INTERVAL | 350ms | 分钟请求间隔 |
| STOCK_WATCHER_INTRADAY_WORKERS | 3 | 分钟采集并发数 |
| STOCK_WATCHER_INTRADAY_CALIBRATION_AT | 15:10 | 每日分钟数据校准时间 |
| STOCK_WATCHER_INTRADAY_TENCENT_ENDPOINT | 腾讯分钟端点 | 分钟行情端点覆盖 |

## 通知与凭证

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_FEISHU_ENABLED | false（示例） | 是否启用 legacy 飞书通道 |
| FEISHU_WEBHOOK | 空 | 飞书自定义机器人 webhook；只从环境读取 |
| FEISHU_SECRET | 空 | 飞书签名密钥；未启用签名可省略 |
| STOCK_WATCHER_FEISHU_CREDENTIALS_FILE | 空 | 显式启用旧版 `feishu.config` 兼容；新部署应改用环境变量 |
| STOCK_WATCHER_NOTIFY_CHANNELS | 空 | 可选多通道；格式见下 |
| TUSHARE_TOKEN | 空 | Tushare Token；缺失时跳过 Tushare |

命名通道使用 `name|type|enabled|webhook_env|secret_env`，条目之间用 `;` 分隔；`type` 当前只支持 `feishu`。例如：

```dotenv
STOCK_WATCHER_FEISHU_ENABLED=false
STOCK_WATCHER_NOTIFY_CHANNELS="feishu-main|feishu|true|FEISHU_WEBHOOK|FEISHU_SECRET"
```

凭证只来自环境变量，不写入日志、镜像或仓库。关闭通知时显式设置 `STOCK_WATCHER_FEISHU_ENABLED=false`，并确保没有设置 `STOCK_WATCHER_NOTIFY_CHANNELS` 的启用通道。

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

`run` 同时启动监控任务和网页，默认只允许本机访问。设置 `STOCK_WATCHER_WEB_ENABLED=false` 可以关闭网页；网页没有账号或写入接口，需要远程访问时通过受控网络或已有认证反向代理开放。容器内固定监听 `0.0.0.0:8080`，并用 `STOCK_WATCHER_WEB_BIND` 控制宿主机入口。应用使用根路径 `/`，反向代理应保留 `/api/` 和 `/assets/` 路径；不支持直接挂到子路径前缀。

Compose 长挂载与 env_file 语义参见 [Docker 官方服务配置](https://docs.docker.com/reference/compose-file/services/)。本配置要求 Docker Compose 2.30.0 或更高版本。
