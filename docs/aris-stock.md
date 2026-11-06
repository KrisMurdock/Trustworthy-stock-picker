# 自动化策略研究首版

`aris-stock` 是独立的 Go 离线研究二进制。它运行已冻结的 MA 信号和预注册候选，保存组合账本、压力情景、确定性重放、门禁及评审记录。不会打开生产行情库、下载行情、发送通知或改动推荐。

本期按已确认范围迁移 `adjusted_return_proxy / qfq_proxy`：前复权价格、虚拟小数持仓、假设的成交可用性与费用。严格历史现金账户、整手、公司行为税费和真实历史 ST/停牌证据另行实现。不要把 `shares` 理解为可下单股数。

## 构建和最小流程

```bash
make aris-build
bin/aris-stock version
mkdir -p .scratch/aris-stock
bin/aris-stock seal --request examples/aris-registration.json > .scratch/aris-stock/bundle.json
bin/aris-stock run --request .scratch/aris-stock/bundle.json --json
bin/aris-stock status --run example-proxy --json
bin/aris-stock verify --run example-proxy --json
```

示例只有协议夹具，没有交易信号；风险预算故意留空。`run` 返回 `needs_human`、退出码 **3**，不运行回测。这是预期的缺配置分支。不要在脚本中把所有非零退出码都解释为进程崩溃。

`seal` 是显式登记动作：把完整人类契约文本、每个输入和当前二进制 SHA-256 写入 bundle，结果输出到 stdout。它不代替人工批准，不自动填风险预算，也不运行模拟。不要直接覆盖正在读取的源文件。更改任何规则或输入后必须重新登记，使用新的 `run_id`；既有运行永不覆盖。

`version` 同时输出编译时版本、Go 构建信息和实际执行文件的 `build_sha256`。换二进制、编译参数或容器构建后必须重新 seal；旧运行只能使用原二进制恢复和重放。Git SHA 不是二进制内容哈希。

## 冻结模型输入

已有参考快照可用原生准备命令转换，不运行 Python：

```bash
bin/aris-stock prepare \
  --snapshot /local/frozen/prices.json.gz \
  --sessions /local/frozen/sessions.json \
  --signals /local/frozen/signals.json \
  --model-config /local/frozen/model-config.json \
  --limit-mode board > .scratch/aris-stock/input.json
bin/aris-stock simulate --input .scratch/aris-stock/input.json > .scratch/aris-stock/diagnostic.json
bin/aris-stock hash --input .scratch/aris-stock/input.json
```

- 快照 schema 1：`calendar` 为排序日期数组，`stocks` 为代码到八列记录数组的映射。记录为 `[adjust,date,open,high,low,close,volume,source]`，价格为百万分之一元整数，成交量为股；支持 `qfq` 和 `none`。
- 信号为 `{code,date,atr}` 数组，日期必须属于评估交易日。首版消费已登记信号，不重新发现或筛选信号；这保留了历史基线的事件集合。信号规则、公式和事件生成版本应保存在 `human_contract` 中。
- 模型配置字段见示例。主止盈 `.05`、对照 `.03`，止损 `.05`；容量用前 20 个全市场可见日的原价成交额估计。`limit-mode=five_percent` 可重放历史统一 5% 限价敏感性情景。
- `prepare` 输出 schema 3；每个信号代码在每个评估日都必须有显式观察状态。缺价只使用已知前价估值，不可成交；首个可见价格以前的占位值不得产生交易。
- 先用所有候选信号代码的并集准备共同 `bars`，再给各候选填入自己的信号数组。基线和候选必须共享价格、交易日、来源说明、费用、滑点和组合约束；只允许信号和止盈目标不同。不同滑点基准应登记为另一运行，额外压力滑点由 `stress_slippage` 固定。
- `simulate` 是诊断命令，不执行研究验收，也不产生 `accepted`。默认 10 分钟，可用 `--timeout` 设置，最多 24 小时。

输入 JSON 拒绝未知字段、重复键、尾随文档、非有限数值和超过 64 层嵌套。v1 单文件及 gzip 解压后上限均为 256 MiB；v2 共享快照边界见下节。省略执行状态不会被当成可成交。

