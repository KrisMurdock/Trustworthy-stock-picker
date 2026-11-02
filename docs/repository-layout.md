# 工程目录与补齐范围

以 `/root/data/codebase/support-agent` 为工程组织参考，按 stock_watcher 的 Go CLI、本地/Windows CIFS 存储和定时任务形态补齐。

| 工程能力 | 原有 stock_watcher | support-agent 参考 | 本仓库落点 |
| --- | --- | --- | --- |
| 容器构建 | 根 Dockerfile | 根 Dockerfile；没有受 Git 管理的 docker/ | 保留唯一根 Dockerfile，增加版本标签、构建上下文白名单 |
| 容器部署 | README 中手动 docker run | 由其服务部署体系负责 | docker/compose.yaml、根 .env.example、docker/README.md |
| 环境配置 | 内置 Go 默认值 + 环境变量 | .env.example、环境变量文档 | .env.example、docs/environment-variables.md；私有凭证不进 Git/镜像/发布包 |
| 开发命令 | test、vet、build、image、clean | Makefile 提供开发和打包入口 | make help/run/check/status/ci/package/docker-smoke |
| 发布包 | 单个本地二进制 | .builds 下按系统和架构打包 | .builds/stock-watcher/linux/{amd64,arm64}，带股池、文档、SHA、校验和 |
| 自动验证 | Go 测试 | GitLab CI 和 scripts | .github/workflows/ci.yml、scripts/ci.sh，适配本仓库 GitHub 托管 |
| 冒烟验收 | Go 包内测试 | tests/ | tests/smoke.sh：真实二进制与容器、配置/存储失败、权限及退出 |
| 开发运维说明 | README 与 systemd 模板 | docs/、MR 模板 | docs/development.md、docs/deployment.md、GitHub PR 模板 |

## 目录职责

```text
cmd/                     Go 命令入口
runtime/                 可嵌入其他 Go 项目的运行时入口
internal/                配置、数据源、存储、筛选、研究、通知、日志等模块
leaders.yaml             已核对股池（外部运行时数据）
feishu.config            旧凭证文件（仅显式兼容，不提交）
leaders.pending.yaml     待人工确认名单
.env.example             本地/生产环境变量参考
Dockerfile               唯一多阶段镜像构建定义
.dockerignore            构建上下文白名单
Makefile                 开发、验证、构建与打包入口
docker/                  Compose 部署及宿主机路径示例
deploy/                  systemd 服务模板
scripts/                 构建、打包、开发启动辅助与收盘后离线 artifact 生成脚本
tests/                   CLI/容器冒烟与验收说明
docs/                    工程、配置与运维说明
.github/                 GitHub Actions、PR 模板
bin/                     本机二进制（忽略）
.builds/                 发布包（忽略）
logs/                    有限滚动日志（忽略）
data/                    本地数据目录或 Windows 共享盘链接/挂载（忽略）
```

`scripts/` 中的 Go 启动、构建和打包脚本仍只组织原生二进制；`scripts/secondstage/` 是独立的收盘后离线 artifact 生成链，由 systemd timer 调用，生产 Go 进程不会 exec Python。业务配置和凭证通过环境变量加载；观测股池保持独立 `leaders.yaml`，避免 `.env` 随股池规模膨胀。

support-agent 的 LLM 提示词、内部镜像仓库与 GitLab 私有流水线依赖不适用于本项目，因此不创建 db/、conf/prompts 或空占位目录。本项目已提供 Go 内嵌只读网页及 HTTP API，同一监听端口承载 `/healthz`、`/readyz` 和 `/api/status`，无需独立前端容器。SQLite 迁移由 `internal/storage` 的版本化代码管理，不创建空的 db/ 目录。

