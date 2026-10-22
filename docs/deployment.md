# 部署、升级与排障

原生/systemd 与 Compose 是两种运行方式，同一数据目录同时只允许一个写入进程。部署前停止旧写入实例；`check`、`status` 可以在服务运行时读取状态。

## 共同准备

1. 默认使用本地存储，无需 Windows 或 SSH 隧道。使用 Windows/CIFS 共享盘时，保持 Windows 在线及 SSH 反向隧道可用，由宿主机把共享挂载至 `/mnt/windows_database`，并确认 `findmnt -T /mnt/windows_database -o TARGET,FSTYPE` 显示 cifs。容器不会建立隧道或挂载 SMB。
2. 从 `.env.example` 复制 `.env`，按[环境变量说明](environment-variables.md)配置数据目录、通知、行情源和滚动日志。业务配置不再读取 `config.yaml`；观测股池继续独立保存在 `leaders.yaml`，`leaders.pending.yaml` 只是待确认名单。
3. 本地模式使用默认 `data/`，显式 `init` 会自动创建；共享盘模式通过 `STOCK_WATCHER_DATA_DIR` 指向挂载目录，并设置 `STOCK_WATCHER_REQUIRE_CIFS=true`。目录 `logs/` 保持在宿主机。先关闭通知完成检查，再按实际需要启用通道。
4. 记录提交 SHA、镜像 ID 或包校验和。升级前备份配置与共享盘状态，保留旧制品。

## 原生与 systemd

在源码目录 `make build`，或解压经过校验的原生发布包。使用包部署时，将下面服务模板中的工作目录和程序绝对路径改为实际安装位置。

```bash
./bin/stock-watcher version
./bin/stock-watcher check
sudo install -m 644 deploy/stock-watcher.service /etc/systemd/system/stock-watcher.service
sudo install -m 644 deploy/stock-watcher-secondstage.service /etc/systemd/system/stock-watcher-secondstage.service
sudo install -m 644 deploy/stock-watcher-secondstage.timer /etc/systemd/system/stock-watcher-secondstage.timer
sudo systemctl daemon-reload
sudo systemctl enable --now stock-watcher.service
sudo systemctl enable --now stock-watcher-secondstage.timer
sudo systemctl status stock-watcher.service
sudo systemctl status stock-watcher-secondstage.timer
journalctl -u stock-watcher.service -n 50 --no-pager
```

模板默认目录 `/root/data/codebase/toys_project/stock_watcher`，读取 `/etc/stock-watcher.env`，等待网络与已有 `mnt-windows_database.automount`。主服务在 15:00 收盘后同步行情；精排服务由 timer 在 15:00、15:05、15:10、15:15、15:20 触发，等待当日冻结初筛报告并生成四模型 artifact。到 `15:28` 仍未齐时 Go 进程会按中性分降级发布，不阻塞主报告。SELinux Enforcing 环境需配置适当的程序路径策略；本服务器原生路径已有 `bin_t` 策略，替换二进制后运行 `sudo restorecon -v /root/data/codebase/toys_project/stock_watcher/bin/stock-watcher` 再重启。

精排线上验收至少检查：`systemctl list-timers stock-watcher-secondstage.timer`、当日 `state/secondstage/<model>/<as_of>.json` 四份文件均存在、`/api/reports?as_of=<date>` 中 `second_stage.degraded=false`、精排稿只在初筛/主报告之后发送。若 `journalctl -u stock-watcher-secondstage.service` 显示模型目录、Python 依赖或 CIFS 挂载错误，先修复离线生成链；不要把缺失 artifact 误判为 Go 精排算法失败。

原生发布包没有开发启动脚本，服务直接运行 Go。升级先停止服务，再校验新包、替换二进制和公开材料，保留环境文件、`leaders.yaml` 股池修改和数据，恢复文件标签，执行 check 后启动。回滚使用保存的旧制品及兼容环境；如果另一次功能升级改变持久数据版本，须先检查其兼容说明，不能直接降级覆盖账本。

## 容器

完整操作见 [docker/README.md](../docker/README.md)（源码仓库提供）。Compose 将容器 8080 映射到宿主机 127.0.0.1:8080，提供同一 Go 进程内的只读网页。健康检查使用原生 `health` 命令读取存储、日历、任务和账本元数据，不写账本、不扫描全库，也不调用远端行情或飞书。业务降级在输出的 `business/issues` 中呈现，存储或日历不可用才使探针失败。每分钟检查一次，三次连续失败标记 unhealthy；Docker 不会仅因 unhealthy 自动重启容器，需查看原因，恢复共享后复查。

