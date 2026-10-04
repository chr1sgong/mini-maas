#!/usr/bin/env bash
# D8 Step7-D 验收脚本: prefix 亲和调度
# 对应 dev-journal/d8-runbook.md Step 7 D 的四项验收
#
# 用法:
#   ./scripts/d8-verify-prefix-affinity.sh                # 跑 D1-D3 (纯请求侧)
#   INCLUDE_SCALING=1 ./scripts/d8-verify-prefix-affinity.sh  # 额外跑 D4 (改 replicas, 需要 kubectl)
#
# 前置: 网关已配置 strategy=prefix_affinity, 模型已有 2 个就绪 endpoint
# 可用环境变量: BASE_URL API_KEY MODEL CAP(=max_affinity_inflight, 默认 8)

set -u

BASE_URL=${BASE_URL:-http://localhost:8080}
API_KEY=${API_KEY:-sk-tenant-a-EXAMPLE}
MODEL=${MODEL:-qwen2.5-7b-awq}
CAP=${CAP:-8}                       # 网关 routing.max_affinity_inflight
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  [PASS] $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  [FAIL] $1"; }
head() { echo; echo "== $1 =="; }

# 发一条请求, 输出 "endpoint<TAB>reason" 到 stdout
req() { # req <json-body>
  # 注意: 并发子 shell 里 $$ 仍是父进程 PID, header 文件必须每次独立, 否则互相污染
  local hf ep reason
  hf=$(mktemp "$TMPDIR/h.XXXXXX")
  curl -s -D "$hf" -o /dev/null "$BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $API_KEY" \
    -H 'Content-Type: application/json' \
    -d "$1" || true
  ep=$(tr -d '\r' < "$hf" | grep -i '^X-Maas-Endpoint:'    | head -1 | cut -d' ' -f2-)
  reason=$(tr -d '\r' < "$hf" | grep -i '^X-Maas-Route-Reason:' | head -1 | cut -d' ' -f2-)
  rm -f "$hf"
  printf '%s\t%s\n' "${ep:-NO_ENDPOINT}" "${reason:-NO_REASON}"
}

# 构造请求体: $1=system内容 $2=user内容 $3=max_tokens(可空)
body() {
  printf '{"model":"%s","messages":[{"role":"system","content":"%s"},{"role":"user","content":"%s"}]%s}' \
    "$MODEL" "$1" "$2" "${3:+, \"max_tokens\": $3}"
}

# >=300 字的共享 system prompt (前 256 字节在 affinity 窗口内)
SHARED_SYS=$(printf '你是一个严谨的写作助手，回答前先列提纲，再分段展开，每段至少三句，避免口语化表达。%.0s' $(seq 1 10))
[ ${#SHARED_SYS} -ge 300 ] || { echo "shared prompt 不足 300 字"; exit 1; }

head "D1: 同 system prompt x 10 顺序请求 -> 全部同端, Reason=affinity"
D1="$TMPDIR/d1"; : > "$D1"
for i in $(seq 10); do req "$(body "$SHARED_SYS" "第${i}次: 写一段关于秋天的短文")" >> "$D1"; done
sort "$D1" | uniq -c | sed 's/^/    /'
ep_n=$(cut -f1 "$D1" | sort -u | wc -l)
aff_n=$(awk -F'\t' '$2=="affinity"' "$D1" | wc -l)
[ "$ep_n" -eq 1 ] && ok "10 条全部落在同一端" || bad "落在 ${ep_n} 个端, 期望 1"
[ "$aff_n" -eq 10 ] && ok "Reason 全部为 affinity (${aff_n}/10)" || bad "affinity 仅 ${aff_n}/10"

head "D2: 20 个不同前缀各 1 条 -> 分散到两端; 每个前缀重复 3 次稳定同端"
D2="$TMPDIR/d2"; : > "$D2"
declare -A FIRST_EP
for p in $(seq -w 20); do
  # 差异点放在前 256 字节内 (前缀编号紧跟开头)
  prefix="PREFIX-${p}-$(printf 'x%.0s' $(seq 1 350))"
  ep=$(req "$(body "$prefix" "用一句话总结你自己")" | cut -f1)
  echo "$ep" >> "$D2"
  FIRST_EP[$p]=$ep
done
distinct=$(sort -u "$D2" | wc -l)
[ "$distinct" -ge 2 ] && ok "20 个前缀分散到 ${distinct} 个端" || bad "20 个前缀只落到 ${distinct} 个端"
STABLE=0
for p in $(seq -w 20); do
  prefix="PREFIX-${p}-$(printf 'x%.0s' $(seq 1 350))"
  for r in 1 2 3; do
    ep=$(req "$(body "$prefix" "重复${r}: 用一句话总结你自己")" | cut -f1)
    [ "$ep" = "${FIRST_EP[$p]}" ] && STABLE=$((STABLE+1)) || \
      echo "    [漂移] 前缀 $p 第 ${r} 次: ${FIRST_EP[$p]} -> $ep"
  done
done
[ "$STABLE" -eq 60 ] && ok "60 次重复全部稳定同端" || bad "稳定 ${STABLE}/60"

head "D3: 并发 $((CAP+4)) 条同前缀长生成 (cap=$CAP) -> 出现 affinity-fallback 且不堆积单端"
D3="$TMPDIR/d3"; : > "$D3"
pids=()
for i in $(seq $((CAP+4))); do
  ( req "$(body "$SHARED_SYS" "并发${i}: 详细论述短视频对青少年注意力的影响" 800)" >> "$D3" ) &
  pids+=($!)
done
wait "${pids[@]}"
sort "$D3" | uniq -c | sed 's/^/    /'
fb=$(awk -F'\t' '$2=="affinity-fallback"' "$D3" | wc -l)
maxshare=$(cut -f1 "$D3" | sort | uniq -c | awk '{print $1}' | sort -n | tail -1)
[ "$fb" -ge 1 ] && ok "出现 ${fb} 条 affinity-fallback (cap=$CAP)" || bad "未出现 affinity-fallback, 并发=${CAP+4}"
[ "$maxshare" -lt $((CAP+4)) ] && ok "无单端打满 (最多一端 ${maxshare}/$((CAP+4)))" || bad "全部 ${maxshare} 条堆积在单端"

if [ "${INCLUDE_SCALING:-0}" = "1" ]; then
  head "D4: replicas 2->3 -> 部分前缀迁移而非全部"
  command -v kubectl >/dev/null || { echo "  [SKIP] 需要 kubectl"; }
  kubectl -n mini-maas patch model "${MODEL}" --type merge -p '{"spec":{"serving":{"replicas":3}}}'
  for _ in $(seq 1 60); do
    n=$(kubectl -n mini-maas get model "$MODEL" -o jsonpath='{.status.endpoints}' 2>/dev/null | grep -o 'http' | wc -l)
    [ "$n" -ge 3 ] && break; sleep 5
  done
  echo "    当前 endpoint 数: $n"
  MOVED=0; KEPT=0
  for p in $(seq -w 20); do
    prefix="PREFIX-${p}-$(printf 'x%.0s' $(seq 1 350))"
    ep=$(req "$(body "$prefix" "扩容后: 用一句话总结你自己")" | cut -f1)
    if [ "$ep" = "${FIRST_EP[$p]}" ]; then KEPT=$((KEPT+1)); else MOVED=$((MOVED+1)); fi
  done
  echo "    迁移 ${MOVED}/20, 保持 ${KEPT}/20"
  [ "$MOVED" -ge 1 ] && [ "$KEPT" -ge 1 ] && ok "部分迁移 (${MOVED} 迁 / ${KEPT} 留)" \
    || bad "期望部分迁移, 实际 迁=${MOVED} 留=${KEPT}"
  kubectl -n mini-maas patch model "${MODEL}" --type merge -p '{"spec":{"serving":{"replicas":2}}}' >/dev/null
  echo "    (已恢复 replicas=2)"
fi

echo; echo "== 汇总: PASS=${PASS} FAIL=${FAIL} =="
[ "$FAIL" -eq 0 ]
