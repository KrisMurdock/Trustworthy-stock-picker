# 行情来源与同步契约

`stock-watcher` 使用 Go 原生日线客户端，按 `market.providers` 顺序取第一个通过校验的完整窗口。默认 `tencent, baostock, tushare, eastmoney`；`eastmoney` 是 AKShare `stock_zh_a_hist` 的实际上游，配置名不使用 `akshare`，以免把直接 HTTP 访问误认为运行了 AKShare 库。本次不安装 Python SDK 或额外的数据服务。

| 配置名 | 协议与接口 | 复权 | 原始成交量 |
| --- | --- | --- | --- |
| `tencent` | 腾讯 HTTP，复用原客户端和 640 根分页 | none / qfq / hfq | 沿用原协议：科创板为股，其余为手 |
| `baostock` | 官方 0.9.4 TCP 协议，匿名登录、压缩帧、250 根分页 | 3 / 2 / 1 映射为 none / qfq / hfq | 股 |
| `tushare` | 官方 REST `daily` + `adj_factor` | 原价；前复权乘当日因子/窗口末日因子；后复权乘当日因子 | 手 |
| `eastmoney` | AKShare 所用的东方财富日线 HTTP 接口 | fqt 0 / 1 / 2 | 手 |

Tushare 按自然年请求，以避免长窗口遇到单次条数上限；拉完各年后统一复权基准，因子缺失时拒绝窗口。BaoStock 按自然年独立登录并查询，每页最多 250 根，分帧读取并解压，结束或取消时关闭连接；停牌合成行不参与分析。小分页用于降低公共端点大响应截断与长跨度分页超时的影响。东方财富单窗口请求最多 6000 根，达到条数上限视为可能截断而拒绝。

## 配置与凭证

```yaml
market:
  providers: [tencent, baostock, tushare, eastmoney]
  request_interval: 1s
  tushare_token_env: TUSHARE_TOKEN
```

Token 仅从指定环境变量读取。未配置时跳过 Tushare；若它是唯一来源，则配置校验失败。配置实际 Token 的私有 `.env` 不入库；`make run` 已支持加载该文件，systemd 使用 `EnvironmentFile`，Docker Compose 使用 `STOCK_WATCHER_RUNTIME_ENV` 指定的环境文件。使用 `./bin/stock-watcher check -config config.yaml` 可查看 `market_sources`，该检查确认配置与存储可用，**不探测远端权限或网络**。

默认端点：

- 腾讯：`https://proxy.finance.qq.com/ifzqgtimg/appstock/app/newfqkline/get`
- BaoStock：`public-api.baostock.com:10030`，需要可出站访问该 TCP 端口。
- Tushare：`https://api.tushare.pro`
- 东方财富：`https://push2his.eastmoney.com/api/qt/stock/kline/get`

可使用示例配置中的 `*_endpoint` / `baostock_address` 覆盖端点。HTTP 端点只接受 HTTPS，回环地址允许 HTTP 供本地测试；禁止 URL 内嵌凭证、查询参数和重定向。所有响应体、TCP 解压和页数都有大小上限，供应商原始错误正文不写日志。

## 回退与数据一致性

1. 同一窗口依次尝试来源，每个来源尝试一次。单请求超时由 `request_timeout` 控制，每源间隔由 `market.request_interval` 控制；多页请求依次执行，受调用者取消信号控制。
2. 校验股票代码、日期、正价格、OHLC、成交量、复权字段、日历和历史覆盖。HTTP 成功但数据不可用同样回退。
3. 前复权/后复权窗口完整刷新。不复权增量只在来源、单位、复权字段及重叠日线全部一致时合并；切换来源或发现重叠修订会完整回补。
4. 复权分析窗口与十年原始窗口分别回退，各自记录实际来源。不会把一个窗口内的部分日期从不同来源拼接，也不会平均多个来源的价格。本次不进行多源交叉验证。
5. 全部来源均为永久错误/数据错误时隔离股票；任一来源出现未被备用源恢复的网络/限流错误时，中止本轮发布。若只有数据隔离，其他有效数据仍落库，推荐继续要求 95% 覆盖率。
6. 取消、超时与连接关闭可验证；共享盘出错不通过替换来源重试，不落到服务器本地磁盘，不因回退发送额外飞书消息。

