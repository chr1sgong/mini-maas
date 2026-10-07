#!/usr/bin/env bash
# 安装 k3s 单节点
# 用法：bash scripts/10-install-k3s.sh
# K3S_CN_MIRROR=1 bash scripts/10-install-k3s.sh   # 官方源慢时用国内镜像
set -euo pipefail

if systemctl is-active --quiet k3s; then
  echo "k3s 已在运行，跳过安装"
  exit 0
fi

# 本机已有 kind 集群的 kubeconfig，先备份再替换（合并不易出错，备份最稳）
if [ -f ~/.kube/config ]; then
  bak=~/.kube/config.bak.$(date +%Y%m%d%H%M%S)
  cp ~/.kube/config "$bak"
  echo "已备份旧 kubeconfig（kind-kind）到 $bak"
  echo "如需临时切回 kind: export KUBECONFIG=$bak"
fi

# 镜像加速：registry.k8s.io / nvcr.io 等官方仓库国内云主机不可达，
# k3s 安装前写好，containerd 首次启动即生效（agent 节点同样适用）
sudo mkdir -p /etc/rancher/k3s
sudo tee /etc/rancher/k3s/registries.yaml >/dev/null <<'EOF'
mirrors:
  docker.io:
    endpoint:
      - "https://docker.m.daocloud.io"
  registry.k8s.io:
    endpoint:
      - "https://k8s.m.daocloud.io"
  nvcr.io:
    endpoint:
      - "https://nvcr.m.daocloud.io"
  quay.io:
    endpoint:
      - "https://quay.m.daocloud.io"
  gcr.io:
    endpoint:
      - "https://gcr.m.daocloud.io"
EOF

# --disable traefik：网关自研，不需要内置 ingress
INSTALL_EXEC="server --write-kubeconfig-mode 644 --disable traefik"
if [ "${K3S_CN_MIRROR:-0}" = "1" ]; then
  curl -sfL https://rancher-mirror.rancher.cn/k3s/k3s-install.sh | INSTALL_K3S_MIRROR=cn INSTALL_K3S_EXEC="$INSTALL_EXEC" sh -
else
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="$INSTALL_EXEC" sh -
fi

mkdir -p ~/.kube
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown "$USER":"$USER" ~/.kube/config

kubectl wait --for=condition=Ready node --all --timeout=180s
kubectl get node -o wide
echo "OK: k3s 就绪"
