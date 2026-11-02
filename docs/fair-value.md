# 个股合理价值区间

合理价值区间是报告内的一项财务研究字段，用来回答“按最近四季度盈利和净资产，当前价格处于什么位置”。它不是目标价、收益预测、成功概率或买卖指令，也不参与推荐排名和推荐门槛。

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
| `totalcapital` | 总股本（万股） | 与净利润交叉校验 EPS |
| `stock_state` | 上市状态 | 只接受正常上市（`1`） |
| `papercode` / `a_code` | 股票代码 | 防止页面与请求股票错配 |

解析时会拒绝缺失字段、重复字段、非有限数值、代码不匹配、非正常上市状态，以及由净利润和总股本反推的 EPS 与 `fourQ_mgsy` 明显不一致的快照。该数据是财报口径，不是实时行情；同日多次同步可能得到相同快照。

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
非金融：FairPE = clamp(8, 30, 11 + 0.35 × growth_clamped + 0.45 × quality)
        FairPB = clamp(0.50, 8, ROE / 5)

金融：  FairPE = clamp(5, 10, 5.5 + 0.15 × growth_clamped + 0.20 × quality)
        FairPB = clamp(0.35, 1.20, ROE / 15)
```

银行、保险、证券、信托和多元金融按行业名称收紧倍数上限。盈利估值与净资产估值各占一半：

```text
pe_price = E × FairPE
pb_price = B × FairPB（B 或 ROE 不可用时退化为 pe_price）
central  = (pe_price + pb_price) / 2
width    = 0.15 + min(|growth|, 25) / 100 × 0.20
lower    = central × (1 - width)
upper    = central × (1 + width)
upside   = (central / P - 1) × 100
```

盈利增速绝对值越大，区间越宽。盈利增速缺失、每股净资产不可用、ROE 为负或异常偏高时，`confidence` 标记为 `low`。TTM EPS 为负、价格无效或快照校验失败时不生成区间，结果只保留明确的 `reason`，并在文章中显示“暂缺”。

## 生成与保存

- `sync` 在报告计算完成后，为精排候选、正式推荐和观察排名中的前 10 只股票拉取估值，单轮总等待上限 30 秒。
- 单只股票请求失败或数据不满足校验时，记录 `unavailable` 原因；不会中断行情同步、报告保存或通知投递。
- 估值结果保存在 `selection.Report.fair_values`，随报告一起写入 `market.db`。字段是可选扩展，不改变现有报告 schema 版本。
- 公众号文章只读取已保存报告中的估值快照，不在排版或投递时重新请求外部数据。
- `preview` 和 `replay` 不访问估值源；`replay` 会复用已冻结报告中的估值快照。
- 日常“每日精选”和盘前观察文章都会展示区间；当候选较多进入紧凑排版时，只保留一行区间摘要，避免超过公众号正文长度限制。

文章字段包括区间上下限、中枢、相对收盘价幅度，以及 TTM EPS、每股净资产、ROE、盈利同比、当前 PE/PB 和估值方法说明。

## 已知限制

- 模型是固定锚点的 PE/PB 相对估值，不是 DCF，也没有使用同业可比公司中位数。
- 财报数据存在披露滞后，无法反映最新订单、价格、政策、商誉减值或一次性损益。
- 周期股的峰值盈利会放大估值；金融股的净资产质量和坏账周期也无法由该公式识别。
- 股本变化、会计口径调整和不同报告期合并范围会使 EPS、ROE 和 PB 失真。
- 区间宽度是增速不确定性的简化表达，不是统计置信区间，不承诺价格会落在区间内。
- 任何区间都不构成投资建议；推荐资格仍由既有历史证据、风险门槛和结构门控决定。
