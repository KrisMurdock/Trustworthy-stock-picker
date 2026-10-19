# 行情来源与同步契约

`stock-watcher` 使用 Go 原生日线客户端，按 `market.providers` 顺序取第一个通过校验的完整窗口。默认 `tencent, sina, baostock, tushare, eastmoney`；`eastmoney` 是 AKShare `stock_zh_a_hist` 的实际上游，配置名不使用 `akshare`，以免把直接 HTTP 访问误认为运行了 AKShare 库。生产不安装 Python SDK 或额外的数据服务。

| 配置名 | 协议与接口 | 复权 | 原始成交量 |
| --- | --- | --- | --- |
| `tencent` | 腾讯 HTTP，复用原客户端和 640 根分页 | none / qfq / hfq | 沿用原协议：科创板为股，其余为手 |
| `sina` | AKShare 新浪日线上游，Go 原生 KLC2 解码与复权因子 | none / qfq / hfq | 股 |
| `baostock` | 官方 0.9.4 TCP 协议，匿名登录、压缩帧、250 根分页 | 3 / 2 / 1 映射为 none / qfq / hfq | 股 |
| `tushare` | 官方 REST `daily` + `adj_factor` | 原价；前复权乘当日因子/窗口末日因子；后复权乘当日因子 | 手 |
| `eastmoney` | AKShare 所用的东方财富日线 HTTP 接口 | fqt 0 / 1 / 2 | 手 |

Tushare 按自然年请求，以避免长窗口遇到单次条数上限；拉完各年后统一复权基准，因子缺失时拒绝窗口。BaoStock 按自然年独立登录并查询，每页最多 250 根，分帧读取并解压，结束或取消时关闭连接；停牌合成行不参与分析。小分页用于降低公共端点大响应截断与长跨度分页超时的影响。东方财富单窗口请求最多 6000 根，达到条数上限视为可能截断而拒绝。

## 配置与凭证

```yaml
market:
  providers: [tencent, sina, baostock, tushare, eastmoney]
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

1. 同一窗口依次尝试来源，每个来源尝试一次。单请求超时由 `request_timeout` 控制，每源间隔由 `market.request_interval` 控制；多页请求同时受 `market.attempt_timeout` 和调用者取消信号控制。冷却中的来源暂时跳过，详见下文。
2. 校验股票代码、日期、正价格、OHLC、成交量、复权字段、日历和历史覆盖。HTTP 成功但数据不可用同样回退。
3. 前复权/后复权窗口完整刷新。不复权增量只在来源、单位、复权字段及重叠日线全部一致时合并；切换来源或发现重叠修订会完整回补。
4. 复权分析窗口与十年原始窗口分别回退，各自记录实际来源。不会把一个窗口内的部分日期从不同来源拼接，也不会平均多个来源的价格。本次不进行多源交叉验证。
5. 单只股票尝试全部来源后仍失败时，记录并跳过，继续同步后续股票；包括网络超时、限流、权限不足和数据校验失败。整轮结束后保存有效数据，推荐仍要求全池 95% 覆盖率。任务取消、整体截止时间到期或共享盘出错则立即停止，不当作单股失败跳过。
6. 取消、超时与连接关闭可验证；共享盘出错不通过替换来源重试，不落到服务器本地磁盘，不因回退发送额外飞书消息。

腾讯当日日线使用不指定结束日期的最新窗口，避免历史日期查询在收盘后仍只返回上一交易日；解析结果仍限制在请求日期内。供应商额外附加当日行时，仅保留请求根数的最新窗口，历史分页继续按明确日期查询。是否可以使用当日日线仍由上层收盘时间与完整性检查决定。

历史 JSON 保持 `schema_version: 1`、UTF-8、ISO 日期和 `volume_unit: provider_raw`，`source` 支持表中五个允许值，`data_field` 仍是 `day/qfqday/hfqday`。当前框架使用 SQLite schema 4，写入入口自动迁移 schema 2/3，不需要清空库。入库时分别保留原始量和按来源换算的股数；历史读取拒绝来源或复权字段不一致的混合窗口。旧版本只识别腾讯，回滚旧二进制前需要恢复相应旧数据快照。

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

## 新浪备用日线与故障源保护

新增 `sina` 原生 Go 客户端，对应 AKShare 的 `stock_zh_a_daily` 上游。直接读取
`https://finance.sina.com.cn/realstock/company/<code>/hisdata_klc2/klc_kl.js`，
在 Go 内解码 KLC2 日线；前/后复权分别读取 `qfq.js`、`hfq.js`，按日期匹配因子，
前复权除以因子，后复权乘以因子，OHLC 保留两位小数，成交量为股。
不运行 Python、JavaScript 引擎或外部数据服务，也不执行响应中的代码。
未知编码、缺失/非法因子、错误股票标识、截断和无效 OHLCV 均触发回退。
新浪返回上市以来的完整流，客户端保留请求窗口及边界锚点；不能用返回的首日直接推断上市日期。

