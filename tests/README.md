# 验证范围

`make ci` 验证格式、go vet、`go test -race ./...`、本机构建、实际 CLI 冒烟和发布包。`make docker-smoke` 构建镜像并运行同一组 CLI 断言，再验证低权限用户、实际 Compose 的健康检查与原样环境变量、SIGTERM 生命周期。

`bash tests/run_test.sh` 在临时仓库内用 mock systemctl/ps 和 CLI 验证 `make run`：同仓库同配置时按“构建、配置校验、停止后台、启动前台”的顺序执行；构建/校验失败保留后台，停止失败或仍有 PID 时不启动前台。不同配置/仓库不停止服务；服务查询拒绝/超时、缺配置、dotenv 加载和 make dry-run 均有断言。该回归检查包含在 `make smoke` / `make ci` 中。

`go test -race ./internal/monitor ./internal/config` 验证启动全池同步与分析、缓存强制刷新、盘中/收盘/周末/节假日、时区与过期窗口、跨收盘重试、同步失败不发送旧缓存、飞书失败只重试投递、同日内容变化去重、新通道、v1 已送达日期迁移和取消。Run 循环测试使用虚拟时间推进重试，不真实等待几分钟。凭证文件测试覆盖相对路径、环境变量优先级、显式清空、缺失/错误文件和错误脱敏；CLI 冒烟另验文件读取与非 root 权限拒绝。

| 验收分支 | 可执行检查 |
| --- | --- |
| 启动成功 | check JSON ready、股池非空、通知关闭；status 账本版本；plan 生成计划 |
| 缺少配置/数据目录 | 返回退出码 1；数据目录不会自动创建 |
| 存储类型错误 | 本地磁盘配 require_cifs=true 必须失败 |
| 通知配置缺失 | 启用通道但没有 webhook 必须失败，禁止请求 |
| 权限不足 | 容器 UID 65534 确认 600 配置存在但不可读，CLI 返回 cannot open configuration file |
| 异常/超时/清理 | Go 包测试中的 httptest/mock；冒烟单命令 30 秒上限，超时不能算预期失败 |
| 容器运行形态 | 原生 Go entrypoint、只读根目录、资源限制、无 capabilities，SIGTERM 后退出码 0 |
| UI/登录角色 | 不适用：本项目为 CLI，权限检查针对文件系统 |

冒烟在临时目录使用测试配置，关闭通知，容器禁用网络；不使用真实股池行情、凭证、生产日志或账本。测试结束删除临时目录和本次测试容器。测试配置宽范围日历仅用于存储及生命周期检查，不代表真实交易日历。

CI 不具备 Windows 挂载；真实 CIFS 需要在服务器上人工验收：在共享盘新建独立临时子目录，将测试配置 `require_cifs` 保持 true，关闭全部通知；用 Compose 的 HOST_DATA 指向该目录，运行 version/check/status。启动隔离实例验证 healthy、SIGTERM 退出，并删除该实例与临时文件。不得把测试实例指向正式写入目录，或发送真实群消息。

发布前检查包内 `sha256sum -c SHA256SUMS`、二进制 version 与 COMMIT 一致。部署后按 docs/deployment.md 对实际实例再次验收；本地通过不能替代部署结果。
