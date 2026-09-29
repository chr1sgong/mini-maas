# D1 Runbook — k3s + GPU 接入 + vLLM 跑通 7B-AWQ（含 stream）

环境：i9-14th / 128G / RTX 4090 24G / Ubuntu 26.04（实测事实见 [env-facts.md](env-facts.md)）
日期：2026-09-22
验收：① test pod 内 `nvidia-smi` 可见；② OpenAI SDK 流式调用正常不断流

---

## ✅ Step 1 环境预检（结果见 env-facts.md）

nvidia-smi ✓ / nvidia-ctk 1.19.0 ✓ / kubectl ✓ / docker active ✓

## ✅ Step 2 安装 k3s

```bash
cd /<project_path>/mini-maas
bash scripts/10-install-k3s.sh          # 官方源慢则：K3S_CN_MIRROR=1
```

- 验收：`kubectl get node` 显示 Ready，版本 v1.3x
- [x] 完成，k3s 版本：v1.36.4+k3s1（节点 win11-pro，containerd 2.3.4）

## ✅ Step 3 GPU runtime 接入

```bash
bash scripts/11-gpu-runtime.sh
```

`nvidia-ctk`需要提前安装：

```bash
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
  | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
  | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
sudo apt-get update && sudo apt-get install -y nvidia-container-toolkit
nvidia-ctk --version   # 确认可用后再跑下面脚本
```

### 注意事项

前几次运行脚本可能会报timeout，但这不是脚本的原因，而是因为第一次拉取镜像耗时较长（尤其国内环境）。新机器预置 device-plugin 镜像（docker pull）

- 脚本背后做的事：基于 k3s 当前 containerd 配置生成 `config.toml.tmpl` → `nvidia-ctk` 注入 nvidia runtime 并设为默认 → 重启 k3s → 部署 device-plugin DaemonSet
- 验收：node allocatable 中出现 `nvidia.com/gpu: 1`

## ✅ Step 4 GPU 验证

```bash
bash scripts/12-verify-gpu.sh
```

- 脚本背后做的事：起一个 `limits: nvidia.com/gpu: 1` 的 pod 跑 `nvidia-smi`，打印出 4090 即通过
- 镜像拉取慢时的兜底：把 yaml 里 image 换成本机已有镜像（见文件内注释）
- [x] **验收①通过**

## ✅ Step 5 部署 vLLM（Qwen2.5-7B-Instruct-AWQ）

```bash
make 13-image          # docker pull vllm/vllm-openai:v0.30.0 后导入 k3s containerd（约 10GB+，最耗时的一步）
make 14-vllm           # kubectl apply deploy/manifests/vllm/vllm-qwen7b-awq.yaml  首次耗时10～20分钟，需要下载模型，编译cuda graph等
kubectl -n mini-maas logs -f deploy/vllm-qwen7b-awq   # 等待 "Application startup complete"
kubectl -n mini-maas get pod                          # Ready 1/1
```

YAML 关键决策（已写入文件注释）：

- `gpu-memory-utilization=0.85`；`HF_ENDPOINT=hf-mirror.com`；shm 8Gi；hostPath 缓存 `/data/mini-maas/hf-cache`；readiness `/health` 首次下载耗时长
  
- [x] 完成，镜像版本：docker镜像 -> vllm/vllm-openai:latest（digest sha256:d076d77d…，2026-09-22 拉取）vllm版本 -> v0.30.0，模型加载耗时：178.421231 seconds

## ✅ Step 6 流式冒烟（验收②）

```bash
pip install openai    # 或用 bench/requirements.txt
kubectl -n mini-maas port-forward svc/vllm-qwen7b-awq 8000:8000 &
make 15-smoke
```

- 通过判据：逐 chunk 打印（非整段返回），末尾输出 TTFT/chunks/total 且断言通过
- 记录：TTFT: 15 ms (需要预热)
- [x] **验收②通过**

---

## 踩坑记录

| 现象 | 原因 | 解法 |
|---|---|---|
| get.k3s.io 下载 k3s 二进制失败 | GitHub Releases 直连不稳（实测握手 3.2s vs 镜像 0.16s） | `K3S_CN_MIRROR=1` 走 rancher-mirror |
| 安装成功后脚本报 "no matching resources found" | `kubectl wait` 早于节点注册 | 已修脚本：先轮询等 node 对象出现，再等 Ready |

## 常见坑速查

| 症状 | 真实原因 | 解法 |
|---|---|---|
| k3s 起不来 | systemd 未开 | 本机已开，排除 |
| `nvidia.com/gpu` 不在 allocatable | tmpl 未生效 / device-plugin 异常 | 确认改的是 `config.toml.tmpl` 且已 restart k3s；查 `kubectl -n kube-system logs ds/nvidia-device-plugin-daemonset` |
| test pod 卡 ContainerCreating | nvcr.io 拉取慢 | 换本地镜像（yaml 注释） |
| vLLM 一直下模型 | HF 不通 | 确认 HF_ENDPOINT 生效；或手动下载到 /data/mini-maas/hf-cache |
| kubectl 连到 kind | kubeconfig 被替换 | 用备份文件 `export KUBECONFIG=~/.kube/config.bak.*` 切回 |