新的默认顺序为 `tencent → sina → baostock → tushare → eastmoney`。
**已有配置显式写了 `market.providers` 时不会自动插入来源**，需在合并部署时修改该列表：

```yaml
market:
  providers: [tencent, sina, baostock, tushare, eastmoney]
  request_interval: 1s
  attempt_timeout: 60s
  failure_threshold: 3
  failure_cooldown: 5m
```

- `attempt_timeout` 限制同一来源整个窗口的网络用时，包括分页、增量失败后的全量请求；允许 10 秒至 5 分钟，默认 60 秒。源内预算到期可回退；调用者取消或整体超时仍立即中止。共享盘读取使用调用者上下文，磁盘故障不会被改写成行情源超时。
- 连续网络、限流或可重试服务错误达到 `failure_threshold` 后，整个日线实例暂时跳过该来源。阈值允许 1–20，默认 3；`failure_cooldown` 允许 1 分钟至 1 小时，默认 5 分钟。
- 冷却到期仅放行一个恢复探测；再次失败继续冷却。有效响应或单股数据校验错误会清除网络失败计数；历史不足、停牌缺口不算整个源不可用。冷却状态仅在进程内，重启后重新探测。
- 日志记录每次失败耗时、进入冷却与恢复时间；隔离股票保留各来源原因，不再只显示泛化的 `invalid_data`。没有任何来源可用时继续下一只股票，整轮保存通过校验的数据，推荐仍须满足 95% 门槛。

`source: sina` 与现有 schema 兼容，无数据库结构迁移；SQLite 中原始量和股数一致，网页显示“新浪 / 成交量单位：股”。回退到不认识 `sina` 的旧二进制前，应恢复匹配的数据快照。分钟数据仍使用独立的腾讯路径，本次新浪接入只覆盖日线。

解码回归使用公开历史响应与独立参考解码器生成的全字段摘要；测试不依赖网络。
`make sources-smoke` 额外覆盖新浪回退、复权、空库写入和已有库增量更新；
`make dashboard-smoke` 验证真实浏览器中的新浪来源与成交量单位。

