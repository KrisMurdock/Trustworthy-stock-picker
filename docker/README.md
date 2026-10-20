# Compose 部署

从仓库根目录运行，要求 Docker Compose ≥ 2.30.0。唯一镜像定义在根 `Dockerfile`；Compose 默认读取根 `.env`，业务配置全部通过环境变量注入，不再挂载 `config.yaml` 或 `feishu.config`。观测股池仍独立保存在 `leaders.yaml`，以只读方式挂载到容器 `/app/leaders.yaml`。

## 首次准备

```bash
cp -n .env.example .env
chmod 600 .env
mkdir -p data logs
```

编辑根 `.env`：

- `STOCK_WATCHER_HOST_DATA` 指向本地数据目录或已挂载的 CIFS 绝对路径；本地模式保持 `STOCK_WATCHER_REQUIRE_CIFS=false`，共享盘模式设为 `true`。
- `STOCK_WATCHER_HOST_POOL` 指向已确认的 `leaders.yaml` 绝对路径。
- `STOCK_WATCHER_HOST_LOGS` 指向宿主机日志目录。
- 按需设置 `STOCK_WATCHER_IMAGE`、`STOCK_WATCHER_VERSION`、`STOCK_WATCHER_WEB_BIND`、`STOCK_WATCHER_FEISHU_ENABLED` 和 `FEISHU_WEBHOOK`。

所有宿主路径必须已经存在，Compose 不会自动创建。`leaders.pending.yaml` 只是待确认名单，不参与容器启动或选股。

## 构建与检查

```bash
export STOCK_WATCHER_VERSION="$(git rev-parse HEAD)"
export STOCK_WATCHER_IMAGE="stock-watcher:${STOCK_WATCHER_VERSION}"
docker compose --env-file .env -f docker/compose.yaml config --quiet
docker compose --env-file .env -f docker/compose.yaml build
docker compose --env-file .env -f docker/compose.yaml run --rm --no-deps stock-watcher version
docker compose --env-file .env -f docker/compose.yaml run --rm --no-deps stock-watcher check
```

将确定的 IMAGE/VERSION 写入 `.env`，确保新终端使用同一版本。Compose 的命令行环境变量优先于文件；切换版本时同步更新或清除旧变量。不要使用普通 `config` 输出，避免展开运行凭证。

## 启动与查看

若同目录已有 systemd 或手工运行的写入进程，先停止它。systemd 与 Compose 只能选择一种常驻运行方式。

```bash
docker compose --env-file .env -f docker/compose.yaml up -d --no-build
docker compose --env-file .env -f docker/compose.yaml ps
docker compose --env-file .env -f docker/compose.yaml logs --tail 50
docker compose --env-file .env -f docker/compose.yaml exec stock-watcher stock-watcher status
```

`check` 是读写探针与配置检查；服务主命令为 `run`，Compose 用只读 `health` 检查就绪。默认宿主机端口为 `127.0.0.1:8080`，网页与 API 共用该端口；`/api/status` 的业务降级与容器 healthy 分开判断。容器内 `/data` 映射宿主机数据目录（本地磁盘或 CIFS 挂载），`/logs` 是本地有限滚动日志。

`STOCK_WATCHER_WEB_BIND` 可调整宿主机入口；容器内固定监听 `0.0.0.0:8080`。健康检查使用只读 `health` 命令，不调用远端行情或飞书。匿名公网入口必须显式设置 `STOCK_WATCHER_WEB_PUBLIC_READ_ONLY=true`；该模式只允许 GET/HEAD，并隐藏 AI 与预测写入口。

## 升级、停止与回滚

升级先构建明确 SHA 的镜像并执行 version/check，再停止旧实例，将 `.env` 的 IMAGE/VERSION 更新为新值，执行 `up -d --no-build --force-recreate`。使用 `docker image inspect --format '{{.Id}}' <image>` 记录实际镜像 ID；`exec stock-watcher stock-watcher version` 核对运行制品。exec 后第一个 stock-watcher 是服务名，第二个是容器内二进制名。

```bash
docker compose --env-file .env -f docker/compose.yaml down
```

停止不会删除宿主机数据。回滚到保留的旧镜像标签并重建服务；先确认配置与持久账本格式兼容。共享故障排查、凭证加载和部署验收见[部署文档](../docs/deployment.md)。

网页提供全量股池、摘要快照、每日排名和复盘回看。网页没有账号体系；除受写模式约束的 AI 配置外均为只读，远程访问需通过受控网络或已有认证反向代理，根路径、`/api/` 和 `/assets/` 应保持一致。

行情库 schema 6 升级、只读健康、报告重放、停机备份和回滚流程见 [部署说明](../docs/deployment.md#框架升级与恢复)。

## AI 配置卷

Compose 将命名卷 `llm-config` 挂载到容器内 `/config`，并固定 `STOCK_WATCHER_LLM_CONFIG_FILE=/config/llm.config`，因此在只读根文件系统下仍可由网页保存 AI 配置。该卷独立于 `/data`，备份与迁移时应单独保留。默认 `STOCK_WATCHER_LLM_WRITE_MODE=loopback`；容器外的浏览器请求通常来自桥接网络而非回环地址，需要在运行变量文件中显式设为 `all` 才允许网页保存。`all` 不提供认证，远程开放前必须配置受控网络或认证反向代理。
