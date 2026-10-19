# 配置与环境变量

业务配置已编译进二进制，启动不再读取 `config.yaml`。外部运行时文件只剩股池 `leaders.yaml` 和私有凭证 `feishu.config`（或环境变量）。以下环境变量用于覆盖内置默认值和部署差异。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_DATA_DIR | data | 行情与状态目录；默认本地 `data`，也可指向已挂载的 CIFS |
| STOCK_WATCHER_WEB_ADDR | 127.0.0.1:8080 | `run` 的只读网页监听地址；IP 或 localhost 加端口。容器默认 0.0.0.0:8080 |
| STOCK_WATCHER_POOL_FILE | leaders.yaml | 股池文件，相对当前工作目录解析 |
| STOCK_WATCHER_REQUIRE_CIFS | false | 默认本地存储；使用 Windows/CIFS 挂载盘时设为 true |
| STOCK_WATCHER_FEISHU_ENABLED | true | 是否启用飞书投递 |
| STOCK_WATCHER_FEISHU_CREDENTIALS_FILE | feishu.config | 飞书凭证文件，相对当前工作目录解析；设为空串可跳过文件 |
| FEISHU_WEBHOOK | 凭证文件值 | 默认飞书通道 webhook；显式空值也会覆盖文件 |
| FEISHU_SECRET | 凭证文件值 | 机器人签名密钥；未启用签名时可省略 |
| STOCK_WATCHER_STRATEGY_ENABLED | true | 是否启用全池短周期排名 |
| STOCK_WATCHER_INTRADAY_ENABLED | true | 是否启用分钟采集 |
| STOCK_WATCHER_WEB_ENABLED | true | 是否启动只读网页 |
| STOCK_WATCHER_POLL_INTERVAL | 5m | 日线监控轮询间隔 |
| STOCK_WATCHER_REQUEST_TIMEOUT | 10s | 单次行情请求超时 |
| STOCK_WATCHER_CALENDAR_VALID_FROM | 2026-01-01 | 内置交易日历起始日 |
| STOCK_WATCHER_CALENDAR_VALID_THROUGH | 2026-12-31 | 内置交易日历结束日 |
| STOCK_WATCHER_CALENDAR_HOLIDAYS | 内置 2026 节假日 | 逗号分隔的休市日期，设置空串表示无额外休市日 |
| STOCK_WATCHER_DAILY_HISTORY_DAYS | 365 | 分析窗口，允许 90～7300 天 |
| STOCK_WATCHER_MARKET_PROVIDERS | tencent,sina,baostock,tushare,eastmoney | 逗号分隔的行情源顺序 |
| STOCK_WATCHER_MARKET_REQUEST_INTERVAL | 1s | 单个行情源请求间隔 |
| STOCK_WATCHER_MARKET_ATTEMPT_TIMEOUT | 60s | 单个行情源整窗尝试超时 |
| STOCK_WATCHER_MARKET_FAILURE_THRESHOLD | 3 | 连续失败后暂时跳过该源 |
| STOCK_WATCHER_MARKET_FAILURE_COOLDOWN | 5m | 跳过后的恢复探测间隔 |
| STOCK_WATCHER_MARKET_TENCENT_ENDPOINT | 腾讯公开端点 | 腾讯日线端点覆盖，供代理或本地测试 |
| STOCK_WATCHER_MARKET_SINA_ENDPOINT | 新浪公开端点 | 新浪复权因子/行情端点覆盖 |
| STOCK_WATCHER_MARKET_BAOSTOCK_ADDRESS | public-api.baostock.com:10030 | BaoStock TCP 地址 |
| STOCK_WATCHER_MARKET_TUSHARE_ENDPOINT | https://api.tushare.pro | Tushare 端点覆盖 |
| STOCK_WATCHER_MARKET_EASTMONEY_ENDPOINT | 东方财富公开端点 | 东方财富日线端点覆盖 |
| STOCK_WATCHER_MARKET_LISTING_ENDPOINT | 新浪发行资料端点 | 上市日期查询端点覆盖 |
| STOCK_WATCHER_INTRADAY_POLL_INTERVAL | 1m | 分钟采集轮询间隔 |
| STOCK_WATCHER_INTRADAY_REQUEST_INTERVAL | 350ms | 分钟请求间隔 |
| STOCK_WATCHER_INTRADAY_WORKERS | 3 | 分钟采集并发数 |
| STOCK_WATCHER_INTRADAY_CALIBRATION_AT | 15:10 | 每日分钟数据校准时间 |
| STOCK_WATCHER_INTRADAY_TENCENT_ENDPOINT | 腾讯分钟端点 | 分钟行情端点覆盖 |
| TUSHARE_TOKEN | 空 | Tushare Token；缺失时跳过 Tushare |
| STOCK_WATCHER_LOG_DIR | logs | 相对路径按当前工作目录解析 |
| STOCK_WATCHER_LOG_MAX_MB | 10 | 单文件大小上限 MB，整数 1～1000 |
| STOCK_WATCHER_LOG_MAX_DAYS | 7 | 轮转日志保留天数，整数 1～1000 |
| STOCK_WATCHER_LOG_BACKUPS | 10 | 轮转文件保留份数，整数 1～1000，gzip 压缩 |
| STOCK_WATCHER_LOG_JSON | false | true 输出结构化日志，false 输出可读文本 |
| STOCK_WATCHER_LOG_COLOR | auto | auto / force / off，仅控制 stdout；文件和 JSON 无颜色 |

