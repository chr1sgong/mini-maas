#!/usr/bin/env bash
# 让 k3s 的 containerd 支持 NVIDIA GPU
# 前置：nvidia-container-toolkit 已安装（本机 nvidia-ctk 1.19.0 ✓），k3s 已运行
set -euo pipefail

TMPL_DIR=/var/lib/rancher/k3s/agent/etc/containerd

# k3s 只认 config.toml.tmpl：以当前生效配置为底，注入 nvidia runtime 并设为默认
sudo mkdir -p "$TMPL_DIR"
if [ ! -f "$TMPL_DIR/config.toml.tmpl" ]; then
  sudo cp "$TMPL_DIR/config.toml" "$TMPL_DIR/config.toml.tmpl"
fi
sudo nvidia-ctk runtime configure --runtime=containerd \
  --config="$TMPL_DIR/config.toml.tmpl" --set-as-default

# nvidia-ctk 1.19 + containerd 2.x（config version 3）走 drop-in 模式，
# 输出写到 /etc/containerd/conf.d/99-nvidia.toml 而非 --config 指定的文件，
# 且 k3s 不读该路径。该文件是以 tmpl 为底的完整合并配置，直接回写为 tmpl。
if [ -f /etc/containerd/conf.d/99-nvidia.toml ]; then
  sudo cp /etc/containerd/conf.d/99-nvidia.toml "$TMPL_DIR/config.toml.tmpl"
fi

sudo systemctl restart k3s

kubectl wait --for=condition=Ready node --all --timeout=180s

# NVIDIA device plugin：清单已本地化（raw.githubusercontent.com 国内不可达）。
# 升级版本时重新下载 deploy/manifests/gpu-test/device-plugin.yaml
DP_MANIFEST="$(cd "$(dirname "$0")/.." && pwd)/deploy/manifests/gpu-test/device-plugin.yaml"
kubectl apply -f "$DP_MANIFEST"
kubectl -n kube-system rollout status daemonset/nvidia-device-plugin-daemonset --timeout=180s

echo "=== allocatable（应看到 nvidia.com/gpu: 1）==="
kubectl get node -o jsonpath='{.items[0].status.allocatable}{"\n"}'
kubectl get node -o jsonpath='{.items[0].status.allocatable}' | grep -q 'nvidia.com/gpu' \
  && echo "OK: GPU 已可分配" \
  || { echo "FAIL: allocatable 中没有 nvidia.com/gpu，检查 device-plugin pod 日志"; exit 1; }
