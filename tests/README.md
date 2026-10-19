# 验证范围

`make ci` 验证格式、go vet、`go test -race ./...`、本机构建、实际 CLI 冒烟和发布包。`make docker-smoke` 构建镜像并运行同一组 CLI 断言，再验证低权限用户、实际 Compose 的健康检查与原样环境变量、SIGTERM 生命周期。

`bash tests/run_test.sh` 在临时仓库内用 mock systemctl/ps 和 CLI 验证 `make run`：同仓库同配置时按“构建、配置校验、停止后台、启动前台”的顺序执行；构建/校验失败保留后台，停止失败或仍有 PID 时不启动前台。不同配置/仓库不停止服务；服务查询拒绝/超时、缺配置、dotenv 加载和 make dry-run 均有断言。该回归检查包含在 `make smoke` / `make ci` 中。

`python3 tests/strategy_model_test.py` 使用 Node.js 20+ 运行网页计算模型，与原脚本对照 55 组有效情景，并验证 22 组无效输入。金额取整、风险/现金/费用限制、多个持仓批次与固定止盈金额都必须一致。该检查已接入 `make smoke` / `make ci`，因此本地 CI 需要 Python 3 和 Node；生产 Go 镜像不需要这些运行时。

看板使用真实 Chromium 验证，先安装 Python Playwright 及其 Chromium，再执行：

```bash
make build
python3 tests/dashboard_smoke.py ./bin/stock-watcher
make image IMAGE=stock-watcher:dashboard-test
python3 tests/dashboard_smoke.py --image stock-watcher:dashboard-test
```

可通过 `PLAYWRIGHT_CHROMIUM_EXECUTABLE` 或 `--browser` 指定已安装的 Chromium。测试以真实 `stock-watcher run` 启动看板与调度循环，容器模式使用生产 entrypoint、只读根目录、8080 容器端口和本机随机端口映射。所有测试数据通过 `tests/fixtures/dashboard` 调用生产 SQLite 初始化和快照事务写入：三个池内股票、两个有行情、一个缺失，以及 v1/v2 两日排名报告。测试禁止行情客户端通过外网请求，使用关闭的本地代理模拟上游故障；凭证关闭。测试覆盖四个页面的独立加载、当前导航项、旧 hash 重定向、`/stocks?status=`、`/chart?code=` 与 `/rankings?as_of=` 深链接、跨页点击、页面标题、控制台与网络、资产版本及缓存策略、全池查询和排序、历史排名、新日报自动跟随和明确选择历史后的保持、移动布局、空数据库、网关 403/接口 503 与浏览器超时后的恢复、只读 API 和 SIGTERM。原生模式另验缺失配置无法启动。

浏览器测试仅使用一个浏览器并串行运行有数据/空数据库场景；结束删除临时目录、进程与容器。桌面和移动截图保存在被 Git 忽略的 `.scratch/dashboard-verification/`，标准输出记录本地二进制 SHA-256 或所测镜像 ID。当前看板没有账号或角色体系，预期允许匿名读取；不存在普通用户和管理员界面，不能将匿名浏览器验证表述为角色权限验证。测试不连接真实部署 URL，本地通过仍需要部署后重新检查。

已保存数据的浏览器 fixture 使用最近 40 天均闭市的 mock 日历，使后台调度找不到可同步的完成交易日，避免运行当天的失败同步写入改变固定快照日期。HTTP 生产启动模式、路由与身份约束不变；真实同步成功、单股隔离、外部失败及生命周期由监控测试和 CLI 冒烟独立验证。

同一浏览器还运行 `calculator_browser_checks.py`：从看板进入 `/calculator`，验证 215 股案例、底仓及加仓上限、固定净利润、多批持仓、错误提示与焦点、清空和过期结果隐藏；断言计算期间无网络请求、无浏览器存储，断网仍可计算，空数据库仍可使用。模块资源注入 403/503 与真实连接停滞以验证加载失败、10 秒超时和重载恢复；直接核对计算器 JS/CSS 内容与缓存头，保存桌面/移动截图。两种运行模式都执行这些断言。

`experiment_browser_checks.py` 验证 `/experiments` 的基线与上影线对照、父子导航、URL 恢复、同口径指标、分组、固定快照 K 线、搜索和筛选、无记录/无入选/失败状态、移动布局、401/403/503/超时恢复、快速切换取消，以及版本不同禁止计算改善量。fixture 通过真实实验引擎生成结果。Go 测试另验留出期价格变化不能影响开发结果、过滤改变入选且不污染池基线、不可覆盖和快照摘要、独立锁、中断恢复，以及 CLI 在行情库离线时仍能沿父快照复现；失败运行必须落档，实验前后行情库摘要不变。

`go test -race ./internal/monitor ./internal/config` 验证启动全池同步与分析、缓存强制刷新、盘中/收盘/周末/节假日、时区与过期窗口、跨收盘重试、同步失败不发送旧缓存、飞书失败只重试投递、同日内容变化去重、新通道、v1 已送达日期迁移和取消。Run 循环测试使用虚拟时间推进重试，不真实等待几分钟。凭证文件测试覆盖相对路径、环境变量优先级、显式清空、缺失/错误文件和错误脱敏；CLI 冒烟另验文件读取与非 root 权限拒绝。

