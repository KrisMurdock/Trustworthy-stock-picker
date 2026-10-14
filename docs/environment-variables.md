# 配置与环境变量

业务参数仍在 `config.yaml`：股池、交易日历、日线、策略、通知通道。配置字段参见根目录 `config.yaml.example`，未知字段会报错。环境变量仅覆盖下表对应设置。

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| STOCK_WATCHER_DATA_DIR | 使用 YAML data_dir | 行情与状态目录；环境变量覆盖 YAML。生产必须指向已挂载 CIFS |
| STOCK_WATCHER_WEB_ADDR | YAML web.listen，默认 127.0.0.1:8080 | `run` 的只读网页监听地址；IP 或 localhost 加端口。容器默认 0.0.0.0:8080 |
| FEISHU_WEBHOOK | 凭证文件或旧 feishu.webhook 配置值 | 默认飞书通道 webhook；显式空值也会覆盖文件或旧 YAML 值 |
| FEISHU_SECRET | 凭证文件或旧 feishu.secret 配置值 | 机器人签名密钥；未启用签名时可省略 |
| STOCK_WATCHER_LOG_DIR | logs | 相对路径按配置文件所在目录解析，生产有限本地日志 |
| STOCK_WATCHER_LOG_MAX_MB | 10 | 单文件大小上限 MB，整数 1～1000 |
| STOCK_WATCHER_LOG_MAX_DAYS | 7 | 轮转日志保留天数，整数 1～1000 |
| STOCK_WATCHER_LOG_BACKUPS | 10 | 轮转文件保留份数，整数 1～1000，gzip 压缩 |
| STOCK_WATCHER_LOG_JSON | false | true 输出结构化日志，false 输出可读文本 |
| STOCK_WATCHER_LOG_COLOR | auto | auto / force / off，仅控制 stdout；文件和 JSON 无颜色 |

多飞书通道可在 `notify.channels[].webhook_env` / `secret_env` 指定不同变量名。启用通道必须配置有效 webhook；示例所有通道默认关闭。不要把凭证写进命令行、提交、构建参数或报告。

可在 `notify.channels[]` 或旧 `feishu` 节点声明 `credentials_file: feishu.config`，文件内容只接受 `webhook: ...` 和可选 `secret: ...`。Go 按配置文件目录解析相对路径，所有命令共用此加载规则；指定文件缺失、无读取权限或格式错误时启动失败，错误不会回显内容。优先级为环境变量 > 凭证文件 > 旧 YAML 内联值；文件中未配置 secret 就不继承旧内联 secret。凭证文件本身不切换 `enabled`。容器须额外只读挂载该文件，默认 Compose 仍使用环境变量。

## 三种加载方式

- 本地开发：复制 `.env.example` 为根 `.env`，`make run` 通过 Bash 加载后前台运行 Go。如果同仓库同配置的 systemd 实例已运行，则先构建并校验配置，再停止后台服务并切换前台。前台不会自动读取 systemd 的 EnvironmentFile，所需凭证应在当前 shell 或开发 `.env` 中配置。该文件须是可信的 Shell 赋值；含特殊字符的值使用单引号。`make check/status` 和直接运行二进制不自动读取 `.env`，可在可信 shell 中先执行 `set -a; source .env; set +a`。
- systemd：模板读取 `/etc/stock-watcher.env`。使用 `KEY=value`，不加 `export`，参考 systemd EnvironmentFile 语法；Go 程序本身不读取 dotenv。
- Compose：`docker/.env` 只提供部署路径和版本；`STOCK_WATCHER_RUNTIME_ENV` 指向独立运行变量文件，Compose 用 `format: raw` 传入值。这里用不带外层引号的 `KEY=value`，`$` 等字符原样传入。不要把带 Shell 引号的开发 `.env` 直接复制过来。

私有文件设为 `chmod 600`。关闭通知的部署也需创建空的运行变量文件，避免误用其他服务的凭证。

## Compose 插值变量

| 变量 | 示例/默认 | 含义 |
| --- | --- | --- |
| STOCK_WATCHER_IMAGE | stock-watcher:local | 构建、启动所用镜像；发布使用明确提交 SHA 标签 |
| STOCK_WATCHER_VERSION | development | 写入二进制与镜像 revision 的版本；发布设为完整提交 SHA |
| STOCK_WATCHER_WEB_BIND | 127.0.0.1:8080 | Compose 网页的宿主机绑定地址与端口，容器内固定 8080 |
| STOCK_WATCHER_HOST_DATA | 必填 | 已挂载 Windows 共享的宿主机绝对路径 |
| STOCK_WATCHER_HOST_CONFIG | 必填 | 私有 YAML 配置文件绝对路径 |
| STOCK_WATCHER_HOST_POOL | 必填 | leaders.yaml 绝对路径 |
| STOCK_WATCHER_HOST_LOGS | 必填 | 已创建的本地日志目录绝对路径 |
| STOCK_WATCHER_RUNTIME_ENV | 必填 | 独立运行变量文件绝对路径 |

Compose 固定容器内数据 `/data`、配置 `/app/config.yaml`、股池 `/app/leaders.yaml`、日志 `/logs`，并关闭颜色；私有 YAML 的 `pool_file` 应为 `leaders.yaml`。Compose 的 environment 覆盖运行变量文件中的 DATA_DIR、LOG_DIR、LOG_COLOR。

`run` 同时启动监控任务和网页，默认只允许本机访问。配置 `web.enabled: false` 可以关闭网页；网页没有账号或写入接口，需要远程访问时通过受控网络或已有认证反向代理开放。Compose 固定容器监听 `0.0.0.0:8080`，并用 `STOCK_WATCHER_WEB_BIND` 控制宿主机入口。应用使用根路径 `/`，反向代理应保留 `/api/` 和 `/assets/` 路径；不支持直接挂到子路径前缀。

路径不自动创建，配置和股池只读挂载。容器根文件系统只读，不需要 SMB 凭证或挂载权限。CIFS 由宿主机负责，`require_cifs: true` 保持启用。不要运行会展开凭证的 `docker compose config` 普通输出；用 `config --quiet` 检查。

Compose 长挂载与 raw 环境文件语义参见 [Docker 官方服务配置](https://docs.docker.com/reference/compose-file/services/)。本配置要求 Docker Compose 2.30.0 或更高版本。
