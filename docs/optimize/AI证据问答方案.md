# AI 证据问答方案

核对日期：2026-10-29。源码基线：`33ab655`。本文记录 `/review` 页面中的问答、复盘归因与模型配置契约。模型解释读取到的本地证据；本文不把引用格式校验、提示词限制或历史统计描述成事实正确性的保证。

## S｜情境：回答本地报告问题，同时保留证据出处

用户可以询问报告、某只股票的复盘、实验档案或方法论，也可以手动触发固定结构的复盘分析。当前实现先识别意图，执行预先写好的查询，再把裁剪后的事实发给兼容 Chat Completions 的模型。它没有开放工具执行器，不让模型生成 SQL、浏览网页或写入行情。

生产边界分为 [advisor](../../internal/advisor/advisor.go)、[llm 客户端](../../internal/llm/client.go)、[HTTP 适配器](../../internal/dashboard/advisor.go) 和 [网页交互](../../internal/dashboard/static/review.js)。问答不建立服务端会话存储；每次请求显式携带历史消息。模型配置文件是该功能中独立的可写入口，不属于行情写入。

## T｜任务：限制可查询内容，解释失败与引用

| 接口 | 方法与用途 |
| --- | --- |
| `/api/master/roles` | GET/HEAD，返回八种分析视角、默认角色及 schema 1 |
| `/api/master/chat` | POST，普通问答；`mode` 允许 `chat/review`，默认 `chat` |
| `/api/review/analysis` | POST，强制 `review` 模式；必须有 `as_of`，无问题时生成默认复盘问题 |
| `/api/llm/config` | GET/HEAD 读取脱敏状态；PUT 更新允许编辑的字段 |
| `/api/llm/config/test` | POST，用已保存配置发送最小连通测试 |

主请求接受 `question`、`role`、`mode`、`as_of`、`code`、`revision`、`history`。同时兼容 `role_id` 和 `messages`：没有 `question` 时，从最后一个用户消息提取问题。HTTP 层禁止同时发送非空 `history` 与 `messages`，拒绝未知字段、多份 JSON 和查询参数。

请求体上限 128 KiB；问题最多 12,000 个 Unicode 字符，历史最多 12 条、每条最多 4,000 字符。进入 advisor 后只保留最近三条非空 `user/assistant` 消息，按时间顺序发送，总计最多 10,000 字符。系统消息和隐藏推理不作为历史传入。

日期、代码与 revision 有独立格式校验；revision 是 64 位小写十六进制。非法请求在流开始前返回 400。`Role` 默认为 `buffett`，未知 ID 拒绝。八个 ID 为 `buffett/munger/graham/livermore/soros/simons/lynch/duan-yongping`，提示版本 `role-v1`；它们只是分析视角，不表示真实人物认可生成内容。

## A｜行动：固定查询计划，再生成可核对的回答

### 1. 规则分类与模型分类

[intent.go](../../internal/advisor/intent.go) 先按关键词、股票代码、日期和实验 ID 分类，规则置信阈值为 0.72。明确越界的问题走确定性 `unsupported` 回答；低置信且没有模型时追问，不查库。

模型仅在规则无法确定时参与分类。分类请求温度 0、输出上限 512 tokens；返回文本最多 8,192 字节，必须是单份 JSON，拒绝未知字段。正常回答阶段另用温度 0.2、上限 2,048 tokens。这些数值是当前代码配置，不是供应商能力描述。

模型分类失败或 JSON 不合格通常转成 `fallback` 澄清；如果请求 context 已取消，则结束请求。允许的意图只有以下七种：

| 意图 | 固定来源与条件 |
| --- | --- |
| `review_explain` | 有代码时 `review.Event`，否则 `review.Day`，日期必填 |
| `report_query` | `review.Day`，日期必填 |
| `stock_evidence` | `review.Event`，日期与单个代码必填 |
| `experiment_query` | 有实验 ID 用 `experiments.Get`，否则 `experiments.List` |
| `methodology` | 不查库，只回答概念 |
| `clarify` | 不查库，追问一个关键条件 |
| `unsupported` | 不查库，返回能力边界 |

