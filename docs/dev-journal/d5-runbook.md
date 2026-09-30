# D5 Runbook — 多模型共卡（7B + 3B）：控制面检查点

验收：apply 后 60s 内 endpoint 就绪（口径见 Step 5，**未通过则降级 informer 方案**）

- reconcile 是不是真的没有单模型假设（两个 CR 无差别工作）；
- 能不能把时间（60s）和空间（显存/内存/GPU 供给）四本账算清楚。代码改动应该很少，账算不对才会卡。

---

## Step 0 基础概念

### 0.1 显存空间 —— gpu_memory_utilization 只有一张卡，需要应用层确保全部值加起来 <= 1

4090 无 MIG，两个 vLLM 进程是**时间片复用同一张卡的 SM**，不是各占一半。`--gpu-memory-utilization=0.45` 只是告诉每个实例"你的显存预算是 24564MiB × 0.45 ≈ 11GiB"，预算内再切：权重（7B-AWQ ≈ 5.5G / 3B-FP16 ≈ 6.2G）+ 激活 + CUDA graph + **剩下的零头全给 KV cache**。

必须实测回答的问题：**第二个实例启动时，vLLM 的显存 profiling 看到的是一张"已被占 11G"的卡**。两个实例先后起，全程看`nvidia-smi`，记录结果。

### 0.2 GPU 供给 —— 调度器只认 device plugin 的广播

env-facts 实测：节点 allocatable `nvidia.com/gpu: 1`。两个 Pod 各 `limits: nvidia.com/gpu: 1` → **第二个 Pod 永远 Pending**，事件里只有一句 `Insufficient nvidia.com/gpu`。这不是 Operator 的 bug，也不是 CR 的问题，是 device plugin 的供给口径只报了物理卡数。

解法方向：NVIDIA device plugin 的 **time-slicing** 配置（ConfigMap 声明 replicas 数，advertise 出 N 份虚拟 GPU）。想清楚它的本质：**这是谎报供给**——调度器被说服"有 2 张卡"，物理上仍是 1 张，不超卖的保证完全由你的显存账本（0.45+0.45≤0.90）和自尊心来兜底。 replicas 设多少、为什么不设 4，Step 2 回答。

### 0.4 时间 60s 的预算分解

60s 检查点验收的是**整条链路的总时延**，但每段的"可谈判性"完全不同：

| 阶段 | 典型量级 | 可压缩性 | 测量手段 |
|---|---|---|---|
| apply → Deployment 创建 | 亚秒 | reconcile 本身，已验证 | `kubectl get events -w` 时间戳 |
| Pod 调度 | 亚秒~秒 | 若 Pending，先看是不是 0.2 节的 GPU 供给问题 | events / `get pod -w` |
| 镜像拉取 | 0s | IfNotPresent + 已导入，应为 0；不是 0 说明镜像策略错了 | events |
| vLLM 进程 boot → 权重 load | 十秒级 | 权重已在 hf-cache；3B 首次要下载 ~6GB，**先预热再计时** | vLLM 日志时间戳 |
| torch 编译 + CUDA graph capture | **最大头，十秒~分钟级** | 见下面两个杠杆 | vLLM 日志 |
| readiness 探测延迟 | ≤ periodSeconds | **当前 periodSeconds=10，天然贡献最多 10s** | `get pod -w` Ready 跳变 |
| endpoints 回填 status | 亚秒~秒 | Owns 触发的 reconcile，D4 已验证 | `get model -o jsonpath -w` |

两个重点：

1. **vLLM 编译缓存的持久性**：v0.30（V1 引擎）默认 torch.compile + CUDA graph capture，这笔开销的缓存落在 `~/.cache/vllm`。 当前 Pod 只挂了 `hf-cache`（`/root/.cache/huggingface`），编译缓存随 Pod 销毁——**每次重建都需要重复编译一边**。要不要给它也挂一个 hostPath降低编译时间？
2. **`--enforce-eager`**：跳过编译与 graph capture，启动最快，但 baseline 已量化过 CUDA graph 对吞吐的收益——不要开它，会影响后续实验数据口径。

---

## Step 1 预检

