#!/bin/sh
set -e
# 数据根目录（ECY_DATA_DIR 的父目录），如默认 /app/data/categories -> /app/data
DATA_ROOT="$(dirname "$ECY_DATA_DIR")"

if [ "$(id -u)" = "0" ]; then
    # 以 root 初始化数据卷：兼容宿主空目录 -v 挂载（属主通常为 root），
    # 修正为 ecy 后应用才能自动创建 categories 并写 settings/endpoints/logs。
    mkdir -p "$DATA_ROOT"
    chown -R ecy:ecy "$DATA_ROOT"
    exec su-exec ecy:ecy /app/ecyapi "$@"
fi

# 非 root 场景（k8s runAsNonRoot / compose user: "10001"）：直接启动，
# 前提是数据卷已由外部预置正确属主。
exec /app/ecyapi "$@"
