# 开发、验证与发布包

需要 Linux、Go 1.26.4、Git、GNU Make、Bash、jq、GNU coreutils。容器检查与部署额外需要 Docker Engine、Compose ≥ 2.30.0。首次构建需要下载 Go 依赖及公共基础镜像。

```bash
make help
make ci                       # gofmt、vet、race tests、构建、CLI 冒烟、发布包
make docker-smoke             # 镜像构建 + 无网络容器冒烟
```

CI 与冒烟使用临时目录、关闭通知，不读取根目录私有 `.env`，不连接真实行情或飞书。Go 包内测试使用 mock/httptest 验证网络超时和取消等分支。需要压低构建占用时执行 `GOMAXPROCS=2 GOFLAGS=-p=2 make ci`。

日常运行：

```bash
make build
make check
make status
make run
```

业务配置已内置，启动不读取 `config.yaml`；`leaders.yaml` 和 `feishu.config` 仍按当前工作目录解析。默认使用本地 `data/` 存储，`init` 会自动创建；使用 Windows 挂载盘时设置 `STOCK_WATCHER_REQUIRE_CIFS=true` 和对应 `STOCK_WATCHER_DATA_DIR`。`make run` 用于前台运行，会构建并加载开发 `.env`。若 `stock-watcher.service` 已运行同一个仓库二进制，则先用原生 `check-config` 校验内置配置与日志环境变量，通过后正常停止后台服务，确认 MainPID 为 0，再进入前台运行。构建或配置校验失败时不停止原服务；停止失败或超时时中止前台启动并报错。

Go `run` 启动后立即同步最近已收盘交易日的完整股池、生成分析并投递尚未过期的 T+1 消息，之后进入定时轮询；已送达的同日同通道消息不重复发送。盘中不会推送已过买入时点的旧推荐。同步失败会自动重试，原始行情和报告保持在数据目录。`once` 仍仅执行一次受内置 `daily.run_at` 限制的定时流程。

没有匹配服务时直接 exec Go；其他仓库或不同 `CONFIG` 不会触发服务停止，仍由数据目录写锁判断是否冲突。每次 systemd 查询限时 3 秒，停止服务限时 30 秒。查询不可用时继续原生启动，由 CLI 返回真实结果。容器和 systemd 均直接启动 Go。`make -n run` 只预演命令，不启动或停止进程。

前台进程通过 Ctrl+C 退出，之后可执行 `systemctl start stock-watcher.service` 恢复后台运行；SELinux 环境先按部署文档恢复二进制标签。前台使用当前 shell 和开发 `.env`，后台使用服务 EnvironmentFile，凭证须按对应方式配置。`check-config` 只校验配置，不代表数据目录或外部服务健康；完整健康检查仍使用 `check`。不要删除 `.stock-watcher.lock` 文件绕过正在运行的实例。

## 发布制品

```bash
make package                  # 默认 linux/amd64
make package BUILD_GOARCH=arm64
cd .builds/stock-watcher/linux/amd64
sha256sum -c SHA256SUMS
./bin/stock-watcher version
```

发布目录含二进制、环境变量示例、已确认/待确认股池、systemd 模板、README 与 docs、`VERSION`、`COMMIT`、`SHA256SUMS`。这是原生运行包；Docker/CI/开发脚本从源码仓库使用。arm64 可以交叉构建，但需在对应机器验收运行。

默认 VERSION 为完整 Git HEAD，镜像 OCI revision 与 `stock-watcher version` 同值。正式发布应在干净提交上构建；本地未提交修改不会自动改变版本字符串。覆盖 VERSION 仅用于开发标记。发布包按白名单复制文件，既不包含私有配置和凭证，也不包含行情和日志。

GitHub Actions 在 master/main 推送、PR 或手工触发时执行 `scripts/ci.sh` 和容器冒烟，保留 Linux amd64 发布包 7 天；不发布镜像、不部署、不发送飞书消息。流程使用 [GitHub 官方 Go Actions 模式](https://docs.github.com/en/actions/tutorials/build-and-test-code/go)。

`make clean` 只删除 bin/ 和 .builds/；不会删除行情、私有配置或日志。

## 框架与界面验证

GitHub Actions 在原生、镜像冒烟后分别执行浏览器门禁。测试依赖为 Python 3.12、`playwright==1.61.0` 及该版本 Chromium，不进入生产镜像：

```bash
python3 -m pip install playwright==1.61.0
python3 -m playwright install --with-deps chromium
make dashboard-smoke
make docker-dashboard-smoke
```

浏览器断言包含四个看板页面（`/`、`/stocks`、`/chart`、`/rankings`）的独立加载、当前导航项、深链接与跨页跳转，以及 DOM、URL、标题、静态资源、只读 HTTP 方法、网关拒绝、请求失败／超时和恢复；新增 `/api/status` 的业务状态也必须在摘要页出现。没有内置账号角色，因此不虚构普通用户／管理员身份。`make ci` 本身仍是无浏览器的基础验证，完整 CI 由 Actions 追加上述步骤。

框架回归位于 `internal/monitor/pipeline_test.go`、`internal/storage/reliability_test.go`、`internal/health/health_test.go`：模拟采集与分析失败、断点恢复、同日修订重放、旧库迁移、长期分钟查询、交易日新鲜度。使用临时数据库和模拟通道，不发送真实消息。
