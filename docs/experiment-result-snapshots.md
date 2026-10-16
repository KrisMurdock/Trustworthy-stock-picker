# 实验结果快照

策略实验室 `/experiments` 为每次运行展示一份结果快照。列表提供盈利目标、达标平均持仓和最大已实现亏损摘要，详情支持切换目标／分组／验证阶段、查看统计口径和下载 JSON。后续修改规则或重跑生成新的实验 ID，不覆盖已结束记录。

## 指标口径

| 指标 | 计算范围 | 无数据时 |
|---|---|---|
| 达到盈利目标的平均持仓日 | 首次确认净目标触达，仅成功交易；买入次日为第 1 个可卖出日 | 无成功交易或原档案未记录，不显示为 0 |
| 最大已实现单笔亏损 | 已结束交易最差净收益的亏损幅度，按该笔实际投入、含交易成本；非账户回撤 | 无已结束交易为空；全部已结束交易不亏损时为 0% |
| 盈利目标触达率 | 确认触达数 / 全部样本，未决仍在分母 | 无样本或触达证据缺失为空 |
| 全部已结束交易平均持仓 | 包含亏损与到期退出 | 无已结束交易为空 |
| 最大持仓不利变动 | 原引擎保存的单笔持仓不利变动幅度，和实际退出亏损分开 | 未记录为空；无止损速度实验未计算此项 |
| 含失败等待分 | 无止损速度实验专用：成功按首次触达日，未达标或未决按 21 日 | 其他实验未计算，不套用为实际持仓 |

每个分组保存净盈利目标、止损目标（0 明确表示无价格止损）、分批上限、可卖出日期上限、实际信号日期范围、样本数、已结束数、未决数及跳过数。日线最高价触达不要求收盘达标；旧引擎的价格机会与新速度引擎的模拟整笔可执行目标具有不同口径，不能直接视为同一指标。

## 归档与旧数据兼容

`experiment.Record.result_snapshot` 是可选、版本化的结果摘要，包含实验 ID、数据快照 ID、引擎、代码版本、结束时间、统计行、说明和 SHA256。结束实验时，在同一条 SQLite 更新中与完整结果、列表摘要一起封存；已结束记录不可再次更新。读取时校验摘要与运行元数据和内容散列。

旧数据库不需要表结构迁移。没有此字段的已完成档案，从**原始保存的研究统计**生成相同结构，只作只读投影，不改写数据库，也不采用 `current_review` 的新规则计算。旧字段不足时保留未知，不把当前复核或新行情伪装成当时结果。运行中、失败、取消和中断记录显示明确空状态，不保留上次选中实验的指标。

当前规则复核仍保留在旧实验详情中，与结果快照分开。无止损速度实验使用自身规则，标记“研究结果快照”，不套用旧版 3% 亏损否决规则。没有新增网页登录权限或写 API；列表与详情沿用只读 `/api/experiments`，访问控制继续由部署网关负责。

## 保存无止损持仓实验

为避免把新的速度结果混入旧条件低吸模型，离线工具新增显式的归档选项：

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git rev-parse HEAD)" -o bin/bottom-study ./cmd/bottom-study
./bin/bottom-study -snapshot /path/to/stocks.json \
  -compare-context /path/to/context.json -optimize-speed \
  -from 2026-01-01 -train-through 2026-06-30 -through 2026-10-12 \
  -archive-config config.yaml > /path/to/report.json
