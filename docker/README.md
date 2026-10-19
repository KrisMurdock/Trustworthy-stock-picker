# Compose 部署

从仓库根目录运行，要求 Docker Compose ≥ 2.30.0。唯一镜像定义在根 Dockerfile；此目录提供部署参数，不复制业务配置或密钥。

## 首次准备

```bash
cp -n docker/.env.example docker/.env
chmod 600 docker/.env
mkdir -p logs
```

编辑 `docker/.env`，把 HOST_POOL/LOGS 改为本机绝对路径；HOST_DATA 指向本地数据目录或现有 CIFS 挂载。使用本地目录时在运行变量文件中保持 `STOCK_WATCHER_REQUIRE_CIFS=false`；使用共享盘时设为 `true`。业务配置已内置在镜像中，不再挂载 `config.yaml`；股池固定读取 `/app/leaders.yaml`。准备 `STOCK_WATCHER_RUNTIME_ENV` 指向的私有文件（示例 `/etc/stock-watcher.env`），在其中设置 `STOCK_WATCHER_FEISHU_ENABLED=false` 或提供飞书凭据；其变量语法见[环境文档](../docs/environment-variables.md)。宿主机数据目录、股池与日志目录必须已经存在。

## 构建与检查

```bash
export STOCK_WATCHER_VERSION="$(git rev-parse HEAD)"
export STOCK_WATCHER_IMAGE="stock-watcher:$STOCK_WATCHER_VERSION"
docker compose --env-file docker/.env -f docker/compose.yaml config --quiet
docker compose --env-file docker/.env -f docker/compose.yaml build
docker compose --env-file docker/.env -f docker/compose.yaml run --rm --no-deps stock-watcher version
docker compose --env-file docker/.env -f docker/compose.yaml run --rm --no-deps stock-watcher check
```

将上面确定的 IMAGE/VERSION 也记录到 `docker/.env`，确保新的终端仍使用同一版本。Compose 命令的 shell 环境变量优先于该文件；切换版本时同步更新或清除旧的 shell 变量。不使用普通 `config` 输出，避免展开运行凭证。

## 启动与查看

若同目录已有 systemd 或手工运行的写入进程，先停止它，再启动容器。systemd 与 Compose 只能选择一种常驻运行方式。

```bash
docker compose --env-file docker/.env -f docker/compose.yaml up -d --no-build
docker compose --env-file docker/.env -f docker/compose.yaml ps
docker compose --env-file docker/.env -f docker/compose.yaml logs --tail 50
docker compose --env-file docker/.env -f docker/compose.yaml exec stock-watcher stock-watcher status
```

`check` 是读写探针与配置检查；服务主命令为 `run`，Compose 用只读 `health` 检查就绪。默认宿主机端口为 `127.0.0.1:8080`，网页与 API 共用该端口；`/api/status` 的业务降级与容器 healthy 分开判断。容器内 `/data` 映射宿主机数据目录（本地磁盘或 CIFS 挂载），`/logs` 是本地有限滚动日志。

## 升级、停止与回滚

升级先构建明确 SHA 的镜像并执行 version/check，再停止旧实例，将 docker/.env 的 IMAGE/VERSION 更新为新值，执行 `up -d --no-build --force-recreate`。使用 `docker image inspect --format '{{.Id}}' <image>` 记录实际镜像 ID；`exec stock-watcher stock-watcher version` 核对运行制品。exec 后第一个 stock-watcher 是服务名，第二个是容器内二进制名。

```bash
docker compose --env-file docker/.env -f docker/compose.yaml down
```

停止不会删除宿主机数据。回滚到保留的旧镜像标签并重建服务；先确认配置与持久账本格式兼容。共享故障排查、凭证加载和部署验收见[部署文档](../docs/deployment.md)。
## 网页入口

`run` 会同时提供监控任务与嵌入网页。Compose 将容器 8080 映射至宿主机 `127.0.0.1:8080`；浏览器打开该地址即可查看全量股池、摘要快照和每日排名。修改 `docker/.env` 的 `STOCK_WATCHER_WEB_BIND` 可调整宿主机地址/端口。网页没有账号或写入接口，远程访问需通过受控网络或已有认证反向代理，根路径、`/api/` 和 `/assets/` 应保持一致。

行情库 schema 4 升级、只读健康、报告重放、停机备份和回滚流程见 [部署说明](../docs/deployment.md#框架升级与恢复)。