## 契约、预算和门禁

bundle 包含 `request`、`human_contract`、`contract` 和 `inputs`。`request.strategy` 当前只接受 `ma-proxy`；它不是支持所有策略的通用协议。运行使用 bundle 内快照，不再读取原行情文件。

风险与统计参数必须事先明确：

| 字段 | 单位与行为 |
| --- | --- |
| `max_drawdown` | 正的小数比例；与组合最大回撤绝对值比较 |
| `absolute_tail_loss` | 元；闭合交易最坏净亏损金额上限，不能用单笔百分比替代 |
| `min_closed_trades` / `min_signal_days` | 闭合交易数、对应的不同信号日期数 |
| `min_net_expectancy` | 元/闭合交易，净期望必须严格大于阈值 |
| `min_profit_factor` | 至少为 1；实际 PF 必须大于 1 且达到阈值。无亏损导致 PF 未定义时不按无穷大通过 |
| `stress_slippage` | 单边小数比例，最多 16 个，每个必须高于该候选原滑点；样本、期望、PF、回撤和绝对尾亏都要通过 |
| `inference.block_sessions` | 非重叠时间块长度，2～1000 个交易日 |
| `inference.min_independent_blocks` | 总块数与有闭合交易的块数下限，4～1000 |
| `inference.bootstrap_samples` | 重采样次数，100～10000 |
| `inference.confidence` / `seed` | 区间置信水平 `[.8,1)` 和固定随机种子 |

时间块按交易日切分，同日所有股票保持在同一个块中。闭合交易净损益归入信号日所在块；同时按相同日期成对比较候选和基线每日组合收益。重采样保留块内关联与真实分母，检查净期望和平均日超额收益区间下界均为正。尾部不足一块保留实际长度；空交易样本不能删除后继续计算。该统计仍属开发期描述，不能校正未登记的早期搜索。

基线自身可以亏损；候选相对基线的区间比较才决定 G15。每个候选均被报告，不自动挑出赢家并隐藏失败者；任何候选的确定性门禁失败会使本次登记运行 `rejected`。

首版实际执行 G1–G5、G7–G10、G12–G13、G15–G16。G3 证明冻结字节一致，历史数据可见性单独由 G14 表达；G16 保留拒单、期末持仓和估值缺失统计，不凭空平仓。G6 的同构建测试证明、G11 的独立留出消费及 purge/embargo、G14 的历史可见时间和修订谱系目前明确为 `inconclusive`。因此**首版不会签发 `accepted` 或 `development_passed`**：数值门禁通过后仍为 `needs_human`，失败为 `rejected`。严格模式及完整验收签发另行实现，不能凭评审 `pass` 绕过。

预算上限：100 个候选，每候选 1～10 次尝试（包括失败/中断）、0～10 次评审、总时限不超过 24 小时。基线也受单候选尝试上限约束。压力情景及确定性重放包含在同一次尝试中，不能改变注册参数。

## 恢复、取消与审计

```bash
bin/aris-stock resume --run RUN_ID --state-dir .scratch/aris-stock --json
bin/aris-stock cancel --run RUN_ID --state-dir .scratch/aris-stock --json
bin/aris-stock verify --run RUN_ID --state-dir .scratch/aris-stock --timeout 10m --json
```

状态目录必须是本地文件系统，拒绝 CIFS/SMB/NFS、根目录和路径链中的符号链接。目录应由运行用户独占；本地同 UID 恶意并发替换文件不属于隔离边界。目录 0700、文件 0600；每个 run 使用非阻塞 `flock` 单写锁。

`state.json` 是唯一裁决源；`bundle.json` 保存冻结输入；`artifacts/<candidate>.json` 保存完整交易/净值、重放哈希和各压力情景。`ledger.jsonl` 记录尝试变更，`events.jsonl` 记录阶段和 state 哈希；二者分别限 32 MiB。审计索引可能保留崩溃前未提交的尾部，不据此推进状态。