```

此命令照常输出完整 240 对研究报告，并将**训练选中的参数及同参数 A/B、训练/后段对照**作为一个实验记录保存到配置数据目录的 `experiments.db`。未达训练样本门槛而没有候选时，归档明确失败，可去掉归档选项查看完整网格。它不是为全部参数各创建一条记录，避免把 480 个候选误当成 480 次独立验证。

归档遵循配置的 CIFS 要求、已有独立实验写锁、原子更新和数据快照散列校验，不打开行情写库或创建行情提供方，不发送通知。未传 `-archive-config` 时保持原有纯离线、不写档案行为。标准实验命令在成功结束时自动保存结果快照，无需新增参数。

归档完成后刷新实验页。相同输入重跑也会创建新 ID，旧快照保持不变；下载 JSON 是当前选中实验的完整结果快照，包含所有保存的分组，切换分组不会改动下载内容。

## 验证与发布边界

针对性测试覆盖封存散列、统计分母与成功均日、未知与零值区分、当前复核不覆盖、重跑不覆盖、旧档案只读兼容、空库、归档配置缺失和取消。真实 Chromium 检查指标 DOM、目标／阶段切换、JSON 下载、移动端宽度、URL、标题、控制台和请求，以及 401/403、503、超时、重试、失败／运行中／空结果状态。

本地验证和构建记录在 `.scratch/result-snapshots/`；截图在 `.scratch/dashboard-verification/`。本次不发布或重启生产服务。**本地验证通过，部署后未验证**；生产部署 URL 和真实网关账号尚未验证。合并部署时需要核对 `stock-watcher version`、静态资源版本及缓存头，并在实际部署 URL 复查实验页、JSON 下载及网关权限。

### 本次改动范围

- `internal/experiment/experiment.go`、`result_snapshot.go`、`result_snapshot_test.go`：封存结构、统计口径及空值／篡改回归。
- `internal/storage/experiments.go`、`result_snapshots_test.go`：原子保存、只读旧档案兼容、列表／详情一致、重跑不覆盖。
- `internal/bottomstudy/result_snapshot.go`、`result_snapshot_test.go`：将训练选中的两组速度实验映射为结果分组，保留亏损和未决。
- `cmd/bottom-study/main.go`、`main_test.go`、`archive.go`、`archive_test.go`：显式归档选项及完整写入／取消验证。
- `internal/dashboard/static/experiments.html`、`experiments.js`、`experiments.css`：结果卡片、列表摘要、分组切换、JSON 下载及手机布局。
- `tests/experiment_browser_checks.py`、`tests/fixtures/dashboard/main.go`：真实浏览器场景与无止损档案夹具。
- `docs/experiments.md`、本文：使用入口、统计口径及复现说明。

### 验收记录

代码与嵌入式前端版本：`a43dd47a7d8d73d79fd187f420818a4e5f143a53`。镜像为 `stock-watcher:a43dd47a7d8d73d79fd187f420818a4e5f143a53`，镜像 ID 为 `sha256:02a579cd9e7f4d8946597e92d0ce8db08b5ce01ac649f5d2d8678006a20d1fdf`。二进制、HTML、JS、CSS 散列保存在 `.scratch/result-snapshots/verification.json`。

- `GOFLAGS=-p=1 make fmt-check vet test`：通过。初次并行全量检查中，原有 `TestDashboardConcurrentPublicationIsAtomic` 出现一次行情读取失败；未修改行情代码，独立 `-race -count=3` 全过，对应存储包和降低并发后的全量检查通过。初次日志保留在 `checks.log`，复查在 `concurrent-recheck.log` 和 `checks-serial.log`。
- `make build smoke`、`make image`：通过。`go test -race ./internal/experiment ./internal/bottomstudy ./internal/storage ./cmd/bottom-study`：通过；最后新增的非法 CLI 参数断言也通过。
- `python3 tests/dashboard_smoke.py bin/stock-watcher`：真实 Chromium 桌面和手机通过。现有应用无内置登录，以匿名只读访问验证，网关 401/403 用浏览器路由模拟，服务端继续拒绝写请求。
- 真实冻结数据重新计算＋归档＋页面展示＋下载：通过，16 个结果分组与研究报告一致，完整 240 对交易统计未变。3% 组合组后段显示成功均日 6.67、全部结束均日 7.37、最大已实现亏损 8.21%、18/19 触达。股票和背景输入散列与原报告匹配。
- 本地真实数据验收 URL：`http://127.0.0.1:46157/experiments?id=7ce0c39e26c167913172fb7a9f72118b`；临时服务已清理。JS/CSS 响应字节与源码一致，缓存头允许及时刷新；下载内容与存档一致，控制台无错误。截图：`.scratch/result-snapshots/actual-result-snapshot-desktop.png`、`actual-result-snapshot-mobile.png`。
- `python3 tests/dashboard_smoke.py --image stock-watcher:a43dd47a7d8d73d79fd187f420818a4e5f143a53` 与 `bash tests/smoke.sh --image stock-watcher:a43dd47a7d8d73d79fd187f420818a4e5f143a53`：通过。复用生产启动命令和环境变量，在临时目录与 mock 外部行情下检查页面、只读 API、配置缺失、权限失败、健康、SIGTERM；临时进程、容器与数据库已清理。
