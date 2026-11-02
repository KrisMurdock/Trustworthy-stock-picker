# 个股估值研究：v1 基线、文献约束与 v2 设计

本文固化个股估值方法的研究结论、v2 目标公式、数据契约和发布边界。它不表示 v2 已经进入生产链路，也不构成目标价、收益预测、成功概率或买卖建议。

## 研究结论

- v1 是基于工程经验的 PE/PB 相对估值区间，不是论文推导，也没有经过行业、周期和参数的系统校准。它可以继续作为“财务研究区间”展示，但不能称为最终合理价值。
- v2 以稳态 justified PB 为核心，并用 `PE = PB / ROE` 强制保持盈利倍数和净资产倍数一致；它只是稳态交叉校验，不是完整 DCF。
- v2 首版只有 TTM EPS、前一年 EPS 和当前每股净资产，标准化盈利与严格 point-in-time 数据尚未具备，因此只能 shadow 存储，所有结果标记低置信度/`ttm_only`，不进入公众号正文。
- 只有完成 5-10 年标准化财务数据、公告日约束和滚动 walk-forward 校准后，才讨论把 v2 升格为正式估值字段。

## v1：工程经验基线

v1 的目标是回答“按最近四季度盈利和净资产，当前价格处于什么位置”。它使用固定的公允 PE/PB 公式，再把两个锚点价格各占一半得到中枢和区间；这些参数来自工程经验，不是论文推导或样本外估计。

### 具体问题

1. **PE/PB 独立计算再平均，隐含 ROE 不一致。** v1 分别计算 `pe_price = E × FairPE` 和 `pb_price = B × FairPB`，再取价格平均。两个锚点只有在 `FairPB / FairPE = E / B` 时才隐含同一个 ROE；v1 没有这个约束，平均价格只是把两个隐含 ROE 的冲突隐藏在同一个中枢里。
2. **固定线性系数没有依据。** `FairPE = 11 + 0.35 × growth + 0.45 × quality`、`FairPB = 1.50 + 0.12 × (ROE - 8)` 等系数和截断区间来自工程经验，没有理论推导、样本外拟合或参数敏感性记录，也没有按年度和行业验证稳定性。
3. **TTM EPS 单期周期误差。** `fourQ_mgsy` 是最近四季度快照，周期股在景气高点或低点会被显著放大；一次性损益、会计口径变化和股本变化也会污染单期 EPS。前一年 EPS 只有单点对照，无法代表完整周期的标准化盈利能力。
4. **行业标签不是真正可比公司。** v1 只区分金融/非金融，并对部分金融行业名称收紧倍数上限；它没有构造可比公司集合，也没有使用行业中位数、分位数或基本面匹配。行业名称相同不代表增长、杠杆、资产质量和会计口径可比。
5. **区间宽度没有覆盖率校准。** v1 用盈利增速绝对值决定宽度，没有预先声明的名义覆盖率，也没有用历史样本计算实际覆盖率、平均宽度或分位数损失；宽度不能解释为统计置信区间，更不能承诺价格落在区间内。

## 文献综述

下表只提炼对方法设计的约束，不复制论文摘要。文献结论来自各自市场和样本，不能直接当作 A 股收益承诺。