历史 JSON 保持 `schema_version: 1`、UTF-8、ISO 日期和 `volume_unit: provider_raw`，`source` 扩展为四个允许值，`data_field` 仍是 `day/qfqday/hfqday`。新增来源均使用既有 SQLite schema 2，无需迁移或清空库。入库时分别保留原始量和按来源换算的股数；历史读取拒绝来源或复权字段不一致的混合窗口。旧版本只识别腾讯，回滚旧二进制前需要恢复相应旧数据快照。

`/api/dashboard` 新增 `stocks[].source` 与 `stocks[].bar.volume_shares`，兼容原有 `volume` 字段。网页股池表以股显示并排序；日 K 显示原始量，并明确标注来源及股/手。

## 验证与参考

`make sources-smoke` 使用本地 HTTP/TCP 模拟服务验证 CLI 初始化、来源回退、复权、SQLite、缺 Token、权限与限流；取消、超时和分页由 Go 集成测试覆盖。容器模式可运行 `python3 tests/sources_smoke.py --image stock-watcher:<提交 SHA>`。测试只使用临时目录和测试 Token，不触达生产共享盘或飞书。

- [BaoStock 官方发行包](https://pypi.org/project/baostock/0.9.4/)：TCP 协议参考官方客户端 `loginout.py`、`history.py`、`socketutil.py`，未引入 Python 运行时。
- [Tushare HTTP 调用](https://tushare.pro/document/1?doc_id=40)、[daily](https://tushare.pro/document/2?doc_id=27)、[adj_factor](https://tushare.pro/document/2?doc_id=28)。前后复权的接口权限与账户额度以官方说明为准。
- [AKShare 东方财富客户端源码](https://github.com/akfamily/akshare/blob/main/akshare/stock_feature/stock_hist_em.py)：Go 客户端按同一上游协议实现，实际来源命名为东方财富。

## 分钟数据

分钟路径与本页的日线回退链独立。首版通过 Go 直接访问腾讯 `appstock/app/kline/mkline`，请求 `m5`；15/30/60 分钟从原始 5 分钟线按沪深交易时段聚合。没有调用 Python、BaoStock 日后发布数据或 Tushare 付费分钟权限来冒充实时回退。腾讯分钟接口不可用时保存失败状态，保留已保存数据，并在下一轮重试；目前没有备用分钟源。

原始价格不复权；时间为 Asia/Shanghai 的周期结束时刻，当前形成中的一根允许保存并明确标注，后续轮询覆盖更新。校验代码、顺序、唯一时间、交易时段、OHLC、有限数值和成交量；不补造缺根、不跨午休聚合、不拼接其他来源。日线的来源顺序与成交量换算不套用到分钟数据。

分钟路径复用 `request_timeout`，使用单独的 `intraday.request_interval` 限速；每股每轮一次请求，最多 `intraday.workers` 个并发，取消会中断网络、排队和数据库写入。HTTP 403/429、超时或无效响应只记录稳定错误原因，不输出原始响应；有效但落后的数据标为 stale，缺根或最后一根待确认标为 partial，均不计作盘后校准成功。供应商返回较旧窗口不会删除本地已观察到的更新 K 线。

默认盘中 1 分钟轮询、15:10 全日校准，非交易时段失败/不完整股票每 5 分钟重试。初次/校准最多请求 640 根，盘中请求 64 根；超出历史窗口的缺口不能靠该接口补齐。盘后校准要求当前交易日完整的 48 根已完成 5 分钟线，停牌或上游缺根股票会保持未完成状态。分钟同步成功不代表日线推荐覆盖率满足要求。
