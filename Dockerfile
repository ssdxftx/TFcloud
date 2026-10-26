# ---------- 1. 构建前端 ----------
# 前端产物与架构无关，固定用构建机架构，避免 arm64 在 QEMU 下慢慢跑 node
FROM --platform=$BUILDPLATFORM node:22-alpine AS ui
# 国内构建可传 --build-arg NPM_REGISTRY=https://registry.npmmirror.com
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src
RUN corepack enable && npm config set registry "$NPM_REGISTRY"
COPY ui/package.json ui/pnpm-lock.yaml ./ui/
WORKDIR /src/ui
RUN pnpm install --frozen-lockfile
COPY ui/ ./
# vite.config.ts 的 outDir 指向 ../internal/webui/dist
RUN mkdir -p /src/internal/webui/dist && pnpm build

# ---------- 2. 构建后端（含前端产物） ----------
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
# 国内构建可传 --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://goproxy.cn,direct
# buildx 注入的目标平台，Go 直接交叉编译，不依赖 QEMU
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
ENV CGO_ENABLED=0 GOPROXY=${GOPROXY} GOSUMDB=sum.golang.org
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/webui/dist ./internal/webui/dist
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -tags embedui -trimpath -ldflags="-s -w" -o /out/tfcloud ./cmd/tfcloud

# ---------- 3. 运行 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata su-exec && \
    addgroup -S tfcloud && adduser -S -G tfcloud -h /app tfcloud && \
    mkdir -p /data && chown -R tfcloud:tfcloud /data
WORKDIR /app
COPY --from=builder /out/tfcloud /app/tfcloud
COPY VERSION /app/VERSION
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh
ENV TF_DATA_DIR=/data \
    HTTP_ADDR=:8787 \
    TZ=Asia/Shanghai
VOLUME ["/data"]
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8787/health/ready >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/tfcloud", "serve"]