| 文献 | 可借鉴什么 / 对 v1 的约束 |
| --- | --- |
| Ohlson (1995), *Earnings, Book Values, and Dividends in Equity Valuation*, Contemporary Accounting Research, DOI `10.1111/j.1911-3846.1995.tb00461.x` | 把账面净资产、盈利和股利放入同一剩余收益估值框架，说明 PE/PB 不应是互不相干的倍数。v2 必须以账面价值与盈利的共同约束为中心，PE 只能由 PB 和 ROE 派生。 |
| Penman (1996), *The Articulation of Price-Earnings Ratios and Market-to-Book Ratios and the Evaluation of Growth*, Journal of Accounting Research, DOI `10.2307/2491501` | 论证 PE 与 PB 通过 ROE 和增长相互表达，单独比较任一倍数会忽略成长和质量差异。v2 强制 `PE = PB / ROE`，并把增长放进 `r - g` 分母。 |
| Nissim & Penman (2001), *Ratio Analysis and Equity Valuation: From Research to Practice*, Review of Accounting Studies, DOI `10.1023/A:1011338221623` | 提供报表重述、ROE 分解以及经营/融资活动分离的方法，强调可比性和杠杆差异。v1 只用行业名称分支、没有拆解 ROE；v2 需要平均净资产、负债率和质量字段，不能直接套统一系数。 |
| Liu, Nissim & Thomas (2002), *Equity Valuation Using Multiples*, Journal of Accounting Research, DOI `10.1111/1475-679X.00042` | 比较多种倍数与盈利口径，指出估值误差会随口径和可比公司调整而变化，前瞻盈利通常比单期历史数据更可靠。v1 的 TTM 单期和固定倍数只能作为示意，v2 需要行业分位与标准化盈利。 |
| Bhojraj & Lee (2002), *Who Is My Peer? A Valuation-Based Approach to the Selection of Comparable Firms*, Journal of Accounting Research, DOI `10.1111/1475-679X.00054` | 用基本面预测倍数来选择可比公司，而不是只按行业代码分组。v1 的行业标签不是可比公司集合；v2 的 peer 至少应按行业、规模和盈利/杠杆结构筛选。 |
| Piotroski (2000), *Value Investing: The Use of Historical Financial Statement Information to Separate Winners from Losers*, Journal of Accounting Research, DOI `10.2307/2672906` | 用历史财务信息构建九项质量信号，区分便宜但基本面改善或恶化的公司。v2 应加入 Piotroski 式质量诊断，不能用低 PE/PB 代替质量判断。 |
| Damodaran (2009), *Ups and Downs: Valuing Cyclical and Commodity Companies*, SSRN, DOI `10.2139/ssrn.1466041` | 讨论周期和商品公司估值，强调正常化盈利、穿越周期假设和情景区间，不能把峰值或谷值的 TTM 直接年化。v2 的标准化 EPS/ROE、`g` 限制和 `r` 情景正是针对这一约束；完整 DCF 仍不在首版。 |
| Liu, Stambaugh & Yuan (2019), *Size and value in China*, Journal of Financial Economics, DOI `10.1016/j.jfineco.2019.03.008` | 中国 A 股的价值构造需要处理壳价值和小市值污染，EP 等口径与 PB 不能机械互换。v2 应保留市值、壳标记和行业分层，不能把同一个 PB 系数套到所有股票。 |
| Qu & Zhang (2015), *Value-relevance of Earnings and Book Value Over the Institutional Transition in China: The Suitability of Fair Value Accounting in This Emerging Market*, The International Journal of Accounting, DOI `10.1016/j.intacc.2013.01.009` | 中国制度转型下 E/B 的信息含量和公允价值会计适用性会随阶段变化。v1 的固定线性系数没有中国样本外依据；v2 必须分年度、分行业检验并滚动校准。 |
| Christoffersen (1998), *Evaluating Interval Forecasts*, International Economic Review, DOI `10.2307/2527341` | 区间预测不能只看平均宽度，还要检验覆盖率和独立性/条件覆盖。v2 因此把覆盖率、平均宽度和 pinball loss 作为验收指标，而不是把 v1 的启发式宽度当作置信区间。 |

## v2：稳态 justified PB

### 目标公式

v2 的核心是稳态 justified PB。设 `ROE`、`g`、`r` 均用小数表示，`B` 为每股净资产，`E` 为每股盈利：

```text
PB_justified = (ROE - g) / (r - g)
PE_consistent = PB_justified / ROE
P_justified   = B × PB_justified
```

由第二式可得 `PB_justified = PE_consistent × ROE`，因此 PE 与 PB 不会再各自形成一套互相冲突的隐含 ROE。这里的 `PE_consistent` 是同一组 `B`、`ROE`、`g`、`r` 下的派生值，不是再乘一个独立的公允 PE 系数。

这不是完整 DCF，而是稳态交叉校验：它假设长期 ROE、增长和股权成本稳定，且账面价值、盈利与股利满足清洁盈余和稳定再投资关系；它不逐年建模收入、资本开支、营运资金、税率、摊薄、周期转折或终值估计。因此输出只能作为研究证据，不能替代完整现金流估值。

### 参数与可用性

- **股权成本 `r`：** 首版使用 `8.5%`、`9.5%`、`10.5%` 三个情景，不把单一 CAPM 点估计伪装成精确参数。后续数据契约要求无风险利率、ERP 和 beta，用于滚动估计和敏感性分析。
- **标准化增长 `g`：** 首版使用 TTM/前一年 EPS 推导的增长并限制在 `[-2%, 6%]`；后续改用跨周期盈利/ROE 趋势，而不是单年增速。
- **约束：** 必须满足 `r - g >= 1%`，且稳态增长必须低于 ROE。净资产非正、ROE 非正、`g >= ROE`、`r - g < 1%`，或公式产生的 PB/PE 非正时，结果暂缺，不使用盈利单锚或 v1 固定系数兜底。
- **倍数上界：** 为避免异常值主导结果，首版对 FairPB 设置上界（非金融 8、金融 3）；不设正下限，公式结果非正即不可估值。
- **情景区间：** 对 `r ∈ {8.5%, 9.5%, 10.5%}` 与 `g ∈ {base-1%, base, base+1%}` 生成情景；`g` 触及 `[-2%, 6%]` 边界时对重复值去重，避免边界情景被重复加权。以基础 `r=9.5%`、基础 `g` 的情景作为中枢，以有效情景价格的 20/80 分位作为上下界。该区间只反映参数情景离散度，不是统计置信区间；正式区间必须经过后续覆盖率校准。

