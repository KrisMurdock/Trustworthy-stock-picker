# 部署、升级与排障

原生/systemd 与 Compose 是两种运行方式，同一数据目录同时只允许一个写入进程。部署前停止旧写入实例；`check`、`status` 可以在服务运行时读取状态。

## 共同准备

1. 保持 Windows 在线及 SSH 反向隧道可用，由宿主机把共享挂载至 `/mnt/windows_database`。确认 `findmnt -T /mnt/windows_database -o TARGET,FSTYPE` 显示 cifs。容器不会建立隧道或挂载 SMB。
2. 从 `config.yaml.example` 创建私有配置，保留 `require_cifs: true`，核对股池、策略与有效交易日历。根配置默认 `data_dir: data`；在源码目录创建指向现有共享的软链接，或通过环境变量指定共享绝对路径。
3. 按[环境变量说明](environment-variables.md)配置通知及滚动日志。目录 `logs/` 在宿主机；行情与状态在 Windows。先关闭通知完成检查，再按实际需要启用通道。
4. 记录提交 SHA、镜像 ID 或包校验和。升级前备份配置与共享盘状态，保留旧制品。

## 原生与 systemd

在源码目录 `make build`，或解压经过校验的原生发布包。使用包部署时，将下面服务模板中的工作目录、程序和配置绝对路径改为实际安装位置。

```bash
./bin/stock-watcher version
./bin/stock-watcher check -config config.yaml
sudo install -m 644 deploy/stock-watcher.service /etc/systemd/system/stock-watcher.service
sudo systemctl daemon-reload
sudo systemctl enable --now stock-watcher.service
sudo systemctl status stock-watcher.service
journalctl -u stock-watcher.service -n 50 --no-pager
```

模板默认目录 `/root/data/codebase/toys_project/stock_watcher`，读取 `/etc/stock-watcher.env`，等待网络与已有 `mnt-windows_database.automount`。SELinux Enforcing 环境需配置适当的程序路径策略；本服务器原生路径已有 `bin_t` 策略，替换二进制后运行 `sudo restorecon -v /root/data/codebase/toys_project/stock_watcher/bin/stock-watcher` 再重启。

原生发布包没有开发启动脚本，服务直接运行 Go。升级先停止服务，再校验新包、替换二进制和公开材料，保留私有配置、股池修改和数据，恢复文件标签，执行 check 后启动。回滚使用保存的旧制品及兼容配置；如果另一次功能升级改变持久数据版本，须先检查其兼容说明，不能直接降级覆盖账本。

## 容器

完整操作见 [docker/README.md](../docker/README.md)（源码仓库提供）。Compose 将容器 8080 映射到宿主机 127.0.0.1:8080，提供同一 Go 进程内的只读网页。健康检查使用原生 `health` 命令读取存储、日历、任务和账本元数据，不写账本、不扫描全库，也不调用远端行情或飞书。业务降级在输出的 `business/issues` 中呈现，存储或日历不可用才使探针失败。每分钟检查一次，三次连续失败标记 unhealthy；Docker 不会仅因 unhealthy 自动重启容器，需查看原因，恢复共享后复查。

默认 CPU 1 核、内存 256 MiB、PID 64，根文件系统只读，删除全部 Linux capabilities；仅 /data 和 /logs 可写。当前共享使用 root UID 权限，所以容器默认 root，不能随意切换用户；改变用户前需要同步调整共享及配置权限并验证。生产额外需要允许访问行情和飞书的出站网络。

## 部署后验收与排障

- `version` 与预期提交一致；`check` 返回 `status: ready`；`status` 能读取账本；检查启动日志及退出码。Compose 使用 `ps` 核对 healthy，systemd 使用 `is-active`。
- 报“CIFS”或路径不存在：检查 Windows/隧道/宿主机挂载。共享重新挂载后，容器可能仍持有旧 bind mount，应重建容器。不要关闭 CIFS 校验或创建本地 data 目录绕过故障。
- 报权限不足：确认运行 UID、私有配置可读及共享可写。SELinux 拒绝时检查宿主机审计日志与专用策略，不要对整个 Windows 盘递归重标记。
- 报缺少 webhook：检查启用通道引用的变量名和环境文件是否生效，只检查是否设置，不输出值。检查通知权限前先使用关闭通知的配置；真实发送需单独确认。
- 报日历越界：更新官方交易日历。当前公开示例只覆盖 2026 年；测试用宽范围日历不得用于生产。
- 冷启动大量研究数据时关注内存及退出码 137，按实际需求调整 Compose 限额；不能把进程存在等同于任务成功。