默认 CPU 1 核、内存 256 MiB、PID 64，根文件系统只读，删除全部 Linux capabilities；仅 /data 和 /logs 可写。当前共享使用 root UID 权限，所以容器默认 root，不能随意切换用户；改变用户前需要同步调整共享及配置权限并验证。生产额外需要允许访问行情和飞书的出站网络。

## 微信公众号草稿（可选）

该能力只创建公众号草稿，不调用发布或群发接口。未认证公众号可以使用 `draft/add`，但 `freepublish/submit` 和 `message/mass/sendall` 要求认证；自动发布和“给所有粉丝群发”不在当前实现范围内。

1. 在公众号后台取得 AppID/AppSecret，设置 `STOCK_WATCHER_WECHAT_ENABLED=true`，并设置 `WECHAT_APP_ID` 和 `WECHAT_APP_SECRET`，或用 `STOCK_WATCHER_WECHAT_CREDENTIALS_FILE` 指向权限为 `600` 的私有 `wechat.config`。环境变量优先于文件；文件不提交、不回显。可选设置 `WECHAT_THUMB_MEDIA_ID` 复用永久封面素材，或设置 `STOCK_WATCHER_WECHAT_AUTHOR`。
2. 把运行主机的公网出口 IP 加入公众号后台的 IP 白名单。容器、systemd 或本地进程的出口 IP 可能不同，必须以实际发起 `api.weixin.qq.com` 请求的主机为准。确认出站网络允许访问该域名。
3. 保持 `STOCK_WATCHER_SECOND_STAGE_ENABLED=true` 和 `STOCK_WATCHER_SECOND_STAGE_PUSH_ENABLED=true`。执行 `./bin/stock-watcher check-config`，再启动 `run`；公开接口或 mock 不能替代真实账号验证。
4. 下一份精排报告生成后，检查公众号草稿箱是否出现标题、摘要、正文和封面完整的草稿。`status` 中 `wechat-draft` 的 `delivered` 仅表示草稿已创建；还要人工确认草稿内容和排版，再从公众号后台决定是否发布或群发。
5. 若日志出现 `wechat_api_40164_ip_not_whitelisted`，更新白名单后执行 `./bin/stock-watcher retry wechat-draft`。若出现 `40164` 以外的权限错误，先核对 AppID/AppSecret、账号接口权限和 `second_stage` 开关；不要用重复启动进程绕过投递账本。

当前仓库的自动化验证覆盖模拟微信接口的成功、鉴权刷新、限流、IP 白名单和输入校验；没有真实 AppID/AppSecret 与实际公众号草稿箱，不能宣称真实部署已验证。首次上线应按上述步骤在真实账号完成一次受控草稿验收。

## 家用机器临时公网入口

家用机器没有公网 IP 或域名时，可使用 Cloudflare Quick Tunnel 临时发布匿名只读网页。该入口默认关闭；先安装 `cloudflared`，确认 `.env` 中的股池和数据目录有效，再显式设置 `STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED=true` 并运行 `make run`。脚本固定本地监听 `127.0.0.1:18080`，强制 `STOCK_WATCHER_WEB_PUBLIC_READ_ONLY=true`、`STOCK_WATCHER_LLM_WRITE_MODE=off`、`STOCK_WATCHER_PREDICTOR_WRITE_MODE=off`、`STOCK_WATCHER_PREDICTOR_PUSH_ENABLED=false`、`STOCK_WATCHER_FEISHU_ENABLED=false` 和 `STOCK_WATCHER_WECHAT_ENABLED=false`，执行 `check-config/init/check` 后启动主程序与隧道；健康检查通过后打印 HTTPS URL，并写入 `.scratch/public-tunnel/url`。未开启该环境变量时，`make run` 只启动本机网页。

该模式不需要路由器端口转发，适合演示和短期分享，但 URL 是临时的：进程停止、机器休眠或隧道重连后可能变化，Cloudflare Quick Tunnel 不提供可用性承诺。公网模式不是认证；它只公开行情和复盘读接口，所有非 GET/HEAD 请求以及 AI 配置、对话、归因和模型测试接口都在应用层返回 404。长期稳定地址需要自有域名与命名隧道，或自有域名 + 认证反向代理。