### 首版：ttm_only 影子结果

v2 首版只复用当前快照中的 TTM EPS、前一年 EPS 和每股净资产，没有 5-10 年标准化序列。此时 `ROE` 只能由 TTM EPS 与每股净资产推导，`g` 只能由 TTM/前一年 EPS 推导，两者都不是严格的标准化值。

因此首版必须满足以下边界：

- 每条可计算 v2 记录带 `confidence=low` 和 `data_quality=ttm_only`；无法抓取或输入无效的记录使用 `data_quality=unavailable` 并保留 `reason`。即使数值看起来合理，也不能升级为正式估值。
- 不把 v2 结果写入公众号正文，不参与推荐排名、推荐门槛、目标价或交易指令。
- 保留数据日期、抓取时点、输入字段和不可估值原因，便于后续替换数据源时复现和审计。
- 不能把首版影子结果用于宣称已经完成样本外验证；Sina 快照不提供公告日和多年序列，严格 point-in-time 回测尚未成立。

### 后续替换

在数据契约满足后，用以下内容替换临时假设，并重新做 walk-forward：

- 5-10 年标准化 EPS 和 ROE，至少使用中位数或截尾均值，并处理周期、异常年份、一次性项目、会计口径变化和股本变化。
- 与标准化盈利匹配的平均净资产，而不是只看最近一期每股净资产。
- 派息率、经营现金流和负债率，用于质量诊断、可持续增长交叉检查和风险分层。
- Piotroski 式质量信号及其分项，不把质量折进一个无法解释的黑箱系数。
- 行业中位数/分位数和基于基本面的 peer 组，最少按行业、规模和杠杆分层，并记录分类标准版本。
- 滚动样本外校准的 `r`、`g`、标准化方法和区间分位数；参数只能在开发窗口选择，测试窗口不得回调。

## 数据契约与缺口

### 当前 Sina 数据的限制

现有 Sina `jsvar.js` 只提供当前快照，例如 `fourQ_mgsy`、`lastyear_mgsy`、`mgjzc` 等；它不提供报告期、公告日、5-10 年序列、现金流、分红、负债率、行业 peer 或风险参数。因此：

- 只能用抓取时点近似历史报告，无法重建当时可见的信息集；
- 不能用历史 `sync -as-of` 声称严格 point-in-time；
- 不能用现有数据完成 v2 所需的标准化盈利和滚动样本外检验。

### 建议的 Tushare 数据

建议接入 Tushare 的 `stock_basic`、`daily_basic`、`fina_indicator`、`income`/`balancesheet`/`cashflow`，并把公告日、报告期和修订标识作为一等字段；分红数据若使用 Tushare `dividend`，也必须记录公告日和口径。

| 新增字段或口径 | 用途 | 建议来源与处理 |
| --- | --- | --- |
| `report_end_date` | 标识财报所属报告期，防止把不同季度混在一起 | 财务报表接口的 `end_date` |
| `announcement_date` | 严格 point-in-time 门槛：信号日只能看到已公告数据 | 优先实际公告日 `f_ann_date`，缺失时回退 `ann_date` |
| 5-10 年 EPS 序列 | 标准化盈利、周期平滑和增长估计 | `fina_indicator`/`income`，按股本变化和复权口径统一 |
| 5-10 年 ROE 序列 | 标准化 ROE、稳定性与质量分层 | `fina_indicator`，保留原始值和计算口径 |
| 平均净资产 | 替代单期每股净资产，降低周期和融资时点影响 | `balancesheet` 的归母权益，按同一窗口平均 |
| 派息率 | 可持续增长交叉检查、质量和股东回报诊断 | 分红记录与归母净利润匹配；记录年度和公告日 |
| 经营现金流 | 盈利质量、现金流与利润背离诊断 | `cashflow` 的经营活动现金流 |
| 负债率 | 杠杆风险、金融/非金融分层和 peer 匹配 | `balancesheet`/`fina_indicator`，统一合并口径 |
| 行业中位数/分位数 | 可比公司相对估值，不把行业名称当 peer | `stock_basic` 行业 + 同报告期 peer；要求最小样本数 |
| 市值与壳标记 | 中国 A 股小市值/壳价值控制，支持分层校准 | `daily_basic` 市值；壳标记必须有版本化规则，不能凭未来退市信息回填 |
| 无风险利率、ERP、beta | 股权成本情景和敏感性分析 | 外部宏观/市场数据；固定期限、频率和 as-of 日期 |
| 财务口径与修订标识 | 处理合并/母公司、重述、追溯调整和不同会计期间 | 财报接口的报表类型、更新标识和版本记录 |