```bash
kubectl get crd models.maas.chr1sgong.io                    # CRD 在
kubectl -n mini-maas get model,deploy,svc,pods              # 7B 链路
kubectl describe node <node-name> | grep -A6 Allocatable    # nvidia.com/gpu: 1
nvidia-smi                                                  # vllm-dev 容器是否还停着（env-facts：共卡前先 stop）
ls /data/mini-maas/hf-cache/hub 2>/dev/null                 # 7B-AWQ 已在；3B 还没有，待下载 ~6GB
```

* [x] 完成，确认 7B 单模型链路仍是 Ready 状态

## Step 2 账本定稿（今天 60% 的学习量在这里）

写代码前填完这张表：

| 决策点 | 你的选择 | 理由 / 不这么选的后果 |
|---|---|---|
| time-slicing replicas 设几份？为什么不设 4？ | 2 份 | 4090 显存 24G：7B-AWQ 权重 ~5.5G + 3B 权重 ~6G；切 4 份每份不足 6G，加 KV cache / CUDA graph 开销不够用。补充：谎报份数应匹配计划共驻实例数——每多谎报一份，调度器就可能把计划外 GPU Pod 排上来，显存兜底责任重一分。另注意：replicas=2 意味着 D11 扩 7B 第 2 副本时供给再次卡死（思考题 5 实景） |
| 两个实例的 gpuMemoryUtilization 分配 | 0.45 + 0.45 | 3B 未量化，权重 ~6.2G 反比 AWQ 版 7B 大，不该少拿；0.45+0.45=0.90，留 0.10 给系统与其他 GPU 进程 |
| 3B 的 maxModelLen / maxNumSeqs 照不照搬 7B？ | 照搬 | 3B KV 头只有 2（7B 为 4），单位 token KV 36KiB vs 56KiB；0.45 预算下容量 ~96k vs ~71k tokens，同一量级——照搬让 D13 双模型对照少一个变量 |
| vLLM 编译缓存挂不挂持久卷？ | **挂**：hostPath `/data/mini-maas/vllm-cache` → `/root/.cache/vllm`，两模型共享根目录（hash 目录天然隔离） | torch.compile 产物按"模型+引擎配置+版本"hash 缓存，命中后编译从几十秒→秒级；Deployment 是 Recreate 策略，不挂则每次重建都冷编译。注意：D13 每扫一组新配置首次启动必冷，冷/温分开计。**代码改动点：buildDeployment 需加第三个 volume** |
| 两个 CR 同时 apply，串行化还是放任？ | 串行（操作纪律：先 7B Ready 再 apply 3B） | 并行是否安全取决于 vLLM profiling 预算口径（0.1 节待实测）；K8s 层面无法保证两个独立调度的 Pod 串行，真正的并行场景在 D11 动态扩缩 |
| 60s 硬线的达标策略 | 默认参数先冲，实测分解后按序调整：**① probe periodSeconds 10→2 → ② 编译缓存持久卷 → ③ 引擎参数（enforce-eager，动它=承认 60s 比性能重要，须书面记录取舍）** | 乱序调整的后果：先动引擎参数会毁掉 D2 baseline 口径，后面的优化全部失去对照 |
| 降级判据 | **控制面失败才降级**：apply→Deployment 创建耗时异常、reconcile 不触发/逻辑修不动、status 回填链路无解、D4 五连回归不可修；vLLM 引擎/资源问题一律算调优不算降级 | 三行方案：① informer watch Model CR，Add/Update 入本地队列；② clientset 直创/更 Deployment/Service（复用 D4 build 纯函数）；③ 二级 informer watch Deployment status，就绪回填 status.endpoints |


必答的个问题：

1. **降级 informer 方案具体退掉什么？** 写三行：informer watch Model CR + 直调 clientset + 轮询 Deployment 状态回填。逐项列出你放弃了 controller-runtime 的什么（workqueue 指数退避 / Owns 反向触发 / 缓存一致性 / leader election 的壳），以及哪些 D4 代码可以原样搬过去（buildDeployment/buildService 是纯函数，应该一行不改——如果不是，说明 D4 的函数边界没划干净）。


## Step 3 解决 GPU 供给（今天唯一的"基础设施"改动）

1. 给 device plugin 配 time-slicing（ConfigMap + 重启 DaemonSet，具体写法自查 v0.17.3 文档）。
2. 验证广播变了：`kubectl describe node <node-name> | grep nvidia.com/gpu`，Capacity 与 Allocatable 都应变成配置的份数。
3. 验证"谎报"的边界：两个 Pod 各 request 1 份能调度上

