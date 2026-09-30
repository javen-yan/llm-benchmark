# BUILD.md — 构建手册

本文档描述如何从源码稳定构建 LLM Benchmark。前端构建产物（`internal/webui/dist/assets/`，约 2MB）**不进仓库**，每次 clone 后按本手册本地构建一次即可。

## 1. 环境要求

| 工具 | 版本要求 | 说明 |
|---|---|---|
| Go | ≥ 1.24 | `go version` 确认 |
| Node.js | ≥ 20 | `node -v` 确认 |
| npm | ≥ 10 | `npm -v` 确认 |
| Python + pip | 3.10+ | 仅 GuideLLM 引擎需要 |

## 2. 构建顺序（重要：先前端，后 Go）

Go 二进制通过 `go:embed` 把 `internal/webui/dist/` 打包进单文件，**必须先构建前端**，否则嵌入的是占位页面（访问 `/` 会提示"前端尚未构建"）。

```bash
# 1) 构建前端（输出到 internal/webui/dist/）
cd web
npm ci            # 严格按 package-lock.json 安装，165 个包
npm run build     # 含 tsc 类型检查 + vite 打包
cd ..

# 2) 质量门禁
gofmt -l .         # 应无输出
go vet ./...
go test ./...

# 3) 构建 Go 二进制
go build -o bin/server ./cmd/server
go build -o bin/bench ./cmd/bench
```

构建产物：`bin/server`（约 43MB，含 Web UI）、`bin/bench`（约 31MB，CLI）。

## 3. 验证构建成功

```bash
# 终端 1：启动模拟目标
./bin/bench mock-target --listen :9000

# 终端 2：启动平台服务
LISTEN=:8080 ./bin/server

# 终端 3：冒烟检查
curl -s http://127.0.0.1:8080/health            # 应返回 {"status":"ok"}
curl -s http://127.0.0.1:8080/ | head -c 200    # 应返回前端 HTML（不是"前端尚未构建"占位页）
curl -s http://127.0.0.1:8080/api/v1/engines   # 应返回 ["mock","guidellm"]

# 跑一次完整压测
./bin/bench run -f examples/spec-mock.yaml --server http://127.0.0.1:8080
# 等待约 10 秒后
./bin/bench report <run-id> --server http://127.0.0.1:8080
```

浏览器打开 http://127.0.0.1:8080 应看到深色中文 Web UI（运行列表页），而不是占位提示页。

## 4. 开发模式（前后端分离）

```bash
# 终端 1：Go 服务（API）
go run ./cmd/server

# 终端 2：前端热更新
cd web && npm run dev   # 默认 http://127.0.0.1:5173，需配置代理到 :8080（见 web/vite.config.ts）
```

## 5. 常见问题

**Q: 打开 `/` 显示"前端尚未构建"？**
A: 构建顺序错了。先 `cd web && npm run build`，再 `go build ./...`，然后重启 server。

**Q: `npm ci` 很慢或失败？**
A: 需要访问 npm registry。国内环境请先配置镜像：
```bash
npm config set registry https://registry.npmmirror.com
```

**Q: `/tmp` 报 No space left on device？**
A: 某些系统 `/tmp` 是小容量 tmpfs。设置 `TMPDIR` 指向大分区：
```bash
export TMPDIR=$HOME/.tmp && mkdir -p $TMPDIR
```

**Q: Go 构建报 `go:embed all:dist` 找不到 dist？**
A: 仓库里 `internal/webui/dist/index.html` 是占位文件，保证目录存在。只要正常 clone 就不会缺；如果手动删过，重新 `git checkout -- internal/webui/dist/`。

**Q: 前端构建产物要提交吗？**
A: 不用。`internal/webui/dist/assets/` 已在 `.gitignore` 中。CI/部署时按 §2 重新构建即可，保证产物与源码一致。

## 6. Docker（可选）

```dockerfile
# 构建阶段：前端 + Go
FROM node:24 AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24 AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN go build -o /bin/server ./cmd/server

# 运行阶段
FROM debian:bookworm-slim
COPY --from=go /bin/server /bin/server
EXPOSE 8080
CMD ["/bin/server"]
```