`leaders.yaml` 必须存在且格式为 `schema_version: 1` 加 `stocks` 列表。`feishu.config` 只接受 `webhook: ...` 和可选 `secret: ...`；指定文件缺失、无读取权限或格式错误时启动失败，错误不会回显内容。优先级为环境变量 > 凭证文件。凭证文件本身不切换启用状态；无需通知时设置 `STOCK_WATCHER_FEISHU_ENABLED=false`。

## 三种加载方式

- 本地开发：复制 `.env.example` 为根 `.env`，`make run` 通过 Bash 加载后前台运行 Go。如果同仓库的 systemd 实例已运行，则先构建并校验内置配置，再停止后台服务并切换前台。前台不会自动读取 systemd 的 EnvironmentFile，所需凭证应在当前 shell 或开发 `.env` 中配置。该文件须是可信的 Shell 赋值；含特殊字符的值使用单引号。`make check/status` 和直接运行二进制不自动读取 `.env`，可在可信 shell 中先执行 `set -a; source .env; set +a`。
- systemd：模板读取 `/etc/stock-watcher.env`。使用 `KEY=value`，不加 `export`，参考 systemd EnvironmentFile 语法；Go 程序本身不读取 dotenv。
- Compose：`docker/.env` 只提供部署路径和版本；`STOCK_WATCHER_RUNTIME_ENV` 指向独立运行变量文件，Compose 用 `format: raw` 传入值。这里用不带外层引号的 `KEY=value`，`$` 等字符原样传入。不要把带 Shell 引号的开发 `.env` 直接复制过来。

私有文件设为 `chmod 600`。关闭通知的部署仍应显式设置 `STOCK_WATCHER_FEISHU_ENABLED=false`，避免误用其他服务的凭证。

## Compose 插值变量

| 变量 | 示例/默认 | 含义 |
| --- | --- | --- |
| STOCK_WATCHER_IMAGE | stock-watcher:local | 构建、启动所用镜像；发布使用明确提交 SHA 标签 |
| STOCK_WATCHER_VERSION | development | 写入二进制与镜像 revision 的版本；发布设为完整提交 SHA |
| STOCK_WATCHER_WEB_BIND | 127.0.0.1:8080 | Compose 网页的宿主机绑定地址与端口，容器内固定 8080 |
| STOCK_WATCHER_HOST_DATA | 必填 | 宿主机数据目录绝对路径；本地目录或已挂载 Windows 共享 |
| STOCK_WATCHER_HOST_POOL | 必填 | leaders.yaml 绝对路径 |
| STOCK_WATCHER_HOST_LOGS | 必填 | 已创建的本地日志目录绝对路径 |
| STOCK_WATCHER_RUNTIME_ENV | 必填 | 独立运行变量文件绝对路径 |

Compose 固定容器内数据 `/data`、股池 `/app/leaders.yaml`、日志 `/logs`，并关闭颜色。运行变量文件负责提供 `STOCK_WATCHER_FEISHU_ENABLED`、`STOCK_WATCHER_CALENDAR_*` 等测试或部署覆盖；本地目录部署保持 `STOCK_WATCHER_REQUIRE_CIFS=false`，共享盘部署设为 `true`。

`run` 同时启动监控任务和网页，默认只允许本机访问。设置 `STOCK_WATCHER_WEB_ENABLED=false` 可以关闭网页；网页没有账号或写入接口，需要远程访问时通过受控网络或已有认证反向代理开放。Compose 固定容器监听 `0.0.0.0:8080`，并用 `STOCK_WATCHER_WEB_BIND` 控制宿主机入口。应用使用根路径 `/`，反向代理应保留 `/api/` 和 `/assets/` 路径；不支持直接挂到子路径前缀。

宿主机数据路径和股池只读挂载路径必须预先存在；`init` 会在已挂载的数据根内创建数据库，但不会创建缺失的宿主机绑定路径。容器根文件系统只读，不需要 SMB 凭证或挂载权限。CIFS 由宿主机负责；启用共享盘模式时设置 `STOCK_WATCHER_REQUIRE_CIFS=true`。不要运行会展开凭证的 `docker compose config` 普通输出；用 `config --quiet` 检查。

Compose 长挂载与 raw 环境文件语义参见 [Docker 官方服务配置](https://docs.docker.com/reference/compose-file/services/)。本配置要求 Docker Compose 2.30.0 或更高版本。
