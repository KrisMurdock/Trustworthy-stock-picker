# 个股财务研究区间（v1 展示，v2/v3 研究）

财务研究区间是报告内的一项财务研究字段，用来回答“按最近四季度盈利和净资产，当前价格处于什么位置”。它不是目标价、收益预测、成功概率或买卖指令，也不参与推荐排名和推荐门槛。

## 研究状态

v1 当前继续展示，但对外统一标注为“财务研究区间”；它是工程经验基线，不是论文推导，也不是最终合理价值。v2 采用稳态 justified PB 与 `PE = PB / ROE` 交叉校验，目前只做 shadow 存储和研究评估，尚未进入公众号正文。由于首版只有 TTM/前一年 EPS，所有可计算的 v2 结果必须标记低置信度/`ttm_only`，无法计算时标记 `unavailable`。

v3 已实现 5-10 年 point-in-time 年度财务、公告日后下一交易日可见性、实际抓取日期约束、平均净资产 ROE、实际现金派息率和 `residual_income_normalized_v3` 研究区间。默认 `observed` 口径不会让后来才抓取的历史行回流到早期 as-of；这里的 as-of 是上海日历日边界，不是盘中时刻级可见性。历史重建需显式使用 `vendor_backfill`，并固定标记低置信度、不进入滚动校准。财务事实与抓取批次保存在独立 `fundamentals.db`，不修改 `market.db`；v3 目前仍不进入 `selection.Report` 或公众号正文。真实 Tushare Token 的全股池回填、滚动校准和人工复核尚未完成，因此不能把 v3 结果称为最终合理价值。方法、文献、数据契约和验收门槛见[个股估值研究](fair-value-research.md)。以下数据来源和区间公式仍描述当前展示的 v1。

## v3 研究与校准命令

```text
stock-watcher sync-fundamentals [-as-of YYYY-MM-DD] [-codes sh600519,sz000001]
stock-watcher audit-fundamentals [-as-of YYYY-MM-DD]
stock-watcher fair-value -code sh600519 [-as-of YYYY-MM-DD] [-visibility observed|vendor_backfill]
```

- `sync-fundamentals` 从 Tushare 年度 `income`、`balancesheet`、`cashflow` 和已实施 `dividend` 追加财务事实，要求配置 `TUSHARE_TOKEN`，使用独立 `.fundamentals.lock`，不会占用常驻 `run` 的主写锁；每次抓取同时保存完整批次和期间成员。
- `audit-fundamentals` 只读汇总代码数、严格 observed 可见数、vendor 回填可见数、stale 数、未来抓取期间数、期间数、抓取批次数、同步状态和问题列表。
- `fair-value` 使用本地已保存的不复权 as-of 收盘价和 PIT 财务输出标准化输入与 v3 估值，不访问外部网络；默认 `observed`，历史重建必须显式传 `vendor_backfill`，该结果带警告和低置信度，不能进入滚动校准。
- 滚动评估原语输出区间的实际覆盖率、平均宽度、方向命中、分位损失和年度/行业分层；少于最低样本时标记 `insufficient_data`。

v3 公式为 `EPS_norm=年度归母净利润中位数/最新总股本`、`BVPS=最新归母权益/最新总股本`、`ROE_norm=年度归母净利润/平均归母权益的中位数`、`g=clamp(ROE_norm×(1-派息率), -2%, 6%)`，再代入 `PB=(ROE-g)/(r-g)`、`PE=PB/ROE`。ROE 只使用相邻财年的平均净资产配对，至少需要 5 个有效 ROE（即至少 6 个年度报告）；缺少足够报表或派息证据、正盈利/净资产，或 `g>=ROE`、`r-g<1%` 时，结果必须暂缺，不补零、不退回 v1。

## 数据来源

估值快照来自新浪公司资料页的公开 JavaScript 变量文件：

```text
https://finance.sina.com.cn/realstock/company/<code>/jsvar.js
```

程序只解析下列固定标量，不执行返回内容：

| 变量 | 含义 | 用途 |
| --- | --- | --- |
| `fourQ_mgsy` | 最近四季度每股收益（TTM EPS） | PE 估值与 ROE |
| `lastyear_mgsy` | 前一年每股收益 | 盈利同比增速 |
| `mgjzc` | 最近报告每股净资产 | PB 估值与 ROE |
| `profit_four` | 最近四季度净利润（亿元） | 与总股本交叉校验 EPS |
| `totalcapital` | 总股本（万股） | 适配层换算为股后与净利润交叉校验 EPS |
| `stock_state` | 上市状态 | 只接受正常上市（`1`） |
| `papercode` / `a_code` | 股票代码 | 防止页面与请求股票错配 |

