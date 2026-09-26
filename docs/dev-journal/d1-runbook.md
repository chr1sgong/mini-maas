# D1 Runbook — k3s + GPU 接入 + vLLM 跑通 7B-AWQ（含 stream）

环境：i9-14th / 128G / RTX 4090 24G / WSL2 Ubuntu 24.04（实测事实见 [env-facts.md](env-facts.md)）
日期：2026-09-22
验收：① test pod 内 `nvidia-smi` 可见；② OpenAI SDK 流式调用正常不断流
策略：**逐层验证、小步快跑**，任何一步不过不往下走。

---

## ✅ Step 1 环境预检（结果见 env-facts.md）

- systemd ✓ / nvidia-smi ✓ / nvidia-ctk 1.19.0 ✓ / kubectl ✓ / docker active ✓

## ✅ Step 2 安装 k3s

```bash
cd /${path}/mini-maas
bash scripts/10-install-k3s.sh          # 官方源慢则：K3S_CN_MIRROR=1 bash scripts/d1/10-install-k3s.sh
```

- 验收：`kubectl get node` 显示 Ready，版本 v1.3x
- [x] 完成，k3s 版本：v1.36.4+k3s1（节点 win11-pro，containerd 2.3.4）

## ✅ Step 3 GPU runtime 接入

```bash
bash scripts/11-gpu-runtime.sh
```

- 脚本背后做的事：基于 k3s 当前 containerd 配置生成 `config.toml.tmpl` → `nvidia-ctk` 注入 nvidia runtime 并设为默认 → 重启 k3s → 部署 device-plugin DaemonSet
- 验收：node allocatable 中出现 `nvidia.com/gpu: 1`
- [x] 完成

## ✅ Step 4 GPU 验证

```bash
bash scripts/12-verify-gpu.sh
```

- 脚本背后做的事：起一个 `limits: nvidia.com/gpu: 1` 的 pod 跑 `nvidia-smi`，打印出 4090 即通过
- 镜像拉取慢时的兜底：把 yaml 里 image 换成本机已有镜像（见文件内注释）
- [x] **验收①通过**

## ✅ Step 5 部署 vLLM（Qwen2.5-7B-Instruct-AWQ）

```bash
make d1-image          # docker pull vllm/vllm-openai:latest 后导入 k3s containerd（约 10GB+，最耗时的一步）
make d1-vllm           # kubectl apply deploy/manifests/vllm/vllm-qwen7b-awq.yaml
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
python3 scripts/smoke_stream.py
```

- 通过判据：逐 chunk 打印（非整段返回），末尾输出 TTFT/chunks/total 且断言通过
- 记录：TTFT 288 ms
- [x] **验收②通过 → D1 收工**

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
| vLLM OOM | 显存被占（vllm-dev 未停 / Windows 占用） | `docker stop vllm-dev`；调低 gpu-memory-utilization |
| kubectl 连到 kind | kubeconfig 被替换 | 用备份文件 `export KUBECONFIG=~/.kube/config.bak.*` 切回 |
| Windows 浏览器访问不到 | ClusterIP 不透出 WSL | port-forward 或 NodePort |
