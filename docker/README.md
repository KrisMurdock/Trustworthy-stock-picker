# Compose 部署

从仓库根目录运行，要求 Docker Compose ≥ 2.30.0。唯一镜像定义在根 Dockerfile；此目录提供部署参数，不复制业务配置或密钥。

## 首次准备

```bash
cp -n docker/.env.example docker/.env
chmod 600 docker/.env
mkdir -p logs
# 已有配置/环境文件不得覆盖。
cp -n config.yaml.example config.yaml
chmod 600 config.yaml
```

编辑 `docker/.env`，把 HOST_CONFIG/POOL/LOGS 改为本机绝对路径；HOST_DATA 指向现有 CIFS 挂载。配置里的 `pool_file` 使用 `leaders.yaml`。准备 `STOCK_WATCHER_RUNTIME_ENV` 指向的私有文件（示例 `/etc/stock-watcher.env`），关闭通知时可为空，权限 600；其变量语法见[环境文档](../docs/environment-variables.md)。所有路径必须已经存在。

## 构建与检查

```bash
export STOCK_WATCHER_VERSION="$(git rev-parse HEAD)"
export STOCK_WATCHER_IMAGE="stock-watcher:$STOCK_WATCHER_VERSION"
docker compose --env-file docker/.env -f docker/compose.yaml config --quiet
docker compose --env-file docker/.env -f docker/compose.yaml build
docker compose --env-file docker/.env -f docker/compose.yaml run --rm --no-deps stock-watcher version
docker compose --env-file docker/.env -f docker/compose.yaml run --rm --no-deps stock-watcher check -config /app/config.yaml
```

将上面确定的 IMAGE/VERSION 也记录到 `docker/.env`，确保新的终端仍使用同一版本。Compose 命令的 shell 环境变量优先于该文件；切换版本时同步更新或清除旧的 shell 变量。不使用普通 `config` 输出，避免展开运行凭证。

## 启动与查看

若同目录已有 systemd 或手工运行的写入进程，先停止它，再启动容器。systemd 与 Compose 只能选择一种常驻运行方式。

```bash
docker compose --env-file docker/.env -f docker/compose.yaml up -d --no-build
docker compose --env-file docker/.env -f docker/compose.yaml ps
docker compose --env-file docker/.env -f docker/compose.yaml logs --tail 50
docker compose --env-file docker/.env -f docker/compose.yaml exec stock-watcher status -config /app/config.yaml
```

check 是读写探针与配置检查；服务主命令是 `run`，健康状态应为 healthy。没有对外 HTTP 端口。容器内 /data 直接映射 Windows，/logs 是本地有限滚动日志。

## 升级、停止与回滚

升级先构建明确 SHA 的镜像并执行 version/check，再停止旧实例，将 docker/.env 的 IMAGE/VERSION 更新为新值，执行 `up -d --no-build --force-recreate`。使用 `docker image inspect --format '{{.Id}}' <image>` 记录实际镜像 ID；`exec stock-watcher version` 核对运行制品。

```bash
docker compose --env-file docker/.env -f docker/compose.yaml down
```

停止不会删除宿主机数据。回滚到保留的旧镜像标签并重建服务；先确认配置与持久账本格式兼容。共享故障排查、凭证加载和部署验收见[部署文档](../docs/deployment.md)。
