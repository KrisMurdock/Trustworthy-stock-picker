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
# 首次部署才复制；已有私有配置不要覆盖。
cp -n config.yaml.example config.yaml
chmod 600 config.yaml
make check CONFIG=config.yaml
make status CONFIG=config.yaml
make run CONFIG=config.yaml
```

运行依赖可用 Windows 挂载；本地测试才使用 `require_cifs: false`。`scripts/run.sh` 只为 `make run` 加载开发环境变量后 exec Go，容器和 systemd 均直接启动 Go。

## 发布制品

```bash
make package                  # 默认 linux/amd64
make package BUILD_GOARCH=arm64
cd .builds/stock-watcher/linux/amd64
sha256sum -c SHA256SUMS
./bin/stock-watcher version
```

发布目录含二进制、公开配置和环境变量示例、已确认/待确认股池、systemd 模板、README 与 docs、`VERSION`、`COMMIT`、`SHA256SUMS`。这是原生运行包；Docker/CI/开发脚本从源码仓库使用。arm64 可以交叉构建，但需在对应机器验收运行。

默认 VERSION 为完整 Git HEAD，镜像 OCI revision 与 `stock-watcher version` 同值。正式发布应在干净提交上构建；本地未提交修改不会自动改变版本字符串。覆盖 VERSION 仅用于开发标记。发布包按白名单复制文件，既不包含私有配置和凭证，也不包含行情和日志。

GitHub Actions 在 master/main 推送、PR 或手工触发时执行 `scripts/ci.sh` 和容器冒烟，保留 Linux amd64 发布包 7 天；不发布镜像、不部署、不发送飞书消息。流程使用 [GitHub 官方 Go Actions 模式](https://docs.github.com/en/actions/tutorials/build-and-test-code/go)。

`make clean` 只删除 bin/ 和 .builds/；不会删除行情、私有配置或日志。