参考：[AKShare 新浪日线实现](https://github.com/akfamily/akshare/blob/main/akshare/stock/stock_zh_a_sina.py)、
[编码参考](https://github.com/akfamily/akshare/blob/main/akshare/stock/cons.py)。

### 2026-10-16 服务器实测

以已收盘的 2026-10-15 为截止日：新浪对全池 104 只股票的 `none/qfq` 共 208 个窗口均返回通过 OHLCV、日期和复权解析校验的数据，最新日均为 10 月 15 日；单窗口耗时中位数约 1.51 秒，最大 3.71 秒（含每秒一次的限速等待）。另对 10 只代表股票重复请求三轮，30/30 成功；对 5 只股票最近 20 根 `none/qfq` 与腾讯交叉核验，OHLC 一致，统一股数最大差异 49 股。后复权另抽样 5 只，均通过解析校验。

同期东方财富按 AKShare 当前源码参数重复 3 次，均在连接阶段被重置；加常见请求头也没有改善。故选择增加独立的新浪上游，而非把同一东方财富请求再包装为 AKShare。

这是一台服务器在有限时段的观测，不代表公共接口长期可用性承诺。208 个窗口的成功也**不等于推荐覆盖达标**：其中 44 只股票返回的历史不足十年，需要独立核对上市日期，或确认是否存在缺失历史。当前同步已加入下述自动上市日期校验，旧观测不代表新规则的整池验收结果。不得直接把供应商首条日线当作上市日期，亦不得为发布推荐而降低 95% 门槛。

## 上市日期与十年历史边界

每只股票下载价格前，按以下顺序确认上市日期：

1. 股池中已核实的 `listed_from`，可覆盖自动结果。
2. `<data_dir>/metadata/listings/<code>.json` 中已校验的上市日期缓存。
3. 新浪发行资料的“上市日期”字段，默认地址为 `https://vip.stock.finance.sina.com.cn/corp/go.php/vISSUE_NewStock/stockid/<数字代码>.phtml`，可通过 `market.listing_endpoint` 覆盖前缀。

查询独立于 `market.providers` 的价格来源顺序，不需要 Token，使用纯 Go 解析 UTF-8 / GB18030 页面，并核对标题中的证券代码和唯一的上市日期字段。发行日期、首条 K 线日期均不能替代上市日期。示例：[联讯仪器发行资料](https://vip.stock.finance.sina.com.cn/corp/go.php/vISSUE_NewStock/stockid/688808.phtml)。

以已收盘日为截止日，不复权历史的请求起点为 `max(上市日期, 截止日前十个自然年)`；闰年 2 月 29 日向前十年时取 2 月 28 日。例如截止 2026-10-15，2002-04-09 上市的股票从 2016-10-15 起下载，2026-04-24 上市的从 2026-04-24 起下载。上市日晚于截止日时跳过，不请求上市前价格。旧的更早日线不主动删除，休市边界仍可保留之前一根日线作覆盖锚点。新浪端点返回完整历史流，Go 客户端在解码后按请求窗口截取；服务端不能按日期裁剪，因此这项规则限制的是同步窗口，不保证上游传输字节数随之减少。

`daily.history_days` 仍兼容 90–7300 天配置，有效分析起点同样不早于上市日和十年前，不再因超长分析窗口扩大原始下载跨度。原有日历、停牌缺口、分页完整性及 95% 推荐覆盖率校验继续生效；上市日期正确不代表后续行情必然完整。

元数据使用版本 1 JSON，保存证券代码、ISO 上市日期、实际来源 `sina_ipo` 和带时区的抓取时间；重启后复用，不随日线重复查询。日期为固定事实，不定时过期；需要纠正时优先配置已核实的 `listed_from`，或停止写入进程后移除对应缓存再重新查询。缓存错误版本、代码不匹配、内容损坏、共享盘读写失败均中止同步，不能退回服务器本地目录。

元数据请求复用 `request_timeout`、`market.request_interval`，采用独立的 `sina_listing` 故障计数，连续可重试错误达到 `failure_threshold` 后按 `failure_cooldown` 冷却，不影响新浪价格源。单票查询超时、403、无效页面或冷却时记录 `listing_lookup` 隔离原因并继续；已有缓存或显式日期的股票仍可同步。任务取消立即停止。`make sources-smoke` 在真实 CLI 和临时 SQLite 上验证首次查询、缓存跨进程复用、权限失败跳过、旧数据保留；容器模式使用同一组断言。

2026-10-16 使用 Go 客户端逐股查询现有 104 股，104/104 获得有效上市日期；以 2026-10-15 为截止，44 股不足十年、60 股已满十年。44 股的上市日期与此前独立采集的原始历史首日一致。此检查验证上市元数据及边界归因，不替代完整同步、停牌缺口与推荐门槛验收。

分钟线目前仍只有已验证的腾讯原生 5 分钟源，15/30/60 分钟由相同原始序列聚合；日线多源回退不覆盖分钟线。请求／展示上限为最近 640 根，研究按日期区间读取本地已积累数据（最多 20 年），不会补出未曾下载的分钟历史。实验整体快照仍有 32 MiB 编码上限，超过时明确拒绝；长范围研究应缩小股池或已同步的分析窗口，不能把截断数据当作完整输入。`/api/status` 明确显示分钟来源及及时更新的股票数；不能把日线健康当成分钟源健康。

个股历史停牌尚无独立权威状态数据，未知日期缺口继续隔离，不能因可能停牌而放宽完整性验证。自动停复牌识别和分钟备用源属于后续数据接入工作，需要先验证来源、单位、时间戳和修订规则。
