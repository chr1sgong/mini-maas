#!/usr/bin/env bash
# 让 k3s 的 containerd 支持 NVIDIA GPU
# 前置：nvidia-container-toolkit 已安装（本机 nvidia-ctk 1.19.0 ✓），k3s 已运行
# control-plane 和 agent 节点都可以跑：自动识别 k3s / k3s-agent 服务，
# 只有 control-plane 会执行 device-plugin 部署和 GPU 验证
set -euo pipefail

TMPL_DIR=/var/lib/rancher/k3s/agent/etc/containerd

if systemctl is-active --quiet k3s; then
  SVC=k3s
elif systemctl is-active --quiet k3s-agent; then
  SVC=k3s-agent
else
  echo "FAIL: k3s / k3s-agent 都未运行"; exit 1
fi
echo "==> 检测到服务: $SVC"

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

# nvidia-ctk 的合并配置会丢弃 k3s 生成的 registry(certs.d) 段，
# 不补回的话 registries.yaml 里配的镜像加速全部失效
if ! grep -q 'agent/etc/containerd/certs.d' "$TMPL_DIR/config.toml.tmpl"; then
  sudo tee -a "$TMPL_DIR/config.toml.tmpl" >/dev/null <<'EOF'

  [plugins."io.containerd.cri.v1.images".registry]
    config_path = "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"
EOF
fi

sudo systemctl restart k3s

kubectl wait --for=condition=Ready node --all --timeout=180s

if [ "$SVC" != "k3s" ]; then
  echo "==> agent 节点完成（device-plugin 部署请在 control-plane 上执行）"
  exit 0
fi

# NVIDIA device plugin：清单已本地化（raw.githubusercontent.com 国内不可达）。
# 升级版本时重新下载 deploy/manifests/gpu-test/device-plugin.yaml
DP_MANIFEST="$(cd "$(dirname "$0")/.." && pwd)/deploy/manifests/gpu-test/device-plugin.yaml"
kubectl apply -f "$DP_MANIFEST"
kubectl -n kube-system rollout status daemonset/nvidia-device-plugin-daemonset --timeout=180s

echo "=== allocatable（应该看到 nvidia.com/gpu: > 1）==="
kubectl get node -o jsonpath='{.items[0].status.allocatable}{"\n"}'
kubectl get node -o jsonpath='{.items[0].status.allocatable}' | grep -q 'nvidia.com/gpu' \
  && echo "OK: GPU 已可分配" \
  || { echo "FAIL: allocatable 中没有 nvidia.com/gpu，检查 device-plugin pod 日志"; exit 1; }
