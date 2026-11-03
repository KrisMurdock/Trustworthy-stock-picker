# 全市场四灯波段研究

`cmd/four-light-study` 在冻结的沪深全市场 SQLite 备份上，逐股票、逐信号日评价四灯事件与买入后的上涨波段。它取消原排名和 Top N 筛选，沿用开盘模拟入场；当前 manifest v2 使用 [`upward_wave_no_drawdown_v1`](upward-wave-evaluation.md)，取消盘中先跌和收盘回撤否决，保留两项描述。五日为主指标，十日独立报告。旧 manifest v1 继续重放 `upward_wave_v0`，不会静默改变旧结果。

四灯公式仍为 `four_light_approx_v1`，保持近似、未对齐商业软件的标记。工具不下载行情、不写实验库或全市场信号归档、不改变排名或发送通知。输出是研究结果，不是选股推荐。

## 冻结输入

输入由独立 `all-market.db` 备份和 manifest JSON 组成。先使用 SQLite 备份接口获得一致的独立文件；也可以停止全部写入、关闭连接后复制完整数据库。不能直接复制正在写入的 SQLite 主文件，也不要把生产原库当研究输出目录。

备份必须是普通文件，使用 `DELETE` journal mode，不携带 `-journal`、`-wal` 或 `-shm`。工具只读支持 all-market schema 1、2、3，既不迁移旧库，也不创建缺失库。打开后保持单个只读事务，先取得 SQLite 读快照，再计算整个文件的 SHA256 并与 manifest 核对；源库被替换、摘要不一致或版本不支持均立即失败。

manifest 最大 1 MiB，只允许一个 JSON 对象，未知字段拒绝。必填字段如下：

| 字段 | 定义 |
| --- | --- |
| `schema_version` | 新研究使用 `2`；兼容 `1` 的旧口径 |
| `wave_model` | v2 必填 `upward_wave_no_drawdown_v1`；v1 不得指定 |
| `pairing_policy` | v2 必填 `causal_affine_ohlc_v1`；v1 不得指定 |
| `database_sha256` | 冻结 SQLite 完整文件的小写 SHA256 |
| `as_of` | 冻结研究输入的截至日 |
| `from` / `through` | 包含两端的信号日范围 |
| `observation_through` | 最后允许读取和评价的价格日期，包含当日 |
| `reserved_from` | 预留区间起点，禁止读入和评价当日及之后的价格 |
| `universe_policy` | 固定 `current_active_sh_sz` |
| `reference_experiment_id` | 可选，记录原开发划分所依据的实验 |
| `calendar` | 已冻结 `experiment.Calendar`，包含 schema、覆盖、完整交易日数组及来源、摘要 |
| `parameters` | 已冻结的完整 `config.Strategy` 参数，包含成交、费用、滑点与预算配置 |

要求 `from ≤ through ≤ observation_through < reserved_from ≤ as_of`，边界必须属于冻结交易日历。预热从 `calendar.from` 开始，因此应保留至少 60 根信号前日线及 ATR 所需数据。`calendar` 和 `parameters` 应从原冻结实验输入复制，不能随运行时默认配置变化；完整字段可参考 `internal/fourlightstudy/contract.go`。

沿用原开发期时，信号范围仍为 `2026-04-08`～`2026-08-11`，预留起点仍为 `2026-08-20`；观察上限最多为该起点前一个冻结交易日。不能为凑足十日窗口向后读取预留价格，也不能为了提高覆盖把临近边界的信号从分母删除。备份可以含更晚行情，摘要计算覆盖全部字节，但价格查询严格截止于 `observation_through`。

## 运行与输出

```bash
go build -ldflags "-X main.version=$(git rev-parse HEAD)" -o /tmp/four-light-study ./cmd/four-light-study

/tmp/four-light-study \
  -manifest .scratch/four-light-study/manifest.json \
  -database .scratch/four-light-study/all-market.snapshot.db \
  -events .scratch/four-light-study/events.jsonl \
  -timeout 10m \
  > .scratch/four-light-study/report.json
```

manifest 和 database 必填；events 可省略。默认总超时 10 分钟，允许大于零且不超过 1 小时；SIGINT、SIGTERM、取消或超时都会中止。错误写 stderr，并以非零状态退出；stdout 只有完整成功报告。命令重定向可能留下空报告文件，应以退出状态和 JSON 内容一起判断成功。