* [x] 完成，node 广播份数已变； ConfigMap 存进 `deploy/manifests/gpu-test/`

## Step 4 第二个模型上线

1. 先写 `qwen2-5-3b.yaml` CR（参考 `deploy/models/qwen2-5-7b.yaml` 。
2. **预热**：apply 一次让权重下载完成（~6GB，看 hf-cache 目录增长），等到 Ready。这次不计时。
3. 修改 7B CR 的 gpuMemoryUtilization 0.85 → 0.45（D1 模板里留好的注释位），apply，观察 Recreate 滚动——**这一步同时是 D4 验收④的双人版**：7B Pod 重建期间 3B 应不受影响，验证隔离性。
4. 双 Ready 后：对两个 endpoint 各跑一次 `scripts/smoke_stream.py`，确认流式正常。

* [x] 完成，双 Ready + 双流式冒烟通过；`nvidia-smi` 输出留档（两进程共卡、各 ~11G）

## Step 5 验收（60s 检查点）

**口径**：warm cache（权重与编译缓存都已就位）+ 镜像 IfNotPresent 已就位；计时起点 `kubectl apply` 成功返回，终点 `status.endpoints` 非空且 curl `/v1/models` 通。**首次下载/首次编译不计入，但必须记进 runbook**（说明控制面验收与冷启动成本是两件事）。

验收结果

| 阶段 | 实测 | 性质 |
|---|---|---|
| apply → Pod 调度 + 镜像（IfNotPresent 已就位） | ~1s | 控制面，已到极限 |
| 容器启动 → vLLM 首行日志（python + import) | ~10s | 进程冷启动物理成本 |
| APIServer → EngineCore spawn + CUDA context | **~35s** | 最大头：两进程接力 import torch + 各自初始化 CUDA |
| 权重加载（page cache 热） | 8.3s | hf-cache 已热，接近极限 |
| torch.compile(**冷**，无持久卷） | 9.2s | 杠杆②可压到 ~1-2s |
| CUDA graph capture 两轮 + profiling | ~5s | 只有 enforce-eager 能动（不动） |
| init engine 收尾（kernel IR / autotune) | ~8.5s | 引擎内部，难压 |
| APIServer 收尾（chat template / 采样参数 / 路由） | ~10s | 引擎内部，难压 |
| readiness 量化（periodSeconds=10) | ~2s | 杠杆①，本次踩点运气好，最坏值 8s |
| Pod Ready → CR status.endpoints 回填 | 亚秒 ✓ | 控制面，D4 链路正常 |


```bash
# 计时采集：apply 前后打点
date +%s.%N; kubectl apply -f deploy/models/qwen2-5-7b.yaml
kubectl -n mini-maas get events -w --sort-by=.lastTimestamp   # 各阶段时间戳
kubectl -n mini-maas get pod -w                                # Ready 跳变时刻
kubectl get model qwen2-5-7b -n mini-maas -o jsonpath='{.status}' -w
# vLLM 日志里的时间戳：进程启动 / 权重加载完成 / graph capture 完成 / Uvicorn up
```

验收项：

* [ ] ① **60s 分解表**：0.4 节那张表填上实测数字，两个模型各一行。每段多少秒，总和多少，过没过 60s。
* [ ] ② **判定**：过了——记录哪个杠杆贡献最大；没过——按 Step 2 的降级判据明确归因（控制面问题→降级 informer；引擎启动问题→调优后重测，不算降级理由）。两种结果都是有效交付，糊涂过关不是。
* [ ] ③ **互相干扰定性观察**：bench 打满 7B（c=16 即可，别上强度），同时手动 curl 3B，记录 3B 的 TTFT 比空闲时恶化多少。只定性，定量留给 D13。
* [ ] ④ **隔离性**：删 3B CR，7B 全程不抖；重建 3B，观察它是否在 7B 运行中成功挤进显存（0.1 节口径的实景验证）。

## Step 6 收尾

