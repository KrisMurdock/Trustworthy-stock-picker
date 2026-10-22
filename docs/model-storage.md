# 模型、特征与评估产物存储

本约定适用于离线下载的第三方模型、研究阶段训练出的模型、构建出的特征集和评估结果。模型权重、特征数据和评估产物不进入 Git，也不放在服务器根盘的工作区或 `.scratch/`；Windows/共享盘是唯一持久化位置。

## 根目录

以生产挂载点 `$STOCK_WATCHER_DATA_DIR`（当前为 `/mnt/windows_database`）为例：

```text
<data_dir>/
├── download_models/<provider>/<model>/<variant>/
├── trained_models/<stage>/<experiment>/<run-id>/
├── build_features/<stage>/<experiment>/<feature-set>/
└── evaluations/<stage>/<experiment>/<run-id-or-date>/
```

阶段名固定使用以下小写标识，避免中文目录在不同文件系统或脚本间出现编码差异：

| 阶段 | 标识 |
| --- | --- |
| 初筛阶段 | `initial_screening` |
| 精排阶段 | `reranking` |
| 5 分钟预测 | `intraday_prediction` |
| 盘前全天高低价预测 | `dayrange_prediction` |

## 当前目录

```text
/mnt/windows_database/download_models/
├── jev/Qwen3-0.6B/
├── laya/official/
├── laya/multilingual/
├── chinese-decision/chinese-decision-0.8b/
├── laya/_incomplete/
└── _cache/

/mnt/windows_database/trained_models/
├── initial_screening/
├── reranking/
├── intraday_prediction/
└── dayrange_prediction/

/mnt/windows_database/build_features/
├── initial_screening/firststage-recall/firststage-recall-milestone-artifacts/
├── reranking/secondstage-compare/
├── intraday_prediction/predictor-calibration-v7/predictor-v8/
├── intraday_prediction/feature-optimization/feature-opt-20261021/
├── intraday_prediction/market-context-v6/market-context-v6/
├── intraday_prediction/predictor-direction-v9/v9/
├── intraday_prediction/predictor-cross-sectional/predictor-eval-cross-sectional/
└── dayrange_prediction/dayrange-closeout/

/mnt/windows_database/evaluations/
├── reranking/secondstage-compare/
└── intraday_prediction/predictor-calibration-v7/v7/
```

`trained_models/` 目前只建立阶段目录；尚未产生需要独立落盘的研究训练模型。生产构建中内嵌的第一层、第二层模型不属于外部研究模型，见下文运行时边界。

已下载权重的稳定目录与校验值：

| 用途 | 本地目录 | 权重字节数 | `model.safetensors` SHA-256 |
| --- | --- | ---: | --- |
| JEV（Qwen3-0.6B） | `download_models/jev/Qwen3-0.6B/` | 1,503,300,328 | `f47f71177f32bcd101b7573ec9171e6a57f4f4d31148d38e382306f42996874b` |
| Laya official | `download_models/laya/official/` | 842,609,210 | `891102d372688fc2a094dac56a384bc537b87c63f21f9f3dac0be2b7cbc8d86c` |
| Laya multilingual | `download_models/laya/multilingual/` | 643,835,514 | `9d628fd971b700382ac6f65920a86f149777b2e748e0c955fb3b19695aa8f204` |
| Chinese Decision 0.8B | `download_models/chinese-decision/chinese-decision-0.8b/` | 1,504,827,608 | `341307aa5cbc3825db3ab50772a5837ab5945bafd3e23fd2a5afcb015873a18f` |

`download_models/laya/_incomplete/` 只保存未完成或弃用的下载；完整模型不得依赖这些目录。下载完成并校验大小、SHA-256 后，应移动到稳定的模型目录，再删除半成品。

## 使用规则

- 下载模型时先写临时目录或 `.part` 文件，校验完成后再移动到正式目录；不得把权重写到仓库工作区。
- 特征集必须保留生成脚本、输入日期区间、字段版本和数据校验信息；原始行情仍由 `<data_dir>` 下的 SQLite 管理，不复制到 `build_features/`。
- 训练模型必须按阶段和实验名隔离，每次训练使用稳定 `run-id`；不得用 `current`、`latest` 等可变名称作为唯一路径。
- 评估结果与特征集分开放置；`comparison.json`、bootstrap 和逐日指标属于 `evaluations/`，不得混入下载模型目录。
- CIFS 挂载不保证支持符号链接或硬链接。脚本必须显式传递规范路径，例如 `--model /mnt/windows_database/download_models/chinese-decision/chinese-decision-0.8b`，不能依赖共享盘内软链接。
- `internal/firststage/model.json` 与 `internal/secondstage/model.json` 是编译进 Go 二进制的生产模型，必须随源码版本化，不能改为运行时从共享盘加载。
- 生产进程自动训练并立即消费的 5 分钟预测模型属于 `<data_dir>/models/intraday/` 运行时状态，不是研究资产；其目录、`current.json` 指针和 SQLite 中的 artifact 路径共同构成 Go 运行时契约。迁移该路径需要同时修改 Go 存储契约、迁移历史记录并停止训练写入，不能在服务运行时直接搬动。

## 清理边界

`.scratch/` 只保留可丢弃的临时输出和本地虚拟环境。Python 虚拟环境包含可执行文件，CIFS 通常以 `noexec` 挂载，不能迁入共享盘；模型权重、特征和评估产物迁走后，应删除对应的工作区副本和重复缓存。