尝试在执行前持久化为 `started`；结果落盘后才变为 `completed`。恢复将未完成尝试记为 `interrupted`，重试使用新编号并继续扣原预算；已完成结果先验哈希再复用。恢复不会重置原始截止时间，不会重新收费已缓存评审。进程在初始冻结完成前崩溃可能留下保留 ID 的不完整目录，此时明确拒绝恢复，使用新 ID，不自动清理证据。

终态无法恢复或覆盖。取消标记按 100ms 检查，模拟按交易日/信号检查 context；SIGTERM 也会取消。报告先持久化，再提交终态，查询到终态即可获取报告。

`verify` 对终态运行重新计算已完成结果、压力情景和门禁，再核对发布报告。它不修改尝试预算、不请求评审。完整 JSON 哈希按本程序规范化后的结构计算，不能用文件字节的 `sha256sum` 替代输入结构哈希；产物账本中的文件哈希则包含文件末尾换行。

| 退出码 | 含义 |
| --- | --- |
| 0 | 管理/诊断命令成功；协议预留 accepted |
| 1 | 输入、运行、存储或验证错误 |
| 2 | rejected |
| 3 | needs_human |
| 4 | canceled |
| 5 | 协议预留 development_passed |

## 只读评审服务

`review.mode=off` 不请求外部服务；`advisory` 只允许固定 HTTPS 结构化评审服务。由运维设置 `ARIS_REVIEW_ENDPOINT`、`ARIS_REVIEW_TOKEN`、`ARIS_REVIEW_MODEL`、`ARIS_REVIEW_FAMILY`，契约登记相同 model/family 和 `author_family`。模型名/家族使用小写字母、数字和连字符，最长 64 字符。该服务是本项目 schema 1 协议，**不直接兼容任意 OpenAI 聊天接口**。

请求 POST JSON，包含版本、内嵌 prompt、bundle/build 哈希、模型和确定性门禁。服务返回：

```json
{"schema_version":1,"verdict":"pass","findings":[],"model":"reviewer-model","model_family":"reviewer-family","independent":false,"duration_ns":0,"response_sha256":""}
```

每条 finding 必须含 `severity`（P0～P3）、`title`、`evidence`、`contract_clause`、`suggested_test`；`veto` 必须有 P0/P1，`pass` 不可包含未解决 finding。传输使用 30 秒超时、1 MiB 输入/输出上限、禁止重定向，无自动网络重试。Token 只进入 Authorization，不落状态、提示词或报告。

请求不发送自由文本契约、原始行情、代码 diff、路径或环境变量，也不提供工具。模型身份与运维映射核对，不能仅相信模型自报；服务本身仍是信任边界。独立性由实际 family 与 author_family 比较得出；要求跨家族但不满足时需要人工处理。缓存以完整输入、prompt 和模型策略哈希命名，保存于本次运行目录；不跨 run 复用。

`veto` 只会收紧结论；`concern`、错误、超时或身份不符进入 `needs_human`。确定性门禁失败时直接拒绝，不花费评审调用。`exec` 请求明确拒绝，因为本期没有可证明的进程沙箱；不会偷偷降级为非隔离的 `codex exec`。

## HTTP 进程接口

```bash
# catalog.json 内容：{"approved":"bundle.json"}，相对目录为 catalog 所在目录。
bin/aris-stock serve --catalog .scratch/aris-stock/catalog.json --addr 127.0.0.1:9090
curl -H 'Content-Type: application/json' -d '{"schema_version":1,"bundle":"approved"}' http://127.0.0.1:9090/v1/runs
```

目录清单由服务启动者加载，最多 100 项、合计 256 MiB；HTTP 不接受路径、契约覆盖或命令。创建任务先持久化冻结状态再返回 202。相同 bundle 重复请求返回同一运行，不同内容的同一 ID 返回 409。每进程一个活动任务、无等待队列，忙时 429；其他 CLI 进程可运行不同 ID。

接口：`GET /healthz`，`POST /v1/runs`，`GET /v1/runs/{id}`，`GET /v1/runs/{id}/report`，`POST /v1/runs/{id}/resume`、`/cancel`。后两者 JSON body 为 `{}`。所有响应 `no-store`，写请求必须为 JSON、最多 1 MiB，不接受 Origin；Host 及绑定地址限回环。无页面、无内置账号/角色。