此外还需要总股本/流通股本和交易价格，以保证每股指标与市值口径一致；财务数据必须带来源、抓取时间、报告期、公告日和修订版本。行业分类标准、壳标记规则和 peer 构造方法都应版本化，避免历史回测悄悄使用今天才知道的信息。

### 影子输出契约

当前 v2 shadow 记录包含：`method=residual_income_pb_roe_v2`、`schema_version`、`as_of`、`fetched_at`、`statement_basis`、`data_quality=ttm_only|unavailable`、`confidence`、`warnings`、`roe_pct`、`eps_growth_pct`、`cost_of_equity_pct`、各情景 `pb`/`pe`/`price`、区间上下界与中枢、来源和不可估值 `reason`。接入严格 point-in-time 数据时还必须增加 `report_end_date`、`announcement_date`、`input_mode=ttm_only|normalized` 和 `source_version`。

缺失数据的处理必须是显式降级，不得静默复用 v1：

- 公告日或报告期缺失：不得作为正式 point-in-time 样本；首版只能低置信度影子记录。
- 行业 peer 不足：保留绝对估值和缺口标记，不虚构行业分位。
- 股权成本或增长无法满足约束：输出 `unavailable`，不填零、不回退固定倍数。
- 净资产/ROE 非正、`r - g < 1%` 或 PB/PE 非正：明确写出不可估值原因。
- 财务数据过期或来自快照抓取：标记 `stale`/`ttm_only`，不进入公众号。

## 验收与发布边界

### 当前状态

- v1 继续展示，但统一标注为“财务研究区间”；它不是论文推导、目标价或最终合理价值。
- v2 只做 shadow 存储和研究评估，不显示在公众号正文，不改变推荐排名、风险门槛或交易结论。
- 现有估值只覆盖报告候选的一部分，研究样本必须单独定义，不能把“文章中出现过的股票”当作无偏的全市场样本。

### 必须验证的断言

在冻结的 walk-forward 测试集上，至少验证以下项目；具体目标期和名义覆盖率必须在看结果前写入实验协议：

1. **区间质量：** 实际覆盖率与名义目标覆盖率、平均区间宽度和上下分位数的 pinball loss；只提高覆盖率但显著扩大区间不算改进。
2. **稳定性：** 分年度、分行业（以及必要的规模/杠杆分层）的覆盖率、偏差和样本量；小样本分组必须标为证据不足。
3. **参数敏感性：** `r` 的 8.5%/9.5%/10.5% 情景、`g` 的边界、ROE 标准化方法、异常值处理和 peer 规则变化后，方向和区间是否稳定。
4. **缺失数据降级：** 公告日缺失、历史不足、行业中位数缺失、财务数据过期时，是否明确输出低置信度或 `unavailable`，且不回退到 v1 或伪造数值。
5. **边界条件：** `r <= g`（或不满足 `r - g >= 1%`）、负盈利、负净资产、零/负 ROE、非正 PB/PE 时，必须不可估值并保留原因。
6. **walk-forward：** 每个 as-of 只用当时已公告的数据训练/校准，按时间滚动测试；禁止随机切分、全样本调参、重复消费留出集或使用修订后的未来数据。

### 发布门槛

v2 在通过上述 walk-forward、完成 point-in-time 数据审计并经过人工复核前，不得称为“最终合理价值”，不得进入公众号正文，也不得替代 v1 的“财务研究区间”。即使通过，也只能先作为有版本、有校准报告、有可追溯输入的正式研究字段发布；任何数据缺失、参数越界或无法满足稳态约束的情况都必须保持“暂缺”，不能用旧缓存或固定倍数补齐。

方法变更、数据源变更和校准结果应同步记录在本文及实验档案中。历史结果只描述当时可见数据和当时版本，不代表未来收益或价格会落入区间。
