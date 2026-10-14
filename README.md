# stock_watcher

原生 Go 的 A 股龙头股池日线监控。服务器下载历史日线，直接写入 Windows 共享盘；收盘后筛选下一交易日 T+1 的候选股，并通过飞书自定义机器人推送。启动、下载、筛选和推送均由 Go 程序执行。

## 运行

需要 Linux、Go 1.26 和已经挂载的 CIFS 共享。服务器上的 `data` 链接到 `/mnt/windows_database`，对应 Windows `D:\shared_database`。默认拒绝本地磁盘回退，Windows 关机或 SSH 隧道断开时不会改写服务器磁盘。

```bash
make build
cp config.yaml.example config.yaml
chmod 600 config.yaml
./bin/stock-watcher check -config config.yaml
./bin/stock-watcher sync -config config.yaml
./bin/stock-watcher preview -config config.yaml -as-of 2026-10-13
./bin/stock-watcher run -config config.yaml
```

`sync` 下载并生成报告，不发送消息。`run` 在交易日 18:05（北京时间）生成 T+1 报告；启动晚于计划时间时，允许在下一交易日开盘前补发。`once` 执行一次同样的调度流程。`status` 查看持久投递状态，`retry` 在停止服务并修正权限、签名等永久错误后解除待发送记录的阻塞。改变配置或股池后重启生效，同一数据日不会重复生成推送；下一数据日使用新配置。

`leaders.yaml` 保存已核对的 104 个证券代码和名称，可直接添加或删除股票。用户原名单另外 6 个疑似笔误在 `leaders.pending.yaml`，确认前不参与下载与筛选。上海和深圳 A 股支持带市场前缀的代码；名称不会参与自动猜测。维护者应使用已核对的证券代码。`cmd/resolve-pool` 可辅助核对名称，结果须人工检查。

## 首版筛选条件

条件全部满足后，按现有技术趋势评分降序取前 5 名，分数相同时按证券代码排序：

- 收盘价 > MA20，且 MA5 > MA10 > MA20。
- 今日成交量 / 前 5 个交易日平均成交量 ≥ 1.2，且 MACD DIF > DEA。
- RSI14 在 50～80，单日涨跌幅在 -3%～7%，总分至少 70。
- 排除 ST、退市标记、无成交量、当日数据未更新及不足 35 根日线的股票。

评分延续旧代码的均线 30 分、MACD 25 分、RSI 20 分、连涨 15 分、量比 10 分。修正旧 RSI 最后一根的平滑计算；量比使用同一返回序列的成交量原值，不把成交额当成交量。MA、MACD、RSI 使用同一窗口的前复权价格。默认重新下载最近 365 个自然日的完整窗口，避免把不同日期复权基准的数据拼在一起。阈值、Top N、历史范围和时间均可配置。这是透明的技术规则筛选，尚未经过收益回测。

没有股票通过时，报告和飞书明确显示“没有满足筛选规则的股票”。过期、停牌和数据不足单独列出；任何股票的网络请求或数据格式错误会阻止整池报告推送，重试成功后才生成完整报告。

## Windows 文件

```text
data/                       # 实际位于 Windows 共享盘
  daily/2026-10-13/sh600000.json
  reports/2026-10-13.json
  state/delivery.json
  .stock-watcher.lock
logs/stock-watcher.log       # 服务器上的有限运行日志
```

持久数据使用 `schema_version: 1`、UTF-8 JSON、ISO 日期和带时区 RFC3339Nano 时间。价格为人民币，`volume_raw` 为供应商原值，单位标记为 `provider_raw`，`data_field` 记录 `qfqday` / `hfqday` / `day`；缺字段、非有限数、版本错误和损坏状态都会报错，不自动清空状态。日线文件包含来源、复权方式、请求截止日和实际最后一根日线日期；可在 Windows 用编辑器直接读取。

单个进程持有共享文件锁。JSON 文件先写临时文件、fsync，再原子重命名；共享目录内的软链接被拒绝。`require_cifs: false` 仅适合本地测试，生产应保持 `true`。共享盘故障时停止本轮写入；已保存的数据和待发送记录保留。日志和程序本身占用少量服务器磁盘，行情不会保存第二份服务器副本。