* [ ] 对标笔记（≤30 行）：KServe/Knative 的 GPU 共享与时间片方案（KSAI 社区多实例共卡通常怎么声明——device plugin 配置、MIG profile、还是 node label 约定？/ 我为什么用 time-slicing + 显存账本 / 这个方案在异构多卡时哪里先崩）。
* [ ] `git add -A && git commit -m "D5: 多模型共卡 — time-slicing 供给 + 双 CR 60s 检查点"`
* [ ] 把 Step 2 账本表 + 5 个问题答案 + Step 5 的 60s 分解表发我 review（我盯：显存口径的实测证据、降级判据是否清晰、编译缓存决策的理由）

***

## 思考题（review 时我会问）

1. time-slicing replicas=4 会立刻炸什么、潜伏炸什么？"供给谎报"的安全边界由谁守护？
2. 两个 0.45 实例**同时**冷启动（不是先后），按你实测的 profiling 口径推演会发生什么？有没有稳定的失败模式？
3. 60s 预算里哪一段是物理下限、哪一段是工程可压缩的？如果把检查点改成 30s，你第一个动哪个旋钮？
4. readinessProbe `periodSeconds=10, failureThreshold=90` 在 D1（冷下载 15 分钟）是宽容，在 D5 是 10s 的探测延迟。probe 参数该不该成为 CR spec 的字段，还是 Operator 常量？判断标准是什么？
5. D11 autoscaler 要把 7B 从 1 扩到 2：共卡布局下第三个 0.45 挤不进来。扩容决策需要知道什么它现在不知道的信息？（提示：这是 bin-packing 问题的萌芽，autoscaler 改 replicas 之前，谁告诉它"卡上还有没有位子"？）
6. 今天如果降级到了 informer 方案，D6-D8 的哪些设计假设要跟着改？哪些纹丝不动？

## 踩坑记录区（遇到即记，含报错原文与解法）

| 时间 | 现象 | 原因 | 解法 |
|---|---|---|---|
| 09-28 | CR apply 后无 Deployment、无 Pod、无下载、无 status，但存量 7B Pod 一直健康 | Operator 是本地 `make run` 跑的，进程早就没了；CR 只是 etcd 里的期望贴纸，没有控制回路什么都不会发生。存量业务不死是因为 Deployment→Pod 由 k3s 内置 controller-manager 维持，自研 Operator 只在期望变化时才被需要 | **CR apply 无反应，第一嫌疑人是控制回路是否在跑**（`ps aux \| grep manager`）；这条同时是"控制面可重启不丢业务"的活教材 |
| 09-29 | 3B CR 的 `gpuMemoryUtilization` 写成蛇形 `gpu_memory_utilization`，apply 不报错但字段"消失" | unknown field 被 structural schema **静默 pruning**（D3 验收③的实测行为首次反噬），实例会拿到默认值 0.85/0.9，共卡直接超卖 | apply 后必须看 `kubectl get model -o yaml` 回显确认字段真吃进去了——**验收看回显，不看"没报错"** |
| 09-29 | **WSL2 显存虚拟化事件**：3B 在 7B 占 0.85 的情况下"成功"启动；两个实例启动日志的 `Free memory on device` 一字不差恒为 22.45/23.99 GiB；nvidia-smi 全卡仅 10840MiB ≈ 3B 一家 footprint，7B 的 ~20G"失踪"；per-process 显存显示 N/A | WSL2 GPU-PV 走 WDDM 驻留模型：分配≠驻留，超卖时 VidMm 把空闲分配**静默逐出到系统内存**（佐证：microsoft/WSL#41176 `dxgkio_make_resident` ENOMEM 而 nvidia-smi 显示空闲；dxgkrnl ioctl 清单含 make_resident/evict/paging_queue）。0.85+0.45=1.30 超卖没爆炸，代价是 7B 被换页、TTFT 污染不可见。期间误判过一次"7B 僵尸服务"——vLLM 日志是 UTC，09-28 23:36 UTC = 本地 07:36 今早 | ① WSL2 下"启动成功"≠"物理装得下"，显存账只能用**算术（预算之和 ≤ 0.90）+ nvidia-smi 常驻总量**验证；② 压测期间 paging 一旦发生，当组数据作废；③ 根治靠预算纪律（无开关可关）；④ 终极解法：迁移裸金属（见 [env-migration.md](env-migration.md)） |

### 附：WSL2 显存虚拟化案件全记录（2026-09-29）