macOS 可使用 `tmux new-session -d -s stock-public 'STOCK_WATCHER_PUBLIC_TUNNEL_ENABLED=true bash scripts/public-tunnel.sh'` 让服务脱离当前终端；用 `tmux attach -t stock-public` 查看日志，用 `cat .scratch/public-tunnel/url` 读取当前地址。机器重启后要重新执行启动命令，临时地址也会变化。

## 部署后验收与排障

- `version` 与预期提交一致；`check` 返回 `status: ready`；`status` 能读取账本；检查启动日志及退出码。Compose 使用 `ps` 核对 healthy，systemd 使用 `is-active`。
- 报“CIFS”或路径不存在：共享盘模式检查 Windows/隧道/宿主机挂载；本地模式检查 `STOCK_WATCHER_DATA_DIR` 和目录权限。共享重新挂载后，容器可能仍持有旧 bind mount，应重建容器。不要在共享盘模式关闭 CIFS 校验或创建本地 data 目录绕过故障。
- 报权限不足：确认运行 UID、`.env`/EnvironmentFile 可读及数据目录可写。SELinux 拒绝时检查宿主机审计日志与专用策略，不要对整个 Windows 盘递归重标记。
- 报缺少 webhook：检查 `STOCK_WATCHER_FEISHU_ENABLED` 和 `FEISHU_WEBHOOK` 是否生效，只检查是否设置，不输出值。检查通知权限前先设置 `STOCK_WATCHER_FEISHU_ENABLED=false`；真实发送需单独确认。
- 旧部署显式设置了 `STOCK_WATCHER_FEISHU_CREDENTIALS_FILE` 时，才检查对应凭证文件；新部署应使用环境变量。
- 报日历越界：内置官方交易日历当前覆盖 2026 年，需在年底前升级二进制；测试可用 `STOCK_WATCHER_CALENDAR_*` 覆盖，不得用于生产。
- 冷启动大量研究数据时关注内存及退出码 137，按实际需求调整 Compose 限额；不能把进程存在等同于任务成功。

项目提供 `/`、`/stocks`、`/chart`、`/rankings`、`/day-range`、`/review`、`/experiments` 和 `/calculator` 页面，默认原生 URL 为 `http://127.0.0.1:8080/`。`/day-range` 展示盘前全天高低价预测、区间、模型版本和收盘核对状态；第一版明确标注盘中剩余时段模型尚未实现。`/review` 提供冻结推荐回看、T+1/T+3/T+5 检查点、证据详情和手动 AI 归因；问答与归因不会自动触发。没有内置账号／管理员角色；远程访问须经受控网络或已有认证代理，AI 配置写入模式不能代替认证。部署验收应在实际访问 URL 验证 DOM、静态资源版本及 API，并检查认证代理的拒绝分支；不能仅凭接口 200 判断界面正常。行情同步和真实飞书投递另做有明确目标的业务验收。密码登录的 Windows 隧道仍需终端保持在线，无人值守重连需要专用密钥另行配置。

## 框架升级与恢复

本版将行情库从 schema 2/3/4/5 升至 6；写入入口在持有存储锁后迁移，旧库的只读入口会要求先迁移。迁移保留原表，新增压缩的不可变日线窗口、报告输入／修订、任务与检查点，以及逐日停牌状态、状态覆盖和交易所日历表；schema 6 为 `report_archive` 增加 `as_of`、`generated_at`、`schema_version` 和 `(as_of, generated_at)` 索引，并从不可变报告载荷回填。升级时先停止所有写入实例，备份共享目录，再用新二进制执行 `init`；它会迁移已有数据库，不会重建或清空。迁移完成后旧二进制不兼容 schema 6，回滚须恢复升级前完整备份。升级后先执行一次 `status-backfill`，再用 `status-audit` 确认覆盖，之后才启动 `run`。复盘页读取现有 `selection_run` 与 `report_archive`，不新增复盘数据表；旧报告缺少冻结输入或完整执行参数时会明确标记为不支持完整复盘。

启用 `STOCK_WATCHER_DAYRANGE_ENABLED=true` 时，`init` 还会创建或校验独立的 `dayrange.db` schema 1；它与 `market.db` schema 6 分开，不影响行情库迁移。首次启用可先执行 `train-dayrange -offline` 做不写生产库的数据审计与训练检查，再停止 `run` 执行一次 `train-dayrange`、盘前 `predict-dayrange` 和收盘后 `reconcile-dayrange`。模型 checksum 错误会被标记为 `invalid` 并回退已验证基线；阶段六盘中剩余时段模型不在此版本中。