一次最多三个互不依赖的意图。规则识别到先后依赖、多个股票/日期/实验 ID、问题与显式参数冲突，或同类意图的不同查询条件，会先澄清。方法论意图清空行情查询条件。缺少实验源会将相关意图转成不支持并附 warning。

`plan` 是确定性的步骤说明，不是可执行 SQL。模型不能选择任意数据源。字段格式和枚举校验能限制查询形状，但并不证明模型分类时提取的所有业务槽位都正确；分类仍可能误判。角色切换改变提示词，不扩张访问权限。

### 2. 证据选择与裁剪

[evidence.go](../../internal/advisor/evidence.go) 构造 `Evidence`：`evidence_id`、`intent`、`kind`、`source`、日期/代码/名称和 `facts`。它挑选字段，不把完整报告、日线、实验快照或逐笔交易明细发送给模型。

报告查询保留元数据、三组聚合及代表事件；整日报告归因重点保留异常事件。事件优先级为：缺数据/不支持 100，歧义 96，亏损 95，未入场 94，止损 93，其他未成熟类别按具体分支处理；`pending` 为 76，普通成熟结果为 45。归因模式的整日事件过滤阈值为 80，因此 pending 通常通过聚合数量呈现，不保证逐条出现。

实验查询无 ID 时只取返回列表前 10 条，并保留列表数量与裁剪标记。指定 ID 未找到会返回 `not_found` 事实，这属于可用证据，不按读取异常处理。列表日期不能解读为对 `as_of` 做了数据库过滤。

证据预算固定为：

```text
每个子意图最多 12 条
整轮最多 30 条
最终 JSON 证据数组最多 32 KiB
```

先在各意图内部按优先级降序、同优先级按加入顺序裁剪，再处理总条数。超字节预算时逐条移除最低优先级事实，同优先级先删较晚加入者，直到 JSON 满足上限。裁剪后重新编号 E1、E2……，输出 `evidence_budget_trimmed` 和裁剪数量。

这不是按 tokens 精确预算，也不包括问题、历史和系统提示词总长度；不能用 32 KiB 推导整个模型请求大小。合并过程不保证每个意图至少保留一条，也没有跨意图按事实内容去重。严重裁剪可能让模型只见少数异常，因此回答必须保留样本和缺失限制。

### 3. 部分失败与无模型降级

子意图查询按顺序执行；单个读取失败会加入 `failed_intents`，其他查询继续。至少一个意图成功时可生成部分回答，`done.partial=true`；模型输入明确列出失败意图，不准补造其事实。全部意图查询失败则输出 `all_intents_failed`，不生成回答。

方法论意图无需市场证据，返回空证据也算成功。因此“全部失败”的判断是查询错误数量，不是证据条数等于零。`clarify/unsupported` 可以完全不调用模型，并仍输出完整事件序列。

未配置模型时，高置信问题仍可先输出本地证据，之后报 `llm_not_configured`；低置信问题则给确定性澄清。网页默认禁用未配置模型时的输入和分析按钮，所以 API 的能力边界比网页入口更宽。读取证据成功不代表 AI 回答成功。

### 4. 浏览器 NDJSON 与上游 SSE 分开

HTTP 成功开始后返回 `application/x-ndjson; charset=utf-8`，每行一个 `StreamEvent`，设置 `X-Accel-Buffering: no` 并及时 flush。**浏览器接口不是 SSE**；模型供应商到 `internal/llm` 的流才是 SSE。

| 事件 | 内容和用途 |
| --- | --- |
| `intent` | 已确认意图、`classification=rule/llm/fallback` 和 warnings |
| `plan` | 固定来源和查询步骤，无 SQL |
| `evidence` | 最终事实、ID、失败意图、裁剪与部分失败信息 |
| `prompt` | 提示版本、角色版本、模式、证据数、历史条数；不含提示原文 |
| `delta` | 回答片段，`delta/text` 兼容字段 |
| `citation` | 合法/非法证据 ID、引用总数、零引用标记 |
| `done` | 正常完成及部分失败，答案字符数 |
| `error` | 稳定错误码与脱敏说明；不是正常完成 |

