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

输入 JSON 拒绝未知字段、重复键、尾随文档、非有限数值和超过 64 层嵌套。单文件及 gzip 解压后上限均为 256 MiB。省略执行状态不会被当成可成交。

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

`tools/aris/test_compile_candidates.py` 覆盖候选子集编译和非法候选名。候选生成仍属于研究层，ARIS 只负责登记后的冻结执行、压力回放、门禁与审计；当前 G11/G14 仍为 `inconclusive`，任何运行都不能自动晋级为 accepted。