这不是面向公网的 API。远程访问需受控代理认证，代理重写 Host 为回环地址；代理身份和权限需要部署环境单独验证。本轮没有更改生产网页，不需要增加普通用户/管理员的虚构应用身份。

## 独立容器与验证

```bash
make aris-test
ARIS_REFERENCE_ROOT=/local/frozen/proxy-baseline make aris-reconcile
make fmt-check vet aris-build build
make aris-image
make aris-smoke aris-docker-smoke
docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges aris-stock:COMMIT version
```

镜像默认 UID/GID 10001，仅包含原生 Go 二进制与系统 CA。实际运行需将本地状态和日志分别挂载到 `/state`、`/logs`（该 UID 可写），输入目录只读挂载；使用 `--state-dir /state`。容器内的二进制哈希可能不同，必须在同一镜像执行 seal。HTTP 仍绑定容器回环，只供同网络命名空间的受控代理访问，不以 `0.0.0.0` 绕过约束。

历史对账以提交 `9d21c3f196003ee4d0249b69ebdfdc5144fdac25` 对应的冻结基线为参考，41 个情景覆盖 3%/5% 止盈、10/20bp 滑点及 5% 限价敏感性。订单/日期/原因/顺序/分母精确一致，浮点容差为 `1e-6 + abs(expected)*1e-10`。参考数据不提交仓库；未设置 `ARIS_REFERENCE_ROOT` 时该专用测试跳过，普通测试通过不能代替基线对账。

当前版本提供 MA 首版执行器及研究流程基础。完整独立留出签发、严格现金账户、受隔离的 exec reviewer、外部策略子进程协议、第二种策略适配器和跨运行研究记忆仍属后续能力，不应把此版本标为整个长期路线图全部完成。尚未部署到生产环境。

## 从冻结特征编译候选

现有离线研究脚本可以先生成共同的 `maexec.Input` 和一份逐信号特征表，再用候选编译器登记固定条件：

```bash
python3 tools/aris/compile_candidates.py \
  --base-input .scratch/aris/base-input.json \
  --features .scratch/aris/features.json \
  --constraints .scratch/aris/constraints.json \
  --inference .scratch/aris/inference.json \
  --budget .scratch/aris/budget.json \
  --human-contract .scratch/aris/contract.md \
  --run-id ma-entry-20261105 \
  --candidate ma20-slope=ma20_slope_gt_02 \
  --candidate close-position=close_position_65 \
  --output .scratch/aris/request.json
bin/aris-stock seal --request .scratch/aris/request.json > .scratch/aris/bundle.json
bin/aris-stock run --request .scratch/aris/bundle.json --state-dir .scratch/aris/state --json
```

编译器只做三件事：校验每个基线信号有对应的特征行、按布尔特征筛选候选信号、复制同一份价格和组合配置。它拒绝重复信号、未知特征、空候选和保留名称 `baseline`，不会搜索阈值、组合条件、调整退出或自动挑选历史赢家。`baseline` 与候选共享 bars、sessions、费用和仓位约束；候选差异只来自信号集合。

历史扩展缓存可先用 `tools/aris/build_snapshot.py` 生成 ARIS 快照。`--max-codes` 用于小规模协议验证；快照包含前复权日线和独立容量缓存，后者的 OHLC 是容量估值近似，不能当作真实不复权成交序列。由于 bundle v1 会为每个候选复制完整输入，候选股票较多时应拆成多个登记 run，或在协议后续版本引入共享快照引用；超过 256 MiB 的 bundle 会被拒绝。

`tools/aris/test_compile_candidates.py` 覆盖候选子集编译和非法候选名。候选生成仍属于研究层，ARIS 只负责登记后的冻结执行、压力回放、门禁与审计；当前 G11/G14 仍为 `inconclusive`，任何运行都不能自动晋级为 accepted。

## 多年全池共享输入（request schema 2）

v2 用 `shared` 替代 `inputs`，同一个 gzip 冻结快照只保存一次。候选在内存共享不可变行情，各自持有信号数组；资金、容量、并发仓位仍在完整股池上统一回放，不能按股票分批后拼接收益。v1 的文件、哈希和执行口径继续兼容。

