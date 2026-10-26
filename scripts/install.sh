#!/usr/bin/env bash
# 天方云签 · Docker 一键安装
# 用法：bash scripts/install.sh --dir /opt/tfcloud --port 8787
set -euo pipefail

INSTALL_DIR="/opt/tfcloud"
PORT="8787"
REPO="${TFCLOUD_REPO:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    --port) PORT="$2"; shift 2 ;;
    --repo) REPO="$2"; shift 2 ;;
    -h|--help)
      echo "用法: $0 [--dir /opt/tfcloud] [--port 8787] [--repo <git 地址>]"
      exit 0 ;;
    *) echo "未知参数: $1"; exit 2 ;;
  esac
done

if ! command -v docker >/dev/null 2>&1; then
  echo "未检测到 Docker，请先安装 Docker 与 Docker Compose v2" >&2
  exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
  echo "需要 Docker Compose v2（docker compose 子命令）" >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"

if [[ ! -f docker-compose.yml ]]; then
  if [[ -z "$REPO" ]]; then
    echo "当前目录没有 docker-compose.yml，请先把项目放到 $INSTALL_DIR，或用 --repo 指定 git 地址" >&2
    exit 1
  fi
  git clone --depth 1 "$REPO" .
fi

mkdir -p data
chmod 750 data

echo "==> 构建并启动（端口 $PORT）"
TFCLOUD_PORT="$PORT" docker compose up -d --build

echo "==> 等待服务就绪"
for i in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$PORT/health/ready" >/dev/null 2>&1; then
    echo "就绪：http://127.0.0.1:$PORT/install"
    exit 0
  fi
  sleep 2
done

echo "服务启动超时，请查看：docker compose logs -f" >&2
exit 1