`runtime/` 只暴露配置、初始化、运行、看板和日志入口，内部存储与行情实现仍由 `internal/` 持有；全市场四灯库采用惰性只读打开，服务启动时数据库尚不存在会在后续请求重试。`internal/allmarket` 持有全市场日线发现、增量同步、断点续跑和独立四灯信号归档逻辑，数据落在独立 `<data_dir>/all-market.db`，不参与生产股池或排名；完整同步后可生成独立低估值快照。`internal/storage` 在其 schema 3 中保存 `four_light_run`、`four_light_daily` 和 `four_light_event`，汇总读取使用单一只读事务。`internal/lowvaluation` 只依赖日线存储适配器和财务快照，按“现价低于财务研究区间下沿、距下沿折价升序”生成全市场候选；`internal/lowvaluationstore` 负责读取持久化快照，避免估值包反向依赖存储。`internal/valuetrapped` 只接收低估值全部候选和同信号日的套牢扫描结果，按“折价、套牢量、20 日成交额、代码”生成交集观察，不引入综合分；`internal/valuetrappedstore` 读取独立快照。`internal/trappedscan` 负责低套牢横截面扫描的套牢 V3 调用、换手率抓取与候选代码过滤。`internal/hkconnect` 持有当前港股通股票的发现、港股交易日解析、10 年不复权与前复权日线同步，以及基于日线趋势门槛的独立港股通观察排名，数据落在独立 `<data_dir>/hk-market.db`；该排名不复用 A 股初筛、精排模型或成交模拟，由主 `run`/`once` 生成独立公众号稿并投递到 `hk_connect` 路由。`internal/hkrecommend` 只定义港股推荐的持久化数据契约，避免文章渲染与存储层形成循环依赖。`internal/fundamentals` 负责追加式 PIT 年度财务事实、observed/vendor 可见性、抓取批次和 5-10 年标准化盈利/ROE/派息；`internal/storage` 将其写入独立 `<data_dir>/fundamentals.db`（schema 4），使用 `.fundamentals.lock`，不扩展 `market.db`；`internal/fairvalue` 持有 v1/v2/v3 研究估值与滚动校准原语，v3 尚未进入公众号。`internal/marketcontext` 负责指数与行业/概念板块的发现、增量下载和独立 `context.db`，由默认股票同步流程并发调用；`internal/marketforecast` 只读该库的上证与创业板日线及可选 5 分钟缓存，按截止日确定性重训下一交易日方向、上午/下午树模型并输出周内统计，不写数据库；`internal/selection/engine.go` 定义日常算法计算边界，`internal/firststage` 生成高召回候选池，`internal/secondstage` 在候选池上执行第一层百分位与可配置离线模型百分位的加权 blend Top10 精排；`internal/predictor` 持有 5 分钟实验预测的特征、树模型、卡尔曼状态、预测库和投递逻辑；`internal/dayrange` 持有盘前全天高低价的数据审计、确定性树模型、区间校准、预测/核对、飞书投递和评估，持久化落在独立 `dayrange.db`；`internal/fourlightalign` 与 `cmd/four-light-align` 只读目标软件灯色样本和前复权日线，输出逐灯一致率及全红转换差异，不修改四灯公式或生产排名；`internal/turnover` 与 `internal/turnoverstudy` 持有研究型换手率/成交额特征和受控收益验证，`cmd/turnover-study` 只读 `market.db`、新浪复权价缓存与搜狐日度指标缓存；`internal/health` 汇总只读业务健康；任务和快照仍由 `internal/storage` 管理。实验代码保持独立，实验结果不会自动进入生产推送。

5 分钟预测把模型、预测和开关写入 `<data_dir>` 下的 `models/intraday/`、`predictions.db` 和 `state/`，不另建本地缓存目录。网页在 `/rankings` 增加预测区块。盘前全天高低价预测使用独立 `<data_dir>/dayrange.db`、`state/dayrange-delivery.json` 分页投递账本、`state/premarket-execution-delivery.json` 执行确认账本和顶层 `/day-range` 页面；第一版不提供盘中剩余时段更新。