## 飞书配置

在群里添加自定义机器人，用受限权限配置文件或 `/etc/stock-watcher.env` 保存凭证，不要提交到 Git：

```text
FEISHU_WEBHOOK=https://open.feishu.cn/open-apis/bot/v2/hook/...
FEISHU_SECRET=...
```

在 `config.yaml` 将 `feishu.enabled` 设为 `true`。开启签名时，按[飞书官方签名规则](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)计算 HMAC-SHA256 和 Base64。只有 HTTP 200 且业务码为 0 才确认送达。429、5xx、限流和网络错误持久重试；401/403、签名或关键词错误阻塞待发送记录，需要修正配置并运行 `retry`。日志不输出 webhook、secret 或原始响应。

推送是至少一次投递：请求成功后、写入确认前异常退出可能重复。卡片包含固定事件 ID。过了目标交易日 09:30 的消息不再发送。没有设置有效 webhook 时 `sync` 和报告预览仍可独立使用。

## 交易日历与数据源

2026 日历来自[深交所休市通知](https://www.szse.cn/disclosure/notice/general/t20251222_618087.html)。T+1 跳过周末和配置中的节假日；日历覆盖到期会报错，应提前按新年度通知更新。未收盘日期不会用于筛选。

日线直接请求腾讯行情 HTTP 接口，沿用旧项目的数据协议。第三方免费行情接口可能限流或改变协议；HTTP 超时默认 10 秒，每只股票请求之间至少 350 毫秒，错误可见且不会生成假数据。运行配置仅包含股池、日线、筛选、飞书和交易日历，修改后需重启生效；不兼容的配置字段会被明确拒绝。

## 部署与验证

```bash
make test
make vet
make build
make image
```

容器使用同一个原生 CLI、配置和 CIFS 检查；应先由宿主机挂载 Windows，再映射共享盘：

```bash
docker run --rm --cpus=1 --memory=256m \
  --mount type=bind,src=/mnt/windows_database,dst=/data \
  --mount type=bind,src="$PWD/config.yaml",dst=/app/config.yaml,readonly \
  --mount type=bind,src="$PWD/logs",dst=/logs \
  --env-file /etc/stock-watcher.env \
  stock-watcher:<commit-sha> run -config /app/config.yaml
```

宿主机直接使用 `bin/stock-watcher` 或 systemd 运行，服务模板中的工作目录为 `/root/data/codebase/toys_project/stock_watcher`。确认私有配置与 Windows 挂载可用后安装：

```bash
sudo install -m 644 deploy/stock-watcher.service /etc/systemd/system/stock-watcher.service
sudo systemctl daemon-reload
sudo systemctl enable --now stock-watcher.service
sudo systemctl status stock-watcher.service
```

SELinux Enforcing 环境需为程序配置 `bin_t` 文件路径策略。本服务器已经配置该策略；每次重新构建后、启动或重启服务前恢复标签：

```bash
sudo restorecon -v /root/data/codebase/toys_project/stock_watcher/bin/stock-watcher
sudo systemctl restart stock-watcher.service
```

`./bin/stock-watcher version` 返回构建对应的提交 SHA。服务安装不会自动配置 Windows 开机 SSH 隧道；目前密码登录隧道仍需要保持终端开启，无人值守重连需另行配置专用 SSH 密钥。

日志 stdout 和 `logs/stock-watcher.log` 格式一致：`[带时区时间] [LEVEL] [文件:行号] 英文消息 {JSON 属性}`。环境变量 `STOCK_WATCHER_LOG_MAX_MB`（10）、`STOCK_WATCHER_LOG_MAX_DAYS`（7）、`STOCK_WATCHER_LOG_BACKUPS`（10）控制大小、天数、份数轮转及 gzip 压缩；`STOCK_WATCHER_LOG_JSON` 开启整条 JSON，`STOCK_WATCHER_LOG_COLOR` 支持 auto/force/off，文件与 JSON 永无颜色。
