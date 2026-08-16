# sqlitex-admin UI

SQLiteX Admin 的前端工程（Vite + 原生 JS，无运行时框架依赖）。

## 独立构建（发布流程）

```bash
npm install     # 仅 vite 一个 devDependency
npm run build   # 产物输出到 dist/，已被 go:embed 嵌入 Go 二进制
```

`dist/` 随仓库提交：Go 侧编译**不依赖 Node 环境**，仅在前端改动后需要重新 `npm run build`。

## 开发模式（前后端独立迭代）

```bash
# 终端 1：启动 Go 服务（提供 API）
cd ../../../ && go run ./cmd/sqlitex-admin -dir <数据目录> -addr 127.0.0.1:8080

# 终端 2：启动 Vite dev server（热更新）
npm run dev     # http://localhost:5173
```

`/api` 与 `/schema` 请求由 Vite 代理转发到 Go 服务（默认 `127.0.0.1:8080`，
8080 被占用时用环境变量覆盖：`SQLITEX_ADMIN_API=http://127.0.0.1:9090 npm run dev`，
见 `vite.config.js`）。改前端代码即时生效，无需重新编译 Go 二进制。

## 目录结构

```
web/
├── index.html          # 页面骨架
├── src/
│   ├── main.js         # 交互逻辑（扫描/分页/详情）
│   └── style.css       # 暗色主题样式
├── vite.config.js      # 构建与开发代理配置
└── dist/               # 构建产物（提交入库，go:embed 嵌入）
```
