#!/usr/bin/env bash
# =============================================================================
# smoke.sh — OmniFusion 全链路本地冒烟测试（mock 上游，零真实 API key）
#
# 对应 docs/05-工程实施计划.md「工作流纪律」第 3 条：真实冒烟脚本本地跑、
# 不进 CI。链路：mockup(127.0.0.1:11434) → ofd(127.0.0.1:20130)，覆盖
# healthz / 网关 key 鉴权 / 模型目录 / 聊天（流式+非流式）/ dashboard
# 五页 / metrics / `ofd status` 共 12 项断言，任一 FAIL 整体退出码 1。
#
# 用法：在仓库根目录执行
#     bash scripts/smoke.sh
#     （本机若 bash 解析到 WSL 而缺 go，可改用 sh scripts/smoke.sh，
#       sh 即 bash 5.3；完整 Git Bash 环境不受影响）
# 前置条件：
#   1. go 工具链可用（脚本现场构建 mockup 与 ofd 两个二进制）；
#   2. 本机 11434 与 20130 端口空闲——被占用时脚本打印警告并直接退出，
#      请先清理占用进程（脚本不会杀不属于自己的进程）。
#
# 说明：本机 Git Bash 缺 sleep 命令，等待函数自适应降级到 python。
# =============================================================================
set -u

REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
BASE=http://127.0.0.1:20130
MOCKUP_ADDR=127.0.0.1:11434
WORKDIR="${TMPDIR:-/tmp}/ofd-smoke-$$"
OFD_PID=""
MOCKUP_PID=""
KEY=""
PASS_COUNT=0
FAIL_COUNT=0

# ---- 基础设施 ---------------------------------------------------------------

die() { # 致命错误：打印日志尾部辅助定位，退出码 1
  echo "[FATAL] $1" >&2
  for f in "$WORKDIR/mockup.log" "$WORKDIR/ofd.log"; do
    if [ -f "$f" ]; then
      echo "---- $f（末 15 行）----" >&2
      tail -n 15 "$f" >&2 2>/dev/null || true
    fi
  done
  exit 1
}

wait_ms() { # $1=毫秒；sleep 缺失时降级 python（python3/python 都试）
  local ms=$1
  if command -v sleep >/dev/null 2>&1; then
    sleep "$(printf '%d.%03d' $((ms / 1000)) $((ms % 1000)))"
  else
    python -c "import time;time.sleep($ms/1000)" 2>/dev/null ||
      python3 -c "import time;time.sleep($ms/1000)"
  fi
}

poll_until() { # $1=超时毫秒 $2=探测命令…→退出码 0 即就绪
  local timeout_ms=$1
  shift
  local step=300
  local tries=$((timeout_ms / step))
  local i
  for ((i = 0; i < tries; i++)); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    wait_ms "$step"
  done
  return 1
}

stop_pid() { # 只杀自己启动的进程：kill 失败再用 taskkill 兜底
  [ -z "${1:-}" ] && return 0
  kill "$1" 2>/dev/null || true
  wait_ms 300
  if kill -0 "$1" 2>/dev/null; then
    taskkill //F //T //PID "$1" >/dev/null 2>&1 || true
  fi
}

cleanup() { # trap EXIT：异常退出也保证清理进程与临时目录
  local rc=$?
  trap - EXIT
  stop_pid "$OFD_PID"
  stop_pid "$MOCKUP_PID"
  wait_ms 200
  rm -rf "$WORKDIR" 2>/dev/null || true
  exit "$rc"
}
trap cleanup EXIT

# 只看 LISTENING 行：避免把 TIME_WAIT 客户端残留（远端列含 :端口）误判为占用
port_busy() { netstat -ano 2>/dev/null | grep LISTENING | grep -qE ":$1[[:space:]]"; }

