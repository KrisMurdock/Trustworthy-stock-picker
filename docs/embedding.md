# 作为 Go 库嵌入

`github.com/KrisMurdock/Trustworthy-stock-picker/runtime` 把生产启动流程封装为可复用 Go API。宿主进程负责命令行参数、日志输出目标和信号处理，运行时负责数据目录写锁、SQLite、行情源、监控循环、通知通道及只读看板。

## 最小流程

```go
cfg, err := stockwatcher.LoadConfig()
if err != nil {
	return err
}

if _, err := stockwatcher.Init(&cfg); err != nil {
	return err
}

rt, err := stockwatcher.New(stockwatcher.Options{
	Version: "host-app",
	Config:  &cfg,
	Logger:  logger,
})
if err != nil {
	return err
}
defer rt.Close()

return rt.Run(ctx)
```

`Init` 创建 `<data_dir>/market.db`，可在新目录首次启动时调用。本地模式下会创建缺失的数据目录；CIFS 模式仍要求共享盘已挂载且不会创建本地替代目录。`New` 打开存储与行情源但不会访问远端，也不会开始同步；`Run` 才开始启动同步、调度和分钟采集。

## 配置

- `LoadConfig()` 加载内置配置，并应用 `STOCK_WATCHER_*`、`FEISHU_*`、`TUSHARE_TOKEN` 等环境变量、股池文件和私有凭证。
- `DefaultConfig()` 返回尚未解析路径、股池和凭证的内置配置，适合完全以代码构造配置。
- `Config` 是结构化配置类型，可直接修改 `DataDir`、`Stocks`、`Web.Enabled`、`Intraday.Enabled` 和通知开关。
- `New` 会重新校验传入配置。相对数据目录按宿主进程当前工作目录解析；跨项目部署建议始终使用绝对路径。

## 生命周期

- `New` 获取数据目录写锁。同一数据目录不能同时存在两个 `Runtime` 或 `stock-watcher run` 实例。
- `Run(ctx)` 阻塞运行。父 `context` 取消时完成监听关闭、监控 join 和资源清理等待，并返回 `nil`。
- 同时调用两次 `Run` 返回 `ErrAlreadyRunning`；`Close` 后调用返回 `ErrClosed`。
- `Close` 会取消正在运行的 `Run`、等待全部循环退出并释放数据库与 HTTP 连接，可重复调用。它不会关闭宿主传入的 `Logger`。
- 宿主应先调用 `Close`，再退出进程。`Run` 返回后仍需显式调用 `Close`。

## 看板与日志

- `DashboardHandler()` 始终返回只读 HTTP handler。将 `Web.Enabled` 设为 `false` 后，可把它挂载到任意宿主的 `http.Server`、路由前缀或认证中间件之后。
- 内置网页只处理 GET/HEAD，并要求代理原样保留 `/api/`、`/assets/` 与 `/calculator` 路径。
- `Options.Logger` 为 `nil` 时丢弃运行时日志且不创建 `logs/`。需要统一日志时使用 `NewLogger`；生命周期由宿主负责。
- 凭证会注册到日志脱敏器；不要在宿主日志中输出 `Config` 的 webhook、secret 或 token 字段。

## 初始化与退出示例

```go
func startStockWatcher(ctx context.Context) error {
	cfg, err := stockwatcher.LoadConfig()
	if err != nil {
		return err
	}
	cfg.DataDir = "/srv/stock-watcher/data"
	cfg.Web.Enabled = false

	logger, err := stockwatcher.NewLogger(stockwatcher.LoggerOptions{
		Dir: "/var/log/host-app", MaxBytes: 10 << 20, MaxDays: 7, Backups: 10, Color: "off",
	})
	if err != nil {
		return err
	}
	defer logger.Close()

	rt, err := stockwatcher.New(stockwatcher.Options{Version: "host-app", Config: &cfg, Logger: logger})
	if err != nil {
		return err
	}
	defer rt.Close()
	return rt.Run(ctx)
}
```

生产部署使用 CIFS 模式时仍须先确认共享盘已挂载且 `market.db` 已初始化。运行时不会因共享盘不可用而回退到本地目录，也不会代替宿主实现认证、限流或公网 TLS。
