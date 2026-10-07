# mini-maas

在 Kubernetes（k3s）上自建的迷你 **Model-as-a-Service** 平台：以 OpenAI 兼容协议对外提供多模型推理服务，覆盖「模型接入 → 实例调度 → 流式推理 → 多租户计量限流 → 指标观测 → 弹性伸缩 → 压测实验归档」的完整链路。适合对maas产品系统架构进行入门学习的用户.

## 产品功能

平台由四个核心组件构成：

- **Operator**（`operator/`，kubebuilder v4 脚手架）
  自定义 `Model` CRD（`maas.chr1sgong.io/v1alpha1`）。在 CR 中声明模型地址（`hf://` artifactURI）、推理引擎（vLLM）及引擎参数（maxModelLen / maxNumSeqs / enablePrefixCaching）、副本数与显存占用比例；reconcile 后自动生成并管理 vLLM 的 Deployment/Service，回填 `status.phase`（Pending/Creating/Ready/Failed）和实例级 `status.endpoints`。

- **Gateway**（`gateway/`，Go，监听 `:8080`）
  OpenAI 兼容的多租户推理网关：
  - 按请求体 `model` 字段路由，watch Model CR status 实时维护「模型 → endpoints」路由表；
  - SSE 流式透传，客户端断连沿 context 传播到上游；
  - API key 鉴权（key → 租户/可用模型/配额映射）；
  - QPS + TPM 双维令牌桶限流（TPM 采用「预检扣估算、旁路解析 usage 结算退款」模型，超限返回 `Retry-After`）；
  - 三种实例调度策略：`round_robin` / `least_inflight` / `prefix_affinity`（rendezvous 哈希亲和 + 过载降级，配合 vLLM prefix cache 提命中率），路由决策通过 `X-MaaS-Endpoint` / `X-MaaS-Route-Reason` 响应头暴露；
  - 暴露 `/metrics`（`maas_gateway_*` 系列 Prometheus 指标）。

- **Autoscaler**（`operator/cmd/autoscaler`）
  从 Prometheus 读取 vLLM 队列指标（`num_requests_waiting/running`），规则式步进决策（±1 副本、防抖、冷却期），merge patch Model CR 的 `spec.serving.replicas`，闭环「指标 → 决策 → 改声明 → 容量变化」。

- **可观测性与实验管线**
  Prometheus + Grafana + dcgm-exporter（GPU 指标）plain YAML 部署；`bench/bench.py` 压测器（steady 闭环 / burst 开环两种流量模型，TTFT/TPOT/吞吐精确口径）；`scripts/export_run.py` + `render_charts.py` 按 PromQL 清单（`scripts/queries.yaml`）从 Prometheus 导出实验数据并渲染报告图，实验按规范归档于 `docs/experiments/`。

## 已完成能力

- ✅ **GPU 推理底座**：k3s + NVIDIA GPU runtime（nvidia-container-toolkit 注入 containerd + device-plugin），vLLM v0.30.0 部署 Qwen2.5-7B-Instruct-AWQ，OpenAI SDK 流式冒烟通过（预热后 TTFT ≈15ms）。
- ✅ **压测器与基线**：`bench.py` 指标口径定稿（TTFT/TPOT/吞吐，steady/burst 双流量模型），完成单实例基线矩阵（成功率 100%）：c=1 吞吐 ≈145 tok/s；c=16 ≈1859 tok/s、TTFT P50 ≈49ms；c=64 吞吐饱和 ≈3021 tok/s（详见 `docs/baseline/`）。
- ✅ **Model CRD 与 Operator**：CRD 契约定稿（校验/默认值/printer columns/status subresource）；reconcile 落地——apply CR 自动建 Deployment/Service、status/endpoints 回填，幂等、级联删除、热更新、自愈均验收通过。
- ✅ **单卡多模型**：单卡上 7B-AWQ + 3B-FP16 双实例（gpuMemoryUtilization 0.45+0.45），device-plugin time-slicing 供给 2 份 GPU。
- ✅ **网关 v1**：SSE 流式透传、路由表 watch Model CR、`/v1/models` 聚合，TTFT 网关税 < 5ms。
- ✅ **多租户**：API key 鉴权 + QPS/TPM 双维限流（含退款结算、include_usage 旁路注入、Retry-After）。
- ✅ **调度策略**：round_robin / least_inflight / prefix_affinity 三种 picker + 路由决策响应头，2 副本下 RR/least-inflight 分布验证通过。
- ✅ **可观测性**：Prometheus 抓取 vLLM 与网关指标，Grafana 六行面板（Traffic/Latency/Throughput/Routing/Saturation/GPU），压测进行中实时可见。
- ✅ **压测-看板闭环**：bench / 网关 / 引擎三视角计数精确对账；单次压测数据 10 分钟内可导出成报告。
- ✅ **弹性伸缩**：自研 autoscaler 接管副本数——突发流量扩容 t1−T0 ≤ 90s，静默 5 分钟自动缩容（数据见 `docs/experiments/autoscaler-baseline`）。
- ✅ **多节点复制**：2 台双卡 4090 云主机组网（k3s server+agent），整套 manifests 原样 apply 成功；7B 扩 4 副本跨节点调度 + 网关跨节点路由分布验证通过（`docs/experiments/20261004-multinode`）。

## 仓库结构