get_code() { # $1=url [$2=bearer] → stdout: HTTP 状态码
  if [ $# -ge 2 ]; then
    curl -s --noproxy '*' -o /dev/null -w '%{http_code}' \
      -H "Authorization: Bearer $2" "$1"
  else
    curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "$1"
  fi
}

get_body() { # $1=url [$2=bearer] → stdout: 响应体
  if [ $# -ge 2 ]; then
    curl -s --noproxy '*' -H "Authorization: Bearer $2" "$1"
  else
    curl -s --noproxy '*' "$1"
  fi
}

# ---- 断言框架 ---------------------------------------------------------------

report() { # $1=PASS|FAIL $2=名称 [$3=详情]
  if [ "$1" = "PASS" ]; then
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  if [ -n "${3:-}" ]; then
    printf '[%s] %s — %s\n' "$1" "$2" "$3"
  else
    printf '[%s] %s\n' "$1" "$2"
  fi
}

assert_eq() { # $1=实际 $2=期望 $3=名称
  if [ "$1" = "$2" ]; then
    report PASS "$3"
  else
    report FAIL "$3" "got='$1' want='$2'"
  fi
}

extract_content() { # $1=chat 响应体 → stdout: message.content（jq 优先，退化 grep）
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$1" | jq -r '.choices[0].message.content // empty' 2>/dev/null
  else
    printf '%s' "$1" | grep -o '"content":"[^"]*"' | head -n 1 |
      sed -e 's/^"content":"//' -e 's/"$//'
  fi
}

# ---- 环境检查与构建 ---------------------------------------------------------

echo "==> 检查端口 11434 / 20130 是否空闲 ..."
for p in 11434 20130; do
  if port_busy "$p"; then
    echo "[FATAL] 端口 $p 已被占用（冒烟需独占），请先清理占用进程后再试。" >&2
    exit 1
  fi
done

command -v go >/dev/null 2>&1 ||
  die "未找到 go 命令（冒烟需 Go 工具链；mise 用户请确认 shims 目录已入 PATH）"

mkdir -p "$WORKDIR" || die "无法创建临时目录 $WORKDIR"

echo "==> 构建 mockup 与 ofd（输出到 $WORKDIR）..."
(cd "$REPO_ROOT" && go build -o "$WORKDIR/mockup.exe" ./scripts/mockup) ||
  die "go build mockup 失败"
(cd "$REPO_ROOT" && go build -o "$WORKDIR/ofd.exe" ./cmd/ofd) ||
  die "go build ofd 失败"

# ---- 启动进程 ---------------------------------------------------------------

echo "==> 启动 mockup（$MOCKUP_ADDR）..."
(cd "$WORKDIR" && exec ./mockup.exe -addr "$MOCKUP_ADDR" >mockup.log 2>&1) &
MOCKUP_PID=$!
mockup_ready() { curl -s --noproxy '*' -o /dev/null "http://$MOCKUP_ADDR/healthz"; }
poll_until 10000 mockup_ready || die "mockup 10s 内未就绪"

echo "==> 启动 ofd（127.0.0.1:20130，store 落 $WORKDIR/data/）..."
(cd "$WORKDIR" && exec ./ofd.exe >ofd.log 2>&1) &
OFD_PID=$!
ofd_ready() { [ "$(get_code "$BASE/healthz")" = "200" ]; }
poll_until 15000 ofd_ready || die "ofd 15s 内未就绪"

echo "==> 获取网关 key（ofd gateway-key）..."
KEY=$(cd "$WORKDIR" && ./ofd.exe gateway-key 2>/dev/null | tr -d '\r\n')
[ -n "$KEY" ] || die "gateway-key 输出为空"

# catalog 异步同步坑：healthz OK 不代表能聊天，必须轮询目录含 mock 模型
echo "==> 等待模型目录异步同步出 mock-model-1（至多 60s）..."
models_with_key() { get_body "$BASE/v1/models" "$KEY"; }
models_has_mock() { models_with_key | grep -q mock-model-1; }
poll_until 60000 models_has_mock || die "目录 60s 内未出现 mock-model-1"

# ---- 断言清单 ---------------------------------------------------------------

echo "==> 断言开始 ..."
h_code=$(get_code "$BASE/healthz")
assert_eq "$h_code" "200" "GET /healthz → 200"
case $(get_body "$BASE/healthz") in
*ok*) report PASS "GET /healthz 含 ok" ;;
*) report FAIL "GET /healthz 含 ok" "body 无 ok" ;;
esac

assert_eq "$(get_code "$BASE/v1/models")" "401" "GET /v1/models 无 key → 401"

m_code=$(get_code "$BASE/v1/models" "$KEY")
assert_eq "$m_code" "200" "GET /v1/models 带 key → 200"
case "$(models_with_key)" in
*mock-model-1*) report PASS "GET /v1/models 含 mock-model-1" ;;
*) report FAIL "GET /v1/models 含 mock-model-1" "目录无该模型" ;;
esac

chat_resp=$(curl -s --noproxy '*' -w '\n%{http_code}' \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"mock-model-1","stream":false,"messages":[{"role":"user","content":"smoke-nostream"}]}' \
  "$BASE/v1/chat/completions")
chat_code=$(printf '%s\n' "$chat_resp" | tail -n 1)
chat_json=$(printf '%s\n' "$chat_resp" | sed '$d')
assert_eq "$chat_code" "200" "chat 非流式 → 200"
content=$(extract_content "$chat_json")
if [ -n "$content" ]; then
  report PASS "chat 非流式 content 非空" "$content"
else
  report FAIL "chat 非流式 content 非空" "content 提取为空"
fi

sse=$(curl -sN --noproxy '*' \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"model":"mock-model-1","stream":true,"messages":[{"role":"user","content":"smoke-stream"}]}' \
  "$BASE/v1/chat/completions")
data_lines=$(printf '%s\n' "$sse" | grep -c '^data:')
sse_done=no
case "$sse" in *'[DONE]'*) sse_done=yes ;; esac
if [ "$data_lines" -ge 2 ] && [ "$sse_done" = "yes" ]; then
  report PASS "chat 流式 SSE" "data 行=$data_lines 且含 [DONE]"
else
  report FAIL "chat 流式 SSE" "data 行=$data_lines，[DONE]=$sse_done"
fi

for page in providers keys usage compression resilience; do
  assert_eq "$(get_code "$BASE/dashboard/$page?key=$KEY")" "200" \
    "dashboard 页 /$page → 200"
done

assert_eq "$(get_code "$BASE/metrics")" "401" "GET /metrics 无 key → 401"
metrics_code=$(get_code "$BASE/metrics" "$KEY")
assert_eq "$metrics_code" "200" "GET /metrics 带 key → 200"
case "$(get_body "$BASE/metrics" "$KEY")" in
*omnifusion_requests_total*)
  report PASS "metrics 含 omnifusion_requests_total" ;;
*)
  echo "    (info) 指标族 omnifusion_requests_total 未出现（零样本时可能无输出，仅加分项）" ;;
esac

(cd "$WORKDIR" && ./ofd.exe status >status.out 2>&1)
status_rc=$?
assert_eq "$status_rc" "0" "ofd status 退出码 0"

# ---- 汇总 -------------------------------------------------------------------

echo "============================================================"
echo "冒烟结果：PASS=$PASS_COUNT FAIL=$FAIL_COUNT"
if [ "$FAIL_COUNT" -eq 0 ]; then
  echo "ALL PASS"
else
  echo "存在失败断言，详见上方 [FAIL] 行"
fi
[ "$FAIL_COUNT" -eq 0 ]
