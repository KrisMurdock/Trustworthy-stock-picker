# 看板视觉与环境层 3D

网页采用深色唯一主题，不提供浅色切换。所有页面共享 `style.css` 的设计令牌：近黑墨色背景、象牙白正文、发丝线分隔，青玉与铜金作为强调色；A 股涨跌仍为红涨绿跌，并重新校准到深色背景上的对比度。中文使用系统无衬线字体，英文标题与数字使用自托管的 IBM Plex Serif 子集；字体按 SIL OFL 1.1 分发，许可证位于 `internal/dashboard/static/fonts/OFL.txt`。

## 环境层

摘要页（`/`）在页头与摘要卡背后挂载一个装饰性 3D 环境层。它只表达氛围，不承载精确读数，`aria-hidden="true"` 且 `pointer-events: none`，不进入 Tab 顺序；数据缺失时呈中性外观，不伪装健康状态。

- 数据来源：`overview.js` 派发 `stockwatcher:snapshot`，并写入 `window.__stockWatcherAmbient` 供场景初始化读取。
- 映射：十年完整性覆盖率控制地形起伏与网格密度，当日可分析比例控制光点强度，运行状态控制色温与呼吸节奏。
- 状态：`idle`、`healthy`、`degraded`、`syncing`、`empty`，同步写入 `#ambient-scene` 的 `data-scene-state`。
- 动效：30fps 上限、缓慢自转、指针视差不超过 4px；页面隐藏或离开视口即暂停，`prefers-reduced-motion: reduce` 只渲染一帧。
- 降级：WebGL 不可用、模型加载失败、shader 失败或上下文丢失时保留静态海报，只输出一次 `console.info`，不影响任何数据展示。
- 性能：DPR 固定为 1，关闭抗锯齿；`Save-Data` 或 `navigator.hardwareConcurrency <= 2` 时直接使用海报。

新增资源总量控制在 1.5 MB 以内（three.js bundle、GLB、字体、海报），由 `tests/dashboard_smoke.py` 按字节断言。

## 资源与再生成

| 资源 | 来源 |
| --- | --- |
| `vendor/three-bundle.min.js` | three.js 0.186.1 + GLTFLoader，esbuild 0.25.0 打包为 ESM，MIT 许可见 `vendor/LICENSE-three.txt` |
| `models/dome.glb` | `scripts/generate_dome_glb.py` 生成的 icosphere（20,480 三角面、10,242 顶点） |
| `fonts/plex-serif-*.woff2` | IBM Plex Serif 数字与拉丁子集，SIL OFL 1.1 |
| `scene-poster.svg` | WebGL 不可用时的静态降级海报 |

重新生成模型：

```bash
python3 scripts/generate_dome_glb.py internal/dashboard/static/models/dome.glb
```

重新打包 three.js（需要 Node.js，仅在更新依赖时执行，运行时与构建镜像都不依赖 Node）：

```bash
mkdir -p /tmp/three-bundle && cd /tmp/three-bundle
npm init -y && npm install three@0.186.1
printf 'import * as THREE from "three";\nimport { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";\nexport { THREE, GLTFLoader };\n' > main.js
npx esbuild@0.25.0 main.js --bundle --minify --format=esm --platform=browser --target=es2020 \
  --outfile=<repo>/internal/dashboard/static/vendor/three-bundle.min.js
```

## 验证

```bash
make dashboard-smoke          # 真实 CLI + Chromium，含环境层断言
make docker-dashboard-smoke   # 容器形态
```

浏览器门禁会检查环境层挂载、状态属性、字体、对比度、资源预算、移动端首屏、减少动效和 WebGL 失败降级。3D 持续帧率依赖宿主机 WebGL 实现；软件渲染环境下若上下文丢失，页面必须保留海报且无控制台错误。