确定性澄清也会输出空证据与 `deterministic-v1` 提示摘要。流开始前缺少运行时配置对象或复盘源返回 503；开始后业务错误通过 `error` 事件传递，此时 HTTP 状态仍为 200。消费端必须等 `done`，不能以收到 200 或几段文字当作成功。

服务端整轮 context 和响应写期限均为 75 秒；模型单次调用默认 60 秒，配置允许 1 秒～5 分钟，仍受整轮期限约束。一次低置信请求可能依次做分类和生成，共用这 75 秒。回调写失败会中止后续工作；浏览器取消也传到请求 context。

LLM 客户端请求 `/chat/completions`，支持配置 URL 路径前缀并禁止重定向。响应流总量最多 2 MiB，单行最多 1 MiB，必须收到 `[DONE]`；提前 EOF、坏 JSON、嵌入错误或超限均失败。401/403 分类为认证错误，408/429/5xx 为可重试上游错误，其余非成功响应为拒绝。这里的“可重试”只是分类，当前客户端没有自动重试循环。

上游错误正文不会进入错误信息。advisor 将生成失败收敛为 `llm_request_failed`，另保留 `canceled`、`deadline_exceeded`、`llm_not_configured` 等稳定码；可能已经送达的片段仍是未完成回答。

### 5. 引用校验的实际能力

[generate.go](../../internal/advisor/generate.go) 用正则识别 `[E数字]`，也接受小写 e。ID 必须存在于本轮最终证据中；返回去重后的 `valid/invalid`，`total` 则包含重复引用。没有匹配引用时 `zero=true`。

检查器不验证“这句话是否被这条证据支持”，不自动改写答案，不因非法引用或零引用阻止 `done`。网页把合法引用变成可跳转链接，标识非法引用，零引用显示“未引用本地证据”。这能暴露引用缺口，不能证明因果归因正确。

`review` 模式提示要求按“一句话结论、关键证据、可能判断偏差、其他解释、缺失数据、风险提示”六段输出，见 [review.md](../../internal/advisor/prompts/review.md)。服务端没有解析并强制验证六段齐全；这是提示契约，不能写成结构化响应校验已通过。

### 6. 配置优先级、落盘与访问边界

[DefaultLLM](../../internal/config/llm.go) 当前默认启用，地址 `https://api.openai.com/v1`，模型 `gpt-4.1-mini`，超时 `60s`，文件 `llm.config`，写入模式 `loopback`。没有 API key 时 `Configured=false`；默认模型名只是仓库默认值。

环境变量 `STOCK_WATCHER_LLM_BASE_URL/API_KEY/MODEL/TIMEOUT` 覆盖文件与默认值，并锁定对应字段。另有 `STOCK_WATCHER_LLM_ENABLED/CONFIG_FILE/WRITE_MODE`。私有配置文件必须是 schema 1、单份严格 YAML，允许四个可编辑字段；文件缺失不阻止无模型运行。完整说明见 [环境变量](../environment-variables.md)。

[LLMRuntime.Apply](../../internal/config/llm_runtime.go) 在锁内复制有效配置，只修改未锁字段，验证后写临时文件，权限 0600，文件同步、rename、目录同步成功后再替换内存值。落盘仅包含未锁字段，环境 key 不抄入文件。写失败不更新内存；若失败发生在 rename 之后，磁盘可能已更新，不能宣称整个流程具备跨文件与内存的事务回滚。

读取配置返回 `configured/enabled/write_enabled/write_mode`、字段来源和锁定状态，API key 只给存在标记与末四位；短 key 用 `****`。更新白名单为 `base_url/api_key/model/timeout`，字段缺失表示保持原值。连通测试只用已保存配置，15 秒外层超时、输出上限 1 token，不发送用户问题或本地证据。