盘前分页卡片只使用 `predictor_dayrange` 路由，账本保存在 `<data_dir>/state/dayrange-delivery.json`。生产应配置 `STOCK_WATCHER_DAYRANGE_PUSH_ENABLED=true` 和对应命名通道；验证时检查卡片中的目标日、数据截止、生成时点、覆盖数、跳过原因与“不构成投资建议”声明。重复执行 `predict-dayrange` 或重启 `run` 不应重复发送同一目标日、同一页；09:30 后账本中的未发送卡片会过期。关闭该开关后预测仍会保存，但不会创建新的分页投递事件。

盘前执行确认卡使用独立的 `premarket_execution` 路由和 `<data_dir>/state/premarket-execution-delivery.json` 账本，只消费精排 Top N 与同日盘前预测的冻结结果。生产应新增 `premarket-execution` 命名通道并设置其 webhook 环境变量；验证时检查精排排名、参考价、预测高/低、区间、`可执行／观察／放弃` 状态、降级说明和“不构成投资建议”声明。该卡按目标日稳定去重，09:30 后过期，与 `predictor_dayrange`、`second_stage`、`selection` 互不阻塞；结构影子期或筛选未启用时不得把状态表述为交易指令。

AI 配置与行情数据分离：原生部署默认写入工作目录的 `llm.config`，Compose 使用独立命名卷挂载到 `/config/llm.config`。该文件权限应为 `0600`，不属于 CIFS 数据目录，也不能被 Git 跟踪。容器根文件系统保持只读；若网页端需要保存 AI 配置，必须保留 `/config` 可写卷，并按需设置 `STOCK_WATCHER_LLM_WRITE_MODE=all`（远程访问还需认证反向代理）。未配置模型时复盘页仍可查看确定性结果，只禁用流式回答。

`daily_bar` 是可更新视图；新报告的 `input_id` 与 `revision` 引用不可变输入及报告。旧数据只能在原摘要仍匹配时重建，修订后的旧窗口会明确报不可复现，不能据此回填虚构旧价格。精确重放使用报告记录的同一构建制品：

```bash
./bin/stock-watcher replay -revision <报告中的revision>
./bin/stock-watcher health
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8080/api/status
```

`/healthz` 仅证明 HTTP 进程存活；`/readyz` 验证本地依赖，返回 `business: degraded` 时仍需查看 `issues`。`/api/status` 即使依赖故障仍返回诊断 JSON（`ready: false`），页面明确显示不可用；正常时它与页面显示日线应有日期、覆盖、分析发布、任务阶段／心跳、分钟同步、消息积压。日线新鲜度按交易日和内置盘后调度计算，正常周末与节假日不要求产生新数据。健康检查不会发送通知；存储探针或数据库文件检查使用 `check`，高频检查使用 `health`。

整池采集时，每只股票只有完整通过校验后才进入检查点；尚未完成的批次不能发布推荐。重启后同日期、同参数任务复用已完成股票，覆盖失败会重新尝试隔离股票。行情发布和分析分离，分析失败可基于已采集窗口重试。成功任务之后的新启动仍刷新全池。默认整轮期限 90 分钟、分析期限 10 分钟，每 30 秒更新心跳；算法须遵守 context 取消，框架不在进程内强杀任意 Go 函数。

备份必须覆盖 `market.db`、`context.db`、`experiments.db`、`calendar.db`、`dayrange.db`（启用时）、`predictions.db`、`models/intraday/`、`state/`、上市信息缓存及私有配置，保留权限；本地模式备份到指定备份盘，共享盘模式在 Windows 端完整复制，不复制到服务器行情目录。安全的最小流程是停止所有 systemd／容器／CLI 写入进程，确认数据库连接关闭，再完整复制数据目录，记录构建 SHA、库版本和校验和。不要在线仅复制一个 SQLite 主文件或删除 journal 文件。恢复先到隔离目录，在通知关闭、独占锁、匹配构建的环境执行 `init/check/health`，抽查 `replay` 与 K 线，再走 `predictor-status`、`status-dayrange` 和 `replay-dayrange` 及一次只读网页检查，再停服务替换生产目录并恢复挂载。Windows 断线／重挂载和真实备份恢复需在部署环境演练；本地 mock 不能证明网络文件系统锁和 fsync 可靠。

不可变窗口会增加磁盘占用；目前不自动删除历史修订，运维需监测容量并制定归档策略。不要在 CIFS 上开启 WAL，也不要在共享故障时退回服务器本地数据目录。