`fourQ_mgsy`、`stock_state`、股票代码和名称是必需字段；每股净资产缺失或非正时 v1 暂缺，不再退化为仅盈利倍数锚。前一年 EPS 缺失会把结果降为低置信度；四季度净利润或总股本缺失时跳过 EPS 交叉校验。解析时仍会拒绝重复字段、非有限数值、代码不匹配和非正常上市状态；当净利润和总股本都存在时，会用二者反推 EPS 与 `fourQ_mgsy` 交叉校验：绝对差超过 `max(0.10×|EPS|, 0.02)` 元时暂缺，处于 `max(0.05×|EPS|, 0.01)` 到硬阈值之间时保留原值但降为低置信度并展示告警。该数据是财报口径，不是实时行情；同日多次同步可能得到相同快照。

## 区间公式

设信号日不复权收盘价为 `P`，TTM EPS 为 `E`，每股净资产为 `B`：

```text
盈利增速 growth = E / lastyear_EPS - 1（前一年 EPS 缺失或非正时标记未知）
ROE            = E / B × 100
当前 PE        = P / E
当前 PB        = P / B
quality        = clamp(ROE - 8, -8, 25)
growth_clamped = clamp(growth, -15, 35)
```

公允倍数只按盈利质量和增速调整，不使用行业成分股中位数：

```text
非金融：FairPE = clamp(11 + 0.35 × growth_clamped + 0.45 × quality, 8, 30)
        FairPB = clamp(1.50 + 0.12 × (clamp(ROE, 0, 40) - 8), 0.80, 6.0)

金融：  FairPE = clamp(5.5 + 0.15 × growth_clamped + 0.20 × quality, 5, 10)
        FairPB = clamp(0.70 + 0.03 × (clamp(ROE, 0, 30) - 8), 0.40, 1.50)
```

银行、保险、证券、信托和多元金融按行业名称收紧倍数上限。盈利估值与净资产估值各占一半：

```text
pe_price = E × FairPE
pb_price = B × FairPB（B 非正时结果暂缺，不再退化为 pe_price）
central  = (pe_price + pb_price) / 2
width    = 0.15 + min(|growth|, 25) / 100 × 0.20
lower    = central × (1 - width)
upper    = central × (1 + width)
upside   = (central / P - 1) × 100
```

盈利增速绝对值越大，区间越宽；盈利增速缺失或 EPS 交叉校验接近容差时，`confidence` 标记为 `low`，区间宽度至少扩大到 30%。TTM EPS 非正、每股净资产非正、ROE 不在 `(0, 50%]`、当前 PB 不在 `[0.1, 20]`、价格无效或快照校验失败时不生成区间，结果只保留明确的 `reason`，并在文章中显示“暂缺”。

## 生成与保存

- `sync` 在报告计算完成后，为精排候选、正式推荐和观察排名中的前 10 只股票拉取估值，单轮总等待上限 30 秒；二阶模型产物稍后到达并重算精排时，会再次补齐最终候选的估值后再保存报告。
- 单只股票请求失败或数据不满足校验时，记录 `unavailable` 原因；不会中断行情同步、报告保存或通知投递。
- 估值结果保存在 `selection.Report.fair_values`，随报告一起写入 `market.db`。字段是可选扩展，不改变现有报告 schema 版本。
- 公众号文章只读取已保存报告中的估值快照，不在排版或投递时重新请求外部数据。
- `preview` 和 `replay` 不访问估值源；`replay` 会复用已冻结报告中的估值快照，`preview` 会复用同日已保存报告中的估值快照。
- 日常“每日精选”和盘前观察文章都会展示区间；当候选较多进入紧凑排版时，只保留区间上下限，低置信度会追加“（低）”，避免超过公众号正文长度限制。

文章字段包括区间上下限、中枢、相对收盘价幅度，以及 TTM EPS、每股净资产、ROE、盈利同比、当前 PE/PB 和估值方法说明。

## 已知限制

- 模型是固定锚点的 PE/PB 相对估值，不是 DCF，也没有使用同业可比公司中位数。
- 新浪 `jsvar.js` 不提供财报截止日和公告日，只有抓取时间；因此历史 `sync -as-of` 不能保证严格 point-in-time，历史报告应把估值视为“抓取时点快照”而不是当时可见的财报数据。
- 估值最多覆盖文章前 10 只候选，与当前生产 `second_stage.top_n=10` 对齐；如果把 TopN 调高，超出估值上限的候选会显示“暂缺”，不会编造区间。
- 财报数据存在披露滞后，无法反映最新订单、价格、政策、商誉减值或一次性损益。
- 周期股的峰值盈利会放大估值；金融股的净资产质量和坏账周期也无法由该公式识别。
- 股本变化、会计口径调整和不同报告期合并范围会使 EPS、ROE 和 PB 失真。
- 区间宽度是增速不确定性的简化表达，不是统计置信区间，不承诺价格会落在区间内。
- 任何区间都不构成投资建议；推荐资格仍由既有历史证据、风险门槛和结构门控决定。
