#!/usr/bin/env bash
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
echo "$REPO_ROOT"

PY="$REPO_ROOT/.venv/bin/python"

if [ ! -x "$PY" ]; then
    echo "Python executable not found: $PY"
    exit 1
fi

# 确保vLLM能调通
if ! curl -sf --max-time 3 http://localhost:8000/v1/models > /dev/null 2>&1; then
    echo "启动 port-forward..."
    kubectl -n mini-maas port-forward svc/vllm-qwen7b-awq 8000:8000 &
    PF=$!
    trap 'kill $PF 2>/dev/null || true' EXIT
    sleep 3
fi

cd "$(dirname "$0")/.."

# client 与 server 同一台机器测试，限制client的核数，避免资源争抢导致性能波动
TASKSET=""
command -v taskset > /dev/null && TASKSET="taskset -c 0-7"

for c in 1 4 8 16 24 32 40 48 56 64; do
    echo "===== steady mode: c=$c ====="
    $TASKSET $PY bench/bench.py --mode steady --concurrency $c --duration 60 --out "bench/results/steady-c$c.jsonl" --md docs/baseline/baseline.md
done

for bs in 1 8 16 24 32 40 48 56 64 72 80 88 96 104 112 120 128; do
    echo "====== burst $bs ======"
    $TASKSET $PY bench/bench.py --mode burst --burst-size $bs  --duration 60 --out "bench/results/burst-$bs.jsonl" --md docs/baseline/burst-baseline.md
done

echo "Baseline Testing Finished Succeeded!!!"