v2 可额外成对传入 `-classification` 与 `-classification-sha256`，输出独立的 `sector_context`，保持基础 `report` 不变。分类来源、可见日期、同日同行业基准和两个独立先验强弱诊断的契约见 [同行业对照](four-light-sector-context.md)。不传入分类时不启用该分析。

在上述分类输入上增加 `-trigger-timing`，可独立诊断首次全红前的价格三灯红龄与截至前一天的5/20日价格路径，输出 `trigger_timing`；原基础及行业报告不变。口径见[触发时点诊断](four-light-trigger-timing.md)。

events 必须是新路径，禁止覆盖任何输入或已有文件。事件先写同目录临时文件，分析和报告序列化成功后才原子发布；发布不会覆盖运行期间被其他进程创建的文件。失败会清理本轮临时文件。输出目录需支持同目录硬链接；这也是本工具应使用独立本地研究目录的原因之一。

输出 JSON 包含 `code_version`、`manifest_file_sha256`、`database_sha256`、`database_schema` 及 `report`。报告保留完整 manifest、逐组/逐日/逐月统计、同日对照和数据审计。v2 报告 schema 为 2、引擎为 `four_light_market_wave_v2`，还输出板块分组、来源组合、候选保留率，以及候选相对未过滤首次全红的同日差值。代码、manifest 与数据库摘要一起标识可复现输入。

events JSONL 仅输出当前全红的股票日，一行一个 `code/date`，包含分组、冻结四灯证据和两个窗口的波段路径。顺序固定为代码、日期；同一行可以属于多个组，不能把这些组加总为独立交易。

## 分组与基准

| 组 | 口径 |
| --- | --- |
| `baseline` | 同一当前股池、同一信号日，四灯当前状态可判的全部股票日 |
| `first_all_red` | 当前全红、上一交易日状态已知且未全红；主研究对象 |
| `all_red` | 当前全红，包括首次、持续及转换未知 |
| `continuing_all_red` | 上一交易日和当前均全红 |
| `flip_all` | 四灯上一交易日均绿、当日均红；首次全红的子集 |
| `first_energy_only` | 首次全红且只有量能灯由绿转红 |
| `first_price_involved` | 首次全红且至少一个价格灯由绿转红 |
| `all_red_transition_unknown` | 当前全红，但上一交易日缺失或转换不可判 |
| `non_all_red` | 当前四灯可判且未全红 |
| `first_moderate_volume` | v2：首次全红且信号日量比 ≥1.2、<2；基础报告的量比候选 |
| `first_high_volume` | v2：首次全红且量比 ≥2；候选的互补诊断组 |

`baseline` 包含信号组，故差值是相对当日可评价市场集合的描述，不是信号组与互斥控制组的因果效应。明确未入场、入场未知和路径未知保留在各自漏斗中，不事先按未来可成交或未来数据完整性删除股票日。

数据加载按 `(code, adjust, trade_date)` 索引分别读取前复权与原价，检查日期是否属于冻结交易日历，以及数值、来源和字段一致性。每种口径只隔离首个问题日期及其后缀，保留此前可验证的完整前缀，并记录股票代码、日期和原因；后续坏数据不能反向删除较早的信号或已知入场。缺原价时仍保留可判的四灯事件，对需要缺失价格的入场或未来窗口记未知；前复权缺失时不能生成该日确定灯色。旧 v1 若 qfq/raw 来源不同会把原价全部设为未知；v2 移除这个来源名门槛，并按下面的成对规则核验。混源、缺数、预热不足、上市日期缺失及转换未知均有审计；非法股票元数据、数据库读取错误和取消属于运行失败。

## v2 行情配对与候选契约

四灯及量比继续读取源行情原有成交量，保持 `four_light_approx_v1` 的精确算术。配对/成交评价复制一份 K 线，将成交量替换成数据库的 `volume_shares`。读取时先验证股数与来源单位及存储整数截断规则一致；腾讯科创板、新浪、BaoStock 为股，腾讯其余板块、Tushare、东方财富为手。股数元数据不一致只使该日期的评价成交量不可用，并保留原始四灯行情；不会因为归一股数损坏而删除信号分母。原始价格、日期、来源口径不合法仍隔离其后缀。

