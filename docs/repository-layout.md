# 工程目录与补齐范围

以 `/root/data/codebase/support-agent` 为工程组织参考，按 stock_watcher 的 Go CLI、Windows CIFS 存储和定时任务形态补齐。

| 工程能力 | 原有 stock_watcher | support-agent 参考 | 本仓库落点 |
| --- | --- | --- | --- |
| 容器构建 | 根 Dockerfile | 根 Dockerfile；没有受 Git 管理的 docker/ | 保留唯一根 Dockerfile，增加版本标签、构建上下文白名单 |
| 容器部署 | README 中手动 docker run | 由其服务部署体系负责 | docker/compose.yaml、docker/.env.example、docker/README.md |
| 环境配置 | YAML 示例和 README 零散说明 | .env.example、环境变量文档 | .env.example、docs/environment-variables.md；私有配置不进 Git/镜像/发布包 |
| 开发命令 | test、vet、build、image、clean | Makefile 提供开发和打包入口 | make help/run/check/status/ci/package/docker-smoke |
| 发布包 | 单个本地二进制 | .builds 下按系统和架构打包 | .builds/stock-watcher/linux/{amd64,arm64}，带配置、股池、文档、SHA、校验和 |
| 自动验证 | Go 测试 | GitLab CI 和 scripts | .github/workflows/ci.yml、scripts/ci.sh，适配本仓库 GitHub 托管 |
| 冒烟验收 | Go 包内测试 | tests/ | tests/smoke.sh：真实二进制与容器、配置/存储失败、权限及退出 |
| 开发运维说明 | README 与 systemd 模板 | docs/、MR 模板 | docs/development.md、docs/deployment.md、GitHub PR 模板 |

## 目录职责

```text
cmd/                     Go 命令入口
internal/                配置、数据源、存储、筛选、研究、通知、日志等模块
config.yaml.example      公开业务配置示例（私有 config.yaml 不提交）
leaders.yaml             已核对股池
leaders.pending.yaml     待人工确认名单
.env.example             本地/生产环境变量参考
Dockerfile               唯一多阶段镜像构建定义
.dockerignore            构建上下文白名单
Makefile                 开发、验证、构建与打包入口
docker/                  Compose 部署及宿主机路径示例
deploy/                  systemd 服务模板
scripts/                 构建、打包、开发启动辅助脚本
tests/                   CLI/容器冒烟与验收说明
docs/                    工程、配置与运维说明
.github/                 GitHub Actions、PR 模板
bin/                     本机二进制（忽略）
.builds/                 发布包（忽略）
logs/                    有限滚动日志（忽略）
data/                    Windows 共享盘链接或挂载（忽略）
```

scripts/ 只组织构建或 exec Go 二进制，生产容器和 systemd 直接运行 Go；没有恢复旧 Python 启动链路。现有根配置路径保持不变，避免为了目录齐全移动用户的私有配置。

support-agent 的 LLM 提示词、内部镜像仓库与 GitLab 私有流水线依赖不适用于本项目，因此不创建 db/、conf/prompts 或空占位目录。本项目已提供 Go 内嵌只读网页及 HTTP API，同一监听端口承载 `/healthz`、`/readyz` 和 `/api/status`，无需独立前端容器。SQLite 迁移由 `internal/storage` 的版本化代码管理，不创建空的 db/ 目录。

`internal/selection/engine.go` 定义日常算法计算边界；`internal/health` 汇总只读业务健康；任务和快照仍由 `internal/storage` 管理。实验代码保持独立，实验结果不会自动进入生产推送。
