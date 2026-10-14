# 验证范围

`make ci` 验证格式、go vet、`go test -race ./...`、本机构建、实际 CLI 冒烟和发布包。`make docker-smoke` 构建镜像并运行同一组 CLI 断言，再验证低权限用户、实际 Compose 的健康检查与原样环境变量、SIGTERM 生命周期。

`bash tests/run_test.sh` 在临时仓库内用 mock systemctl/ps 和 CLI 验证 `make run`：同仓库同配置时按“构建、配置校验、停止后台、启动前台”的顺序执行；构建/校验失败保留后台，停止失败或仍有 PID 时不启动前台。不同配置/仓库不停止服务；服务查询拒绝/超时、缺配置、dotenv 加载和 make dry-run 均有断言。该回归检查包含在 `make smoke` / `make ci` 中。

看板使用真实 Chromium 验证，先安装 Python Playwright 及其 Chromium，再执行：

```bash
make build
python3 tests/dashboard_smoke.py ./bin/stock-watcher
make image IMAGE=stock-watcher:dashboard-test
python3 tests/dashboard_smoke.py --image stock-watcher:dashboard-test
```

可通过 `PLAYWRIGHT_CHROMIUM_EXECUTABLE` 或 `--browser` 指定已安装的 Chromium。测试以真实 `stock-watcher run` 启动看板与调度循环，容器模式使用生产 entrypoint、只读根目录、8080 容器端口和本机随机端口映射。所有测试数据通过 `tests/fixtures/dashboard` 调用生产 SQLite 初始化和快照事务写入：三个池内股票、两个有行情、一个缺失，以及 v1/v2 两日排名报告。测试禁止行情客户端通过外网请求，使用关闭的本地代理模拟上游故障；凭证关闭。测试覆盖 DOM、URL、页面标题、控制台与网络、资产版本及缓存策略、全池查询和排序、历史排名、新日报自动跟随和明确选择历史后的保持、移动布局、空数据库、网关 403/接口 503 与浏览器超时后的恢复、只读 API 和 SIGTERM。原生模式另验缺失配置无法启动。

浏览器测试仅使用一个浏览器并串行运行有数据/空数据库场景；结束删除临时目录、进程与容器。桌面和移动截图保存在被 Git 忽略的 `.scratch/dashboard-verification/`，标准输出记录本地二进制 SHA-256 或所测镜像 ID。当前看板没有账号或角色体系，预期允许匿名读取；不存在普通用户和管理员界面，不能将匿名浏览器验证表述为角色权限验证。测试不连接真实部署 URL，本地通过仍需要部署后重新检查。

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