`shared` schema 1 包含 `snapshot_gzip`（base64）、压缩字节的 `snapshot_sha256`、评估 `sessions`、统一 `config`、按候选 ID 索引的 `signals` 和 `limit_mode=historical_board`。信号并集决定共同价格覆盖。外层文档最多 256 MiB，压缩快照最多 64 MiB，解压最多 1 GiB；严格校验重复键、未知字段、快照哈希和混用协议。归档自包含，不读取可变外部路径。

历史模式沿用原多年代理基线：创业板在 2020-08-24 前后分别使用 10%/20% 假设涨跌幅；过户费在 2022-04-29 从 0.00002 切换为 0.00001，印花税在 2023-08-28 从 0.001 切换为 0.0005。容量均值使用冻结交易日历前 20 日的原价成交额（保留预热日期），缺失按原基线零额处理。旧 `board`/`five_percent` 模式不变。这是已有模型的口径对齐，不能替代历史 ST、停牌和公司行为证据。

候选编译器增加共享输入模式：

```bash
python3 tools/aris/compile_candidates.py \
  --shared-snapshot /local/frozen/full.json.gz \
  --signals /local/frozen/signals.json --sessions /local/frozen/sessions.json \
  --model-config /local/frozen/model-config.json \
  --features /local/frozen/features.json --candidate demand=cross_demand \
  --constraints /local/frozen/constraints.json --inference /local/frozen/inference.json \
  --budget /local/frozen/budget.json --human-contract /local/frozen/contract.md \
  --run-id full-demand-v1 --output /local/frozen/request.json
```

特征文件必须覆盖每个基线信号；被选择的字段必须逐行存在，且为布尔或 null（null 不入选）。编译器将特征文件 SHA-256 和选择表达式附在可见的人类契约末尾，随后一起 seal，拒绝覆盖已有输出。Python 只在离线制作输入时使用，Go 执行、恢复和重放不启动 Python。

`build_snapshot.py` 的原价缓存只有收盘和成交量，用于容量估计；其填充的 OHLC 不得用于成交模拟。信号输出剔除研究元数据 `cross_date`，只保留执行协议的 code/date/atr。

## 原生自主迭代

`discover` 将假设生成、特征计算、训练失败归因、下一轮候选、成本压力、停止和最终验证接成一个原生 Go 流程。无需人工逐轮填写候选，也不调用模型服务、Python、子进程或任意代码。其搜索范围是可审计的表达式语法，不是无限制发明任何算法。

```bash
# 新二进制必须重新 seal；输入仍为完整 v2 共同行情 bundle。
bin/aris-stock seal --request /local/frozen/request.json > /local/frozen/sealed-new.json
bin/aris-stock discover --request /local/frozen/sealed-new.json \
  --search-plan examples/aris-search-plan.json --state-dir .scratch/aris-auto \
  --timeout 30m --json
bin/aris-stock discover-status --run autonomous-ma-example --state-dir .scratch/aris-auto --json
bin/aris-stock discover-cancel --run autonomous-ma-example --state-dir .scratch/aris-auto --json
bin/aris-stock discover-resume --run autonomous-ma-example --state-dir .scratch/aris-auto --timeout 30m --json
bin/aris-stock discover-verify --run autonomous-ma-example --state-dir .scratch/aris-auto --timeout 30m --json
```

`examples/aris-search-plan.json` 给出日期和试验预算示例；日期必须存在于输入日历，请按自己的冻结数据调整。原 bundle 的其他人工候选不参与自主搜索，只有 `contract.baseline` 的事件作为入场母集；风险、样本、费用压力与时间块参数沿用明确登记的 contract。旧 run/verify/HTTP 接口不变；自主搜索目前只通过 CLI 调用。

### 特征与下一轮生成

