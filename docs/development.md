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

首次部署完成 `sync` 后，用 `make minute-backfill`（默认 6 并发、最近 200 个交易日）补齐历史 5 分钟线；可用 `MINUTE_ARGS` 覆盖 `-from`、`-as-of`、`-codes`、`-workers`、`-provider`。`-provider auto` 对最近 104 个完整交易日优先使用新浪，更长窗口使用 BaoStock。已有生产实例运行时不要直接写生产库，先按 [行情来源与回退](data-sources.md#分钟数据) 的 staging 流程抓取，再停止 `run` 执行 `-import-from`。

业务配置和凭证全部来自 `.env`，启动不读取 `config.yaml`，默认也不读取 `feishu.config`；观测股池仍独立保存在 `leaders.yaml`。默认使用本地 `data/`，`init` 会自动创建；使用 Windows 挂载盘时设置 `STOCK_WATCHER_REQUIRE_CIFS=true` 和对应 `STOCK_WATCHER_DATA_DIR`。`make run` 用于前台运行，会构建并加载开发 `.env`。若 `stock-watcher.service` 已运行同一个仓库二进制，则先用原生 `check-config` 校验配置与日志环境变量，通过后正常停止后台服务，确认 MainPID 为 0，再进入前台运行。构建或配置校验失败时不停止原服务；停止失败或超时时中止前台启动并报错。

Go `run` 启动后立即同步最近已收盘交易日的完整股池，并并发同步核心 A 股指数与全量行业/概念板块，生成分析并投递尚未过期的 T+1 消息，之后进入定时轮询；已送达的同日同通道消息不重复发送。盘中不会推送已过买入时点的旧推荐。同步失败会自动重试，原始行情和报告保持在数据目录。`once` 仍仅执行一次受内置 `daily.run_at` 限制的定时流程；手动 `sync` 和 `once` 触发同步时也使用同一套股票与市场环境同步路径。

默认 `daily.run_at=15:00`。收盘后 Go 进程先完成行情同步、第一层初筛和主报告，精排稿可以延后到 `second_stage.publish_by=15:28`。默认四模型 artifact 由 `deploy/stock-watcher-secondstage.timer` 调用 `scripts/secondstage/generate_daily.py` 离线生成；开发验证可用独立目录运行：

```bash
PYTHONPATH=/root/python_env/blend-eval python3.11 scripts/secondstage/generate_daily.py \
  --as-of 2026-10-21 --deadline '' \
  --features-root /tmp/secondstage-smoke --state-root /tmp/secondstage-smoke/state
```

该脚本不属于生产 Go 进程；它只读取冻结报告并把已选模型的 artifact 原子写入 `state/secondstage/<model>/<as_of>.json`。网络不可用时先确认本地模型目录和 `HF_HUB_OFFLINE=1`，不要在生产同步中调用在线 Hugging Face。

可选 DeepSeek 第五路默认关闭。启用时传入私有配置；`llm.config` 已被 Git 忽略，也可以用 `STOCK_WATCHER_DEEPSEEK_CONFIG_FILE` 指定，禁止把密钥写入仓库或日志：

```bash
PYTHONPATH=/root/python_env/blend-eval python3.11 scripts/secondstage/generate_daily.py \
  --as-of 2026-10-21 --deadline '' \
  --features-root /tmp/secondstage-smoke --state-root /tmp/secondstage-smoke/state \
  --deepseek --deepseek-config /root/data/codebase/toys_project/stock_watcher/llm.config
```

五路全部成功后才发布；任一路失败不会留下部分新 artifact。DeepSeek 日报支路固定 `reasoning_effort=none`，避免高推理档耗尽输出预算。两阶段研究可以分别运行：

```bash
python3 scripts/secondstage/deepseek_scores.py \
  --candidates /tmp/secondstage/candidates.json --dataset /tmp/secondstage/dataset.json \
  --stage stage1 --output-dir /tmp/secondstage/scores/deepseek-stage1 \
  --config /root/data/codebase/toys_project/stock_watcher/llm.config --reasoning-effort none \
  --top-out /tmp/secondstage/candidates_deepseek_top95.json

python3 scripts/secondstage/deepseek_scores.py \
  --candidates /tmp/secondstage/candidates_stage2_top95.json --dataset /tmp/secondstage/dataset.json \
  --stage stage2 --output-dir /tmp/secondstage/scores/deepseek-stage2 \
  --config /root/data/codebase/toys_project/stock_watcher/llm.config --reasoning-effort none
```

评估只读取冻结输入和 artifact，不调用模型：

```bash
python3 scripts/secondstage/evaluate_deepseek.py \
  --dataset /tmp/secondstage/dataset.json \
  --stage2-candidates /tmp/secondstage/candidates_stage2_top95.json \
  --deepseek-stage2-dir /tmp/secondstage/scores/deepseek-stage2 \
  --jev-stage2 /tmp/secondstage/jev_stage2.json \
  --tree-model internal/secondstage/model.json \
  --out-dir /tmp/secondstage/evaluation
```

完整两阶段对比还需提供 `--stage1-candidates`、`--deepseek-stage1-dir`、`--deepseek-e2e-candidates`、`--deepseek-e2e-stage2-dir` 和 `--jev-stage1`；只比较 Top95 精排时加 `--stage2-only`。本轮 9 日测试和 2 日敏感性结论、指标边界见 [算法接入契约](algorithm-interface.md#deepseek-离线打分与两阶段对比实验支路)。对应单测可用 `make secondstage-deepseek-test` 或 `python3 -m unittest tests.secondstage_deepseek_scores_test tests.secondstage_evaluate_deepseek_test tests.secondstage_generate_daily_test` 运行。

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

发布目录含二进制、环境变量示例、已确认/待确认股池、收盘后精排脚本、systemd 模板、README 与 docs、`VERSION`、`COMMIT`、`SHA256SUMS`。这是原生运行包；Docker/CI/开发脚本从源码仓库使用。arm64 可以交叉构建，但需在对应机器验收运行。

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

浏览器断言覆盖八个页面（`/`、`/stocks`、`/chart`、`/rankings`、`/day-range`、`/review`、`/experiments`、`/calculator`）的独立加载、当前导航项、深链接与跨页跳转，以及 DOM、URL、标题、静态资源、只读 HTTP 方法、网关拒绝、请求失败／超时和恢复；新增 `/api/status` 的业务状态也必须在摘要页出现。摘要页额外断言环境层挂载、`data-scene-state`、字体加载、正文对比度、资源体积上限、移动端首屏、减少动效和 WebGL 不可用时的海报降级。`/rankings` 还会加载测试夹具中的 5 分钟预测，断言预测表、推送开关和 reload 后的开关状态。`/day-range` 使用盘前 fixture 断言区间、实际核对状态、验证门禁、阶段六未实现提示、400/405、断网保留旧结果和重试恢复。没有内置账号角色，因此不虚构普通用户／管理员身份。`make ci` 本身仍是无浏览器的基础验证，完整 CI 由 Actions 追加上述步骤。

框架回归位于 `internal/monitor/pipeline_test.go`、`internal/storage/reliability_test.go`、`internal/health/health_test.go`：模拟采集与分析失败、断点恢复、同日修订重放、旧库迁移、长期分钟查询、交易日新鲜度。使用临时数据库和模拟通道，不发送真实消息。