```bash
operator/    Model CRD + controller + autoscaler（kubebuilder，独立 go module）
gateway/     OpenAI 兼容多租户网关（独立 go module）
deploy/      manifests（vllm / gpu-test / observability）与 Model CR 样例
bench/       bench.py 压测器
scripts/     环境搭建、压测、导出、渲染脚本
tests/e2e/   网关 e2e 测试（pytest）
docs/        baseline / designs / experiments 归档
```

## Quick Start

### 环境

> 以下所列环境是当前开发版本, 其他软硬件软硬件版本暂未做测试

| 项目 | 信息 | 本项目验证环境 |
|---|---|---|
| 硬件 | x86_64 + NVIDIA GPU（显存按模型而定） | RTX 4090 24G 或者Nvidia其他同显存(L4)或者规格更高的显卡, 暂不支持其他家显卡 |
| OS | 裸金属 Linux（systemd）；**不建议 WSL2**（显存虚拟化导致超卖时静默换页，性能数据不可用） | Ubuntu 22.04 / 24.04 |
| NVIDIA 驱动 | 已安装并可 `nvidia-smi` | driver 595.91.07 / CUDA 13.2 |
| nvidia-container-toolkit | ≥ 1.19（`nvidia-ctk` 可用） | — |
| Docker | 用于拉取并导入镜像到 k3s | — |
| kubectl | 已安装 | — |
| Go | ≥ 1.26（编译 gateway / operator / autoscaler） | Go 1.27.1 |
| Python | ≥ 3.12 + [uv](https://docs.astral.sh/uv/)（bench 与脚本） | Python 3.12 |
| 网络 | 可访问 HuggingFace，或配置镜像（清单默认 `HF_ENDPOINT=hf-mirror.com`，k3s 已预配国内镜像加速） | — |

### 0. 项目准备

```bash
git clone https://github.com/chr1sgong/mini-maas.git  # clone repo
cd mini-maas
uv sync                                               # 使用uv管理python环境
```

### 1. 搭建集群与 GPU 运行时

```bash
make 10-k3s            # 安装单节点 k3s（--disable traefik，预配镜像加速）
make 11-gpu-runtime    # nvidia-ctk 给 containerd 注入 nvidia runtime + 部署 device-plugin
make 12-verify-gpu     # 起 pod 跑 nvidia-smi 验证
```

### 2. 部署第一个模型（两条路径任选）

**手工路径**（快速体验）：

```bash
make 13-image          # 拉取 vllm/vllm-openai:v0.30.0 并导入 k3s
make 14-vllm           # kubectl apply deploy/manifests/vllm/vllm-qwen2.5-7b-awq.yaml
kubectl -n mini-maas port-forward pod/<pod-name> 8080:8000 # 通过端口转发把k3s的pod暴露出来, forwarding from 127.0.0.1:8080 -> pod:8000
make 15-smoke          # OpenAI SDK 流式冒烟
```

**Operator 路径**（声明式管理）：

```bash
cd operator
make install           # 安装 Model CRD
make run               # 本地运行 controller（使用当前 kubeconfig）
kubectl apply -f ../deploy/manifests/bootstrap/namespace.yaml && kubectl apply -f ../deploy/manifests/models/qwen2.5-7b-awq.yaml   # 注册模型，Operator自动建 Deployment/Service
kubectl -n mini-maas get models  # 观察 Phase → Ready
kubectl -n mini-maas get svc   # 确保Operator拉起了Service
kubectl -n mini-maas port-forward svc/<svc-name> 8080:8000 # 通过端口转发把k3s的pod暴露出来, forwarding from 127.0.0.1:8080 -> pod:8000. Operator实现了Service, 这里通过svc实现端口转发
make 15-smoke          # OpenAI SDK 流式冒烟
```

> 首次启动需下载模型权重并编译 CUDA graph，约 60s 起（模型未缓存时更久）。

### 3. 启动网关

```bash
cd gateway
go run ./cmd/gateway -config config.json -kubeconfig ~/.kube/config   # 监听 :8080，watch mini-maas namespace 的 Mode CR. 如果通过, ~/.kube/config是通过本地admin访问的,实际生产环境需要注意权限问题
```

调用（OpenAI 兼容，API key 见 `gateway/config.json` 的 `keys` 段）：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <api-key>" -H "Content-Type: application/json" \
  -d '{"model": "qwen2.5-7b-awq", "messages": [{"role": "user", "content": "hello"}], "stream": true}'
```

网关 e2e 测试（pytest，需先用 `gateway/gateway.e2e.json` 启动网关）：

```bash
cd tests/e2e && ../../.venv/bin/python -m pytest
```

### 4. 可观测性（可选）

```bash
kubectl apply -f deploy/manifests/observability/   # Prometheus + Grafana + dcgm-exporter
```

### 5. Autoscaler（可选）

```bash
cd operator
go run ./cmd/autoscaler --dry-run -kubeconfig ~/.kube/config   # 先 dry-run 校准，再去掉 --dry-run 接管 replicas, autoscaler也需要感知集群副本, 与routing类似
```

### 6. 压测与实验

```bash
make 20-baseline       # steady/burst 基线矩阵，结果写 bench/results/ 与 docs/baseline/
bash scripts/base-observability.sh            # 走网关压测并打印导出窗口
python3 scripts/export_run.py ...    # 从 Prometheus 导出 CSV/summary.json
python3 scripts/render_charts.py ... # 渲染报告图
```

## 文档索引

- 基线数据：`docs/baseline/`
- 设计笔记：`docs/designs/`
- 实验归档：`docs/experiments/`