`causal_affine_ohlc_v1` 使用信号日之前（含当日）末尾最多 5、至少 3 根同日期 OHLC；每对成交股数差不超过 100 股。比较平移、比例、正斜率仿射候选，选择近期证据最大残差最小的兼容参考；差异不超过 0.000001 元时按上述顺序选择更简单的形式。双方价域不足 0.20 元时记 `pairing_insufficient_price_range`；候选拟合后的复权价域还须达到 20 倍舍入边界，否则该候选不成立，全部候选失败时记 `pairing_transform_inconsistent`。价格容差为 `0.005 × (1 + scale) + 0.000001` 元，表示双方分币舍入的误差预算。整个近期证据必须兼容，近期已改变的映射不会借用未来数据修复。

逐笔 `price_pair_reference` 保存规则、映射、系数、容差、证据起止/根数、价域及最大残差。未来只验证该固定参考，不重新拟合。日期、OHLC、股数异常和价格关系改变分别记录未知；D6 的异常不会污染完整五日窗口。信号时配对不明记入场未知，入场后配对不明保留确认入场并记路径未知。来源相同也必须验证，来源不同不再自动失效。

这是保守的数值一致性规则：固定 0.20 元证据范围会降低低价/低波动股覆盖；有限证据与舍入可能混淆平移和比例关系，外推失配可能把稳定关系判为未知，小于容差的变化也可能无法识别。`price_transform_changed` 不等于已经证明公司行为，更不等于已实现分红拆股现金流。现有 qfq 是事后冻结的数据版本，不能保证为信号日当时可见的复权版本。

候选量比 = 信号日成交量 / 前五根成交量均值，计算次序与量能灯一致；恰好 1.2 纳入候选，恰好 2 归互补组。候选归属仅使用信号日前数据，不使用 D1 后的涨跌、是否成功或数据完整性过滤。原四灯颜色与生命周期不变。

`candidate_retention` 的 `selected_fraction` 为候选信号数/全部首次全红，`known_fraction` 为候选有效数/全部首次全红有效数，`effective_coverage` 为候选有效数/候选信号数，`entry_unknown_fraction` 为候选入场未知数/候选信号数。它们与旧 `coverage=known/entered` 不同。

`boards` 按科创板、创业板、沪市主板、深市主板代码前缀报告同样的漏斗。`pair_sources` 记录各板块当前股票数及来源组合，不表示这些股票的所有路径均有效。`comparisons` 仍相对市场基准；`candidate_comparisons` 显式 `reference=first_all_red`，对双方已知窗口在同一信号日的波段率做等权差。候选包含于参照组，因此不是独立的控制实验。

## 统计解释

五日和十日各自给出成功/失败/未知、未入场、有效覆盖率、有效样本波段率、确认入场范围内的保守波段率，以及启动、持续、回撤和期末回吐。空分母为 `null`，不写成 0%。

同时报告每个信号日的分组率与同日基准率，再对双方 `known > 0` 的日期做等权差值，避免某天信号特别多主导全部结论。保留未能配对的日期数。`mean_difference`、`lower_95`、`upper_95` 是比率差，乘以 100 才是百分点。描述性区间使用固定种子 `20261102`、10 个交易日移动块、2,000 次重采样，完整日期网格保留未知日；相邻股票事件和同日股票仍然相关，区间不能被理解为独立交易检验。少于 20 个交易日、少于 2 个配对日或有效重采样不足时，区间为 `null`，原因见 `interval_status`。

研究范围是备份中当前 `active=1` 的沪深 A 股，存在当前股池与存活者偏差；它不是重建了每个历史交易日的真实全市场。历史 ST 身份未完整冻结、日线无法证明开盘真实可成交，公司行为尚未做完整现金流调整，均限制结论。

波段标签使用未来路径，因此只能在事后评价。费用后的收盘标记、区间最高点与回吐不是实际卖出成交或已实现收益，也不能回灌为当天选股特征。分组重叠、多种分组的开发期比较以及重复观察均不构成独立验证；本轮继续封存原预留区间，不能因某个分组率较高就宣称策略通过或全市场实盘可用。

本次冻结开发期的实际数据与覆盖变化见 [v2 全市场对照结果](four-light-wave-v2-results.md)。

价格三灯连续全红段内的首次与再次触发诊断见[价格段研究](four-light-price-episodes.md)，通过 `-price-episodes` 单独启用。

五日启动与20/40/60日延续可通过 `-wave-horizons` 研究，口径见[长期波段诊断](four-light-wave-horizons.md)；新窗口仅描述，不设回撤或成功门槛。
