#!/bin/sh
# 以 root 启动时先修正数据卷属主，再降权运行，兼容 bind mount 与 named volume。
set -e
DATA_DIR="${TF_DATA_DIR:-/data}"
if [ "$(id -u)" = "0" ]; then
  mkdir -p "$DATA_DIR" "$DATA_DIR/uploads"
  chown -R tfcloud:tfcloud "$DATA_DIR" 2>/dev/null || true
  exec su-exec tfcloud "$@"
fi
exec "$@"
