#!/usr/bin/env bash
# 部署后冒烟测试：健康检查 → JSON 接口 → 图片访问 → 缩略图。
# 用法：./smoke.sh [BASE_URL] [分类路径]
#   ./smoke.sh http://127.0.0.1:8080 anime
set -euo pipefail

BASE="${1:-http://127.0.0.1:8080}"
CAT="${2:-}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "ok   $*"; }

echo "== 目标：$BASE =="

# 1) 健康检查
body="$(curl -fsS --max-time 5 "$BASE/healthz")" || fail "/healthz 不可达"
[ "$body" = "ok" ] || fail "/healthz 返回 $body"
ok "/healthz"

# 2) 首页可达
code="$(curl -s -o /dev/null -w %{http_code} --max-time 5 "$BASE/")"
[ "$code" = "200" ] || fail "首页 HTTP $code"
ok "/ 首页"

# 3) 图片接口（给了分类才测）
if [ -n "$CAT" ]; then
  json="$(curl -fsS --max-time 5 "$BASE/json?$CAT")" || fail "/json?$CAT 失败"
  case "$json" in
    *\"link\"*) : ;;
    *) fail "/json?$CAT 返回异常: $json" ;;
  esac
  url="$(printf %s "$json" | sed -n 's/.*"link":"\([^"]*\)".*/\1/p')"
  [ -n "$url" ] || fail "无法解析 link 字段"
  ok "/json?$CAT -> $url"

  code="$(curl -s -o /dev/null -w %{http_code} --max-time 10 "$url")"
  [ "$code" = "200" ] || fail "图片地址 HTTP $code"
  ok "图片访问"

  ctype="$(curl -s -o /dev/null -w %{content_type} --max-time 20 "$url?w=480")"
  [ "$ctype" = "image/webp" ] || fail "缩略图 content-type=$ctype"
  ok "缩略图 ?w=480 -> $ctype"
fi

echo "全部通过"