项目提供 `/`、`/calculator`、`/experiments` 页面，默认原生 URL 为 `http://127.0.0.1:8080/`。没有内置账号／管理员角色；远程访问须经受控网络或已有认证代理。部署验收应在实际访问 URL 验证 DOM、静态资源版本及 API，并检查认证代理的拒绝分支；不能仅凭接口 200 判断界面正常。行情同步和真实飞书投递另做有明确目标的业务验收。密码登录的 Windows 隧道仍需终端保持在线，无人值守重连需要专用密钥另行配置。

## 框架升级与恢复

本版将行情库从 schema 2/3 升至 4；写入入口在持有存储锁后迁移，旧库的只读入口会要求先迁移。迁移保留原表，新增压缩的不可变日线窗口、报告输入／修订、任务与检查点。升级时先停止所有写入实例，备份共享目录，再用新二进制执行 `init`；它会迁移已有数据库，不会重建或清空。迁移完成后旧二进制不兼容 schema 4，回滚须恢复升级前完整备份。

`daily_bar` 是可更新视图；新报告的 `input_id` 与 `revision` 引用不可变输入及报告。旧数据只能在原摘要仍匹配时重建，修订后的旧窗口会明确报不可复现，不能据此回填虚构旧价格。精确重放使用报告记录的同一构建制品：

```bash
./bin/stock-watcher replay -config config.yaml -revision <报告中的revision>
./bin/stock-watcher health -config config.yaml
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
curl --fail http://127.0.0.1:8080/api/status
```

`/healthz` 仅证明 HTTP 进程存活；`/readyz` 验证本地依赖，返回 `business: degraded` 时仍需查看 `issues`。`/api/status` 即使依赖故障仍返回诊断 JSON（`ready: false`），页面明确显示不可用；正常时它与页面显示日线应有日期、覆盖、分析发布、任务阶段／心跳、分钟同步、消息积压。日线新鲜度按交易日和配置的盘后调度计算，正常周末与节假日不要求产生新数据。健康检查不会发送通知；存储探针或数据库文件检查使用 `check`，高频检查使用 `health`。

整池采集时，每只股票只有完整通过校验后才进入检查点；尚未完成的批次不能发布推荐。重启后同日期、同参数任务复用已完成股票，覆盖失败会重新尝试隔离股票。行情发布和分析分离，分析失败可基于已采集窗口重试。成功任务之后的新启动仍刷新全池。默认整轮期限 90 分钟、分析期限 10 分钟，每 30 秒更新心跳；算法须遵守 context 取消，框架不在进程内强杀任意 Go 函数。

备份必须覆盖 `market.db`、实验数据库、`state/`、上市信息缓存及私有配置，保留权限；备份也应放在 Windows 或指定备份盘，不复制到服务器行情目录。安全的最小流程是停止所有 systemd／容器／CLI 写入进程，确认数据库连接关闭，再在 Windows 对共享目录完整复制，记录构建 SHA、库版本和校验和。不要在线仅复制一个 SQLite 主文件或删除 journal 文件。恢复先到隔离目录，在通知关闭、独占锁、匹配构建的环境执行 `init/check/health`，抽查 `replay` 与 K 线，再停服务替换生产目录并恢复挂载。Windows 断线／重挂载和真实备份恢复需在部署环境演练；本地 mock 不能证明网络文件系统锁和 fsync 可靠。

不可变窗口会增加磁盘占用；目前不自动删除历史修订，运维需监测容量并制定归档策略。不要在 CIFS 上开启 WAL，也不要在共享故障时退回服务器本地数据目录。