| 边界 | 实际检查 |
| --- | --- |
| 模型地址 | HTTPS 或回环 HTTP；禁止 URL 凭证、查询、fragment；禁止重定向 |
| 聊天/归因 POST | JSON Content-Type、指定 action header；有 `Origin` 时 Host 必须匹配；`Sec-Fetch-Site` 只接受缺失、same-origin、none |
| 配置写入/测试 | 上述检查外再检查 `write_mode`；`off` 全禁、`all` 放行来源、`loopback` 检查直连 `RemoteAddr` |
| 公网只读模式 | 交互路由及非 GET/HEAD 一律 404；配置读取也隐藏；普通复盘查询仍开放 |

action 值分别为 `master-chat`、`review-analysis`、`llm-config`、`llm-test`。非公网模式下，聊天/归因不受 LLM `write_mode` 约束。缺失 Origin 被允许，检查也不是用户身份认证；回环反向代理会让 `RemoteAddr` 呈现回环地址，不得把这一开关当作公网鉴权。

应用没有内置普通用户/管理员。公开部署需要核实 `STOCK_WATCHER_WEB_PUBLIC_READ_ONLY` 与已有认证代理的实际配置，不能仅靠隐藏按钮保护可消耗模型额度的接口。公网网页隐藏 AI/设置入口且不加载这些配置；未配置、停用、加载失败分别显示状态。切换角色清理会话上下文，停止按钮中断流，退出页面取消在途请求；后端不会替用户保管历史。

## R｜结果：测试事实边界，不用流开始代表回答完成

以下命令是模块验证入口；本节列出现有测试证据，不表示本次文档撰写已执行这些检查：

```bash
go test ./internal/advisor ./internal/llm ./internal/config ./internal/dashboard
make review-smoke
make docker-review-smoke
```

| 可执行断言 | 现有测试证据 |
| --- | --- |
| 规则查询、低置信澄清、分类 JSON 拒绝、依赖与槽位冲突 | `TestRuleSingleIntentUsesDeterministicQueryAndCitations`、`TestLowConfidenceWithoutLLMClarifiesWithoutQuery`、`TestLLMClassificationValidAndInvalidJSON`、`TestDependencyAndSlotConflictClarifyWithoutQuery` |
| 12/30/32 KiB 裁剪及可见 warning | `TestEvidenceBudgetTrimmingAndVisibility`、`TestEvidenceJSONNeverExceedsBudget` |
| 部分失败继续、全部失败不生成、无模型/超时/取消/回调失败 | `TestPartialFailureAnswersAndAllFailureDoesNot`、`TestLLMNotConfiguredTimeoutCancellationAndEmitFailure` |
| 引用不改写答案、历史最多三条、敏感配置不进入证据 | `TestCitationValidationDoesNotRewriteAnswer`、`TestHistoryKeepsOnlyRecentThreeTurns`、`TestSensitiveConfigurationAndPromptTextAreNotSent` |
| 上游 SSE、缺 DONE、超限、重定向和 HTTP 错误不泄露正文 | [client_test.go](../../internal/llm/client_test.go) |
| action/同源/JSON 拒绝，配置锁定/脱敏，连通测试不带用户数据 | `TestAdvisorEndpointsRequireSameOriginActionAndJSON`、`TestLLMConfigWriteModesAndRequestGuards`、`TestLLMConfigViewMasksKeyAndSavesUnlockedFields`、`TestLLMConfigTestSendsNoUserDataAndClassifiesErrors` |
| 公网模式交互接口不可达 | [handler_test.go](../../internal/dashboard/handler_test.go) 的公网只读测试 |
| 真实页面未配置禁用、mock 流式回答、有效/无效/零引用、角色与移动布局 | [review_smoke.py](../../tests/review_smoke.py) |

部署验收使用单个浏览器串行检查：匿名访问、代理拒绝和获准访问；公网模式同时断言 DOM 隐藏和接口 404，非公网配置禁写分支断言 403。还要检查标题/URL、控制台错误、网络请求、手动取消和中途断流，不能只观察首个 delta。真实供应商可用性、代理身份与线上密钥仅验证存在或生效，不输出值；没有真实环境时明确记录“部署后未验证”。