**案情**：3B CR apply 后，在 7B 仍持 0.85×24G ≈ 20.4G 的情况下，3B（0.45）不仅调度成功（time-slicing 供给正常），vLLM 也启动成功——按裸金属语义这不可能。

**证据链**（三级）：

1. **现象级（本地铁证）**：① 7B/3B 两份启动日志 `Free memory on device` 恒为 `22.45/23.99 GiB`——永不变化的读数是虚拟化视图，不是测量；② `nvidia-smi` 总量 10840MiB ≈ 3B 预算 + 基线，7B 自报的 13.76G KV cache 不在卡上；③ 算术判决 0.85+0.45=1.30 > 1.0，物理上必有代价。
2. **机制级（公开资料）**：microsoft/WSL issue #41176（2026-07）证实 WSL2 存在 WDDM 驻留预算与分页失败路径（`dxgkio_make_resident` ENOMEM 而 nvidia-smi 谎报空闲）；IOActive 的 dxgkrnl 源码分析显示 vmbus 通道内建 `LX_DXMAKERESIDENT / LX_DXEVICT / paging_queue` 原语。
3. **推论级**：7B 权重今早已加载+warmup（必曾驻留），现已不在 → 逐出真实发生，触发点是 3B 启动使总需求 31G 超物理 24.5G。

**误案记录**：一度怀疑 7B 是重启遗留的"僵尸服务"——证伪依据是 vLLM 日志时间戳为 UTC（09-28 23:36 UTC = 09-29 07:36 本地 = 今早发现世），且 7B 冒烟可正常应答。**教训：跨时区看日志先对齐时区再下结论。**

**工程结论**：

- WSL2 可做控制面开发（D3-D5 链路验证不受影响），**不可做性能数据的最终来源**——profiling 口径、nvidia-smi、vLLM 自报三方数据互相矛盾。
- D13 实验纪律新增一条：压测期间监控常驻显存，发现 paging 当组数据作废。
- 该事件直接促成迁移裸金属决策（[env-migration.md](env-migration.md)）；迁移后 Phase 4 验收③要求 `Free memory` 读数恢复真实（两实例读数不同、随对方启停变化）。

## 常见坑速查

| 症状 | 真实原因 | 解法 |
|---|---|---|
| 第二个 Pod 永远 Pending，`Insufficient nvidia.com/gpu` | device plugin 只广告物理卡数 1 | Step 3：time-slicing；先 `describe node` 确认广告份数 |
| 第二个 vLLM 启动 OOM / 显存不足报错 | profiling 口径与预期不符；或第三个 GPU 进程占卡（vllm-dev 忘 stop） | 按 0.1 节实测口径重算预算；`nvidia-smi` 看全部进程 |
| 重建 Pod 后启动又变慢 | 编译缓存随 Pod 销毁（`~/.cache/vllm` 未持久化） | Step 2 决策项；挂 hostPath 后重测 |
| apply 3B 后 vLLM 日志反复重试下载 | hf-cache 两个 Pod 共享同一 hostPath，下载锁/半成品文件互相干扰 | 预热串行做：一个 Ready 再动另一个 |
| 7B 改 0.45 重建时 3B 也重启了 | label/selector 串味，两个 CR 的子资源边界没划清 | 回到 D4 检查 buildDeployment 的 label 生成是否含 CR 名 |
| 双 Ready 但 3B 的 429/OOM 在压测时偶发 | 共卡 SM 时间片争抢 + KV cache 互相独立但显存贴线 | D13 定量；今天只记定性现象 |

***

## 附：D5 命令速查

```bash
# GPU 供给
kubectl describe node win11-pro | grep -E 'nvidia.com/gpu|Allocatable' -A6
kubectl -n kube-system get ds,pods -o wide | grep -i nvidia

# 显存实况（共卡验证的核心证据）
nvidia-smi --query-compute-apps=pid,used_memory --format=csv
nvidia-smi

# 双 CR 状态
kubectl -n mini-maas get model -o wide
kubectl get model qwen2-5-7b qwen2-5-3b -n mini-maas -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\t"}{.status.endpoints}{"\n"}{end}'

# 冒烟（两个 endpoint 各一次）
python scripts/smoke_stream.py --base-url http://<endpoint7b> --model qwen2.5-7b
python scripts/smoke_stream.py --base-url http://<endpoint3b> --model <你3B的served-name>
```