| 验收分支 | 可执行检查 |
| --- | --- |
| 启动成功 | init 初始化 SQLite；check JSON ready、股池非空、通知关闭；status 账本版本；plan 生成计划 |
| 缺少配置/数据目录 | 返回退出码 1；数据目录不会自动创建 |
| 存储类型错误 | 本地磁盘配 require_cifs=true 必须失败 |
| 通知配置缺失 | 启用通道但没有 webhook 必须失败，禁止请求 |
| 权限不足 | 容器 UID 65534 确认 600 配置存在但不可读，CLI 返回 cannot open configuration file |
| 异常/超时/清理 | Go 包测试中的 httptest/mock；冒烟单命令 30 秒上限，超时不能算预期失败 |
| 容器运行形态 | 原生 Go entrypoint、只读根目录、资源限制、无 capabilities，SIGTERM 后退出码 0 |
| UI 成功/异常 | dashboard_smoke.py 检查真实页面及 API、全池与快照、历史排名、筛选排序、空数据、503/超时恢复 |
| UI/登录角色 | 看板允许匿名读取；无普通用户/管理员角色；写接口返回 405；文件系统权限另验 |

CLI 冒烟在临时目录使用测试配置，关闭通知，容器禁用网络；浏览器容器保留本机端口映射并将行情代理指向关闭的本地端口。两类冒烟都不使用真实股池行情、凭证、生产日志或账本。测试结束删除临时目录和本次测试容器。测试配置宽范围日历仅用于存储及生命周期检查，不代表真实交易日历。

CI 不具备 Windows 挂载；真实 CIFS 需要在服务器上人工验收：在共享盘新建独立临时子目录，将测试配置 `require_cifs` 保持 true，关闭全部通知；用 Compose 的 HOST_DATA 指向该目录，运行 version/check/status。启动隔离实例验证 healthy、SIGTERM 退出，并删除该实例与临时文件。不得把测试实例指向正式写入目录，或发送真实群消息。

发布前检查包内 `sha256sum -c SHA256SUMS`、二进制 version 与 COMMIT 一致。部署后按 docs/deployment.md 对实际实例再次验收；本地通过不能替代部署结果。
# 多源行情

`make sources-smoke` 用本地 HTTP/TCP fixture 启动真实 Go CLI，覆盖腾讯失败后回退 BaoStock/Tushare、Tushare 权限/限流后回退东方财富、复权与原始量入库、缺 Token、全源失败保留已有数据与凭证脱敏。容器模式运行 `python3 tests/sources_smoke.py --image stock-watcher:<SHA>`；Linux 上使用 host 网络访问回环 mock 服务，运行制品、命令和环境变量与生产一致，数据目录为临时普通目录。测试不读取私有配置、不访问行情公网或飞书。

`go test -race ./internal/market ./internal/monitor ./internal/storage ./internal/config` 补充分页、取消、超时、缺字段、数据覆盖校验、来源切换完整回补和成交量换算。`make dashboard-smoke` 验证浏览器中来源与成交量单位，使用混合腾讯/BaoStock fixture；前端测试配置强制只启用腾讯，并使用关闭的 HTTPS 代理，防止新 TCP 数据源在测试中访问公网。

## 分钟采集与图表

`make intraday-smoke` 以真实 Go CLI 对接本地 HTTP fixture，验证 schema 2 → 3 迁移、5 分钟落库及四周期 API、日线接口失败时分钟仍继续采集、403/超时保留数据、SIGTERM 和写锁释放。容器使用 `python3 tests/intraday_smoke.py --image stock-watcher:<SHA>` 运行同一断言，生产 entrypoint、只读根目录、资源限制及临时数据挂载；Linux host 网络仅用于访问回环 mock。

Go 测试覆盖形成中更新、OHLC/时间校验、午休边界、缺根不补造、滞后窗口保留较新数据、校准修订与重启去重、过期日历、配置错误、单股隔离、取消与存储失联。已有日线和报告在迁移后可重读，空库初始化、只读连接写入拒绝与 CIFS 类型限制另有断言。

`dashboard-smoke` 在同一个 Chromium 中增加四周期切换、真实 SVG 蜡烛/成交量/MA 数值、形成中状态、校准日期、只有分钟数据的股票、移动布局、无数据/403/503/超时/取消与恢复。固定图表 fixture 关闭分钟轮询避免被网络失败状态覆盖；采集调度由 intraday-smoke 独立验证。应用无账号体系，匿名只读成功、POST 405 与网关 403 分别验证，不虚构普通用户/管理员角色。
## 单笔亏损硬约束

策略测试覆盖 -2.99%／-3%／-3.01% 边界、手续费与滑点和分位取整、无止损空间拒绝、跳空真实超限、跌停未决，以及高平均收益不能覆盖历史亏损超限。实验判定测试覆盖风险否决、未决/空样本、证据不足、持仓缩短、盈利达成率提高和软指标取舍。存储测试用旧版“开发候选”档案验证当前复核结果，同时逐字核对原档案未改写；除权未决不计入完成盈亏。浏览器额外验证硬条件提示、原结论与当前结论分开、超限样本筛选、两个盈利目标与持仓时间。

最高价触达回归覆盖收盘回落仍触达、买入当日不可卖、退出后高点排除、开盘退出后的反弹排除、日内止损与触达先后不明、费用滑点、旧档案缺失，以及浏览器规则卡片、触达筛选与逐笔实际亏损并列展示。

次日触达回归覆盖首个可卖出交易日、延迟触达不计成功、未决/顺序不明/旧证据缺失、80%但样本不足、亏损优先、期限缩短仍隔离原最终区间。浏览器使用真实实验fixture核对两档笔数/分母/比例、目标差距、原期限与次日期限说明、次日筛选、空态及移动布局。
