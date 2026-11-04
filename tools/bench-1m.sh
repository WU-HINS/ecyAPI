#!/usr/bin/env bash
# bench-1m.sh —— 百万级本地性能基准：生成数据集 → 两种模式各跑一遍 → 输出对比表
# 用法: tools/bench-1m.sh [目录] [分类数] [每类张数]
set -euo pipefail
DIR=${1:-/tmp/bench1m}
CATS=${2:-2000}
PER=${3:-500}
BIN=${ECY_BIN:-./ecyapi}
PORT_A=${PORT_A:-19841}
PORT_B=${PORT_B:-19842}

echo "== 生成数据：${CATS} 分类 × ${PER} 张 = $((CATS*PER)) 张 =="
mkdir -p "$DIR/src"
for i in $(seq 0 19); do [ -f "$DIR/src/s$i.png" ] || cp "${SRC_PNG:-/tmp/scale/small.png}" "$DIR/src/s$i.png"; done
python3 - "$DIR" "$CATS" "$PER" <<'PY'
import os, sys
d, cats, per = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
root = os.path.join(d, 'categories')
for c in range(cats):
    cd = os.path.join(root, 'c%04d' % c)
    os.makedirs(cd, exist_ok=True)
    for i in range(per):
        dst = os.path.join(cd, 'p%04d.png' % i)
        if not os.path.exists(dst):
            os.link(os.path.join(d, 'src', 's%d.png' % ((c + i) % 20)), dst)
print('ok')
PY

run() { # $1=mode $2=port
  pkill -x "$(basename "$BIN")" 2>/dev/null || true; sleep 1
  rm -f "$DIR/index.bin" "$DIR/index.snapshot.gz"
  local t0=$(date +%s%N)
  env ECY_ADDR=127.0.0.1:$2 ECY_DATA_DIR="$DIR/categories" ECY_ADMIN_PASSWORD=pw \
      ECY_ACCESS_LOG=off ECY_STARTUP_WAIT=900 ECY_INDEX_MODE=$1 \
      "$BIN" >"$DIR/log-$1" 2>&1 &
  local pid=$!
  for i in $(seq 3000); do curl -sf -o /dev/null "http://127.0.0.1:$2/readyz" && break; sleep 0.3; done
  local t1=$(date +%s%N)
  sleep 5
  local rss=$(awk '/VmRSS/{print int($2/1024)}' /proc/$pid/status)
  local hwm=$(awk '/VmHWM/{print int($2/1024)}' /proc/$pid/status)
  printf '  %-8s ready %6d ms | RSS %4d MB | peak %4d MB\n' "$1" $(( (t1-t0)/1000000 )) "$rss" "$hwm"
  kill $pid 2>/dev/null; sleep 2
}

echo '== 冷启动（无索引） =='
run memory "$PORT_A"
run compact "$PORT_B"