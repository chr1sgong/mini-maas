#!/usr/bin/env bash
# D1 Step 4 / 验收①：起一个申请 GPU 的 test pod，容器内跑 nvidia-smi
set -euo pipefail

MANIFEST="$(cd "$(dirname "$0")/.." && pwd)/deploy/manifests/gpu-test/nvidia-smi-pod.yaml"

kubectl delete pod nvidia-smi --ignore-not-found
kubectl apply -f "$MANIFEST"

phase=""
for _ in $(seq 1 60); do
  phase=$(kubectl get pod nvidia-smi -o jsonpath='{.status.phase}' 2>/dev/null || echo Pending)
  [ "$phase" = "Succeeded" ] && break
  if [ "$phase" = "Failed" ]; then
    echo "FAIL: pod 失败"; kubectl describe pod nvidia-smi | tail -20; exit 1
  fi
  sleep 5
done

if [ "$phase" != "Succeeded" ]; then
  echo "FAIL: 超时（5 分钟未完成，大概率是镜像拉取慢）"
  kubectl describe pod nvidia-smi | tail -20
  exit 1
fi

echo "=== test pod 内的 nvidia-smi 输出 ==="
kubectl logs nvidia-smi
echo "OK: D1 验收①通过（GPU 在 k3s 容器内可见）"