- 特征只使用信号日及之前的前复权观察日线：5/20/60 根收益、收益标准差、当日量比（以前 w 根、不含当日的均量为分母）、均线偏离与均线单日斜率；另有当日振幅/收盘、上影占振幅、收盘在振幅中的位置。均线斜率为当日 w 根均价相对前一根 w 根均价的变化，不等同于旧人工候选的其他斜率定义。
- 缺少历史、零振幅、无有效分母时相应特征缺失，不能用零填补；未知值不满足过滤条件。原始非复权容量缓存不充当量价特征。
- 每个特征的阈值取训练信号特征的25%/50%/75%分位，每个阈值生成 `<=`、`>=` 两种方向，冻结完整谓词池及哈希。改变验证段行情不会改变这些阈值。
- 每轮计算各表达式在当前开发冠军已闭合交易中剔除的亏损与盈利金额，优先评估“剔除亏损金额减去剔除盈利金额”最大的表达式。这个估计只负责提出假设，实际结果必须重新执行全池组合回测，不能把原交易子集收益当成新组合收益。
- 可以尝试单条件，或在当前开发冠军上增加一个不同特征条件，最多两个条件；已经尝试过的训练信号子集不重复回测。当前轮新结果会更新开发冠军和下一轮失败归因，所有尝试都保留。
- 开发冠军按基础和压力情景中最差的组合净收益比较，必须同时满足样本、信号日期、回撤、尾损及无未平仓约束。即使暂时降低亏损，也只表示搜索改进；最终还要求开发与验证净期望/PF达标，并检查验证时间块证据。

### 数据隔离与停止

`train_through`、`validation_from`、`validation_through` 固定两个执行窗口，各自从相同初始资金启动。两个窗口间至少空出 `holding_sessions+1` 个交易日，各窗口末尾也排除无法覆盖完整持仓窗口的新信号。实际无法卖出的仓位仍保留并阻止资格通过，不强制平仓。

验证段只在搜索结束、开发冠军固定后执行一次最终对照，不参与阈值、下一轮、开发冠军或停止决策。验证阶段进程中断可以重跑同一冻结对照，不重新搜索。所有选择和试验次数均记录；训练段经过自适应多重搜索，训练收益和筛选统计有选择偏差。历史数据此前已被观察、或在另一个 campaign 再次使用时，不能把隔离验证称为全新独立样本外。

`max_trials` 为完整候选回测预算（1～100），`patience` 为连续无改进上限，`max_terms` 为1或2。表达式池穷尽、达到预算或连续无改进均会自动结束。池中廉价的失败归因筛选同样属于搜索，不等于只探索了最终回测的若干表达式；不能据有限回测次数宣称已消除多重检验。

### 检查点与结果

状态保存在本地 `<state-dir>/campaigns/<id>/`，拒绝 CIFS/NFS、符号链接和目录穿越。`campaign.json` 保存版本化计划、完整共同输入、信号时点特征及谓词池；`state.json` 保存期限、停止原因、开发冠军、每次公式/假设依据、压力收益、失败分类和产物哈希；`baseline.json`、`trial-NNN.json`、`validation.json` 保存完整模拟账本。

每个尝试在执行前登记。崩溃或取消后尚未提交完成的尝试标记 `interrupted`，仍消耗预算；恢复跳过该信号子集。运行 ID 不可复用，恢复不重置次数、阈值、期限或停止规则。单写锁阻止并行操作同一 campaign。`discover-verify` 验证冻结输入及产物，并重放全部自适应候选顺序、失败反馈、开发冠军与最终验证；中断预留也原样计入，不补做隐藏试验。

`completed_rejected` 表示搜索运行正常结束但没有通过研究门槛；`completed_research_only` 表示诊断门槛通过，仍缺独立证据，不能晋升正式推荐。二者 CLI 均返回0，必须看 `status`/`validation_failures`，不能把退出码0当策略获批。暂停/超时和错误由状态及非零退出码体现。`timeout` 最大24小时，是不可在恢复时重置的 campaign 期限；CLI `--timeout` 另限制本次调用时间。

验证入口：`make aris-test`、`make aris-discovery-smoke`、`make aris-discovery-docker-smoke`。容器冒烟使用 UID10001、只读文件系统、禁网，运行原生 CLI 的完整生成/迭代/查询/重放，并验证重复 ID、非法区间、缺配置和产物篡改拒绝；Python 仅在宿主机生成合成输入。
