# 设计文档 — Autoscaler Loop

先读本文再动手。任务清单与验收落在 [d11-runbook.md](../d11-runbook.md)，本文只讲"为什么这么做"和重点问题的参考答案。

## 一、框架与流程

### 1.1 今天在整条链路里的位置

D11 交付全项目最后一块自研组件，也是唯一一个**闭环组件**：此前所有天都在铺"人肉驱动"的能力（手改 CR、手跑压测、手导出数据），今天把"指标 → 决策 → 改声明 → 容量变化 → 指标回落"这个反馈环接通。验收两条：突发扩容 < 90s；静默期自动缩容。里程碑"闭环成"的判定清单见 Q8。

今天踩的地基全部是前十天预埋的，动手前先确认它们还在：

- **5s scrape**：D9 把 vllm job 的抓取间隔定为 5s，prometheus.yaml 注释里写死了理由——"给 autoscaler 反应留预算"。这是 90s 预算里第一笔存款，今天取款。
- **口径与查询**：D10 的 metrics.md 定死了 `queue_waiting` / `queue_running` / `replicas_actual` / `gateway_ttft_p99` 的口径，queries.yaml 里这 32 条 PromQL 全部对活集群验证过——autoscaler 的决策输入**不许发明新指标**，从这里取。
- **控制面链路**：D4/D5 验证过"改 CR spec → reconcile → Deployment → Pod Ready → status.endpoints 回填（亚秒）"，D6/D8 验证过网关 informer watch Model CR 亚秒级更新路由表。今天**不动** operator 和网关的任何逻辑（唯一例外是拧 D5 留下的 readiness 杠杆，见 Q4）。
- **验收工具**：D10 的 export 管线就是今天的验收曲线生产线，`replicas_actual` 条目是 D10 预置的副本数阶梯图。
- **容量账**：D5 实测了 warm cache 下 Pod 启动全程 ≈ 60s（分解表在 d5 日志 Step 5），这是 90s 里最大也最不可谈判的一段，Q4 拿它做预算。

一个纪律从今天开始执行：**`spec.serving.replicas` 从"人手字段"变成"autoscaler 字段"**——项目文档 4.2 注释里早写了"autoscaler 的唯一写入字段"。autoscaler 上线后，手改 replicas 就是和控制器打架（想固定容量做实验时，正确姿势是 min=max 或停 loop，见"三、给后续日子留的接缝"里 D13 那条）。

### 1.2 控制回路数据流

```
bench burst ──► vLLM 队列堆积（queue_waiting 抬头）
                     │
        Prometheus（5s scrape，D9 已定）
                     │
        autoscaler loop（5s tick：查询 → 决策 → 防抖）
                     │ PATCH Model CR spec.serving.replicas: 1→2
                     ▼
        operator reconcile（D4 已有，改 spec 只动副本数，不碰存量 Pod）
                     │
        kubelet 起新 Pod（warm cache ≈ 60s，D5 实测）→ Ready
                     │ status.endpoints 回填（亚秒）
                     ▼
        网关 informer 见 CR 更新 → 路由表加 endpoint（亚秒）→ 队列泄洪
                     │
        queue_waiting 回落 ──►（反馈）不再触发扩容；静默持续 → 缩回 1
```

设计的支点有三个：

- **只写声明，不碰执行**（Q2）：autoscaler  PATCH CR 的 `spec.serving.replicas`，之后的一切交给 operator/kubelet/网关 informer 这些已验证的链路。它自己绝不直接改 Deployment——双写必争，而且绕开声明式就失去了 reconcile 的兜底。
- **决策逻辑是纯函数**（Q3）：`decide(信号, 状态) → 目标副本数`，与"查询 Prometheus"和"PATCH CR"两个副作用隔离，表驱动单测覆盖所有抖动场景。loop 骨架先以 dry-run 跑真实流量校准阈值，再开接管。
- **预算是算出来的，不是试出来的**（Q4）：90s = 决策段（可调）+ 控制面（~2s）+ 容量段（D5 实测 ~60s）+ 接管段（~1s）。动手前先手动 patch 复测一遍容量段，确认底数没变，再决定决策段能花多少。

### 1.3 组件分解

| 组件 | 位置（你的仓库） | 职责 | 状态 |
|---|---|---|---|
| autoscaler main | `operator/cmd/autoscaler/main.go` | 配置加载、tick loop、组件装配 | 新建 |
| metrics client | `operator/internal/autoscaler/metrics.go` | Prometheus HTTP query API（instant），与 D10 export 同一 API | 新建 |
| decider（纯函数） | `operator/internal/autoscaler/decide.go` | 信号 + 状态 → 目标副本数；阈值/窗口/防抖全在这 | 新建 |
| decider 单测 | `operator/internal/autoscaler/decide_test.go` | 表驱动：突增/毛刺/静默/振荡/prometheus 挂 | 新建 |
| actuator | `operator/internal/autoscaler/actuator.go` | PATCH CR replicas，冲突重试，dry-run 开关 | 新建 |
| 部署方式 | 宿主机进程（同网关）或 cluster 内 Deployment | v1 先宿主机 `go run`，不进集群 | 新建 |
| 验收场景 | bench `--mode burst` + D10 export 管线 | 场景 A/B/C 曲线归档 | 复用 |
| 对标笔记 | `docs/design-notes/` | Knative KPA，≤30 行三段式 | 新建 |

进 operator 仓库的理由：复用 `api/v1alpha1` 的 scheme 和 controller-runtime client，不引第三套类型；但**独立 main、独立进程**（Q2 讲为什么不塞进 manager）。

### 1.4 实施顺序

1. **先量底数（人工彩排）**：手动 `kubectl patch model qwen2.5-7b-awq --type merge -p '{"spec":{"serving":{"replicas":2}}}'`，用 D10 管线记录"patch → 第二副本在网关路由表出现并接流"全程曲线。这就是 90s 里不可压缩的部分（D5 测过一次，今天用新管线复测，顺便验证 readiness 杠杆的收益）。底数不对，后面全是白干。
2. **decider 纯函数 + 单测**：先把 Q3 的决策表写成测试，再实现。这步不碰集群。
3. **dry-run 校准**：loop 只查询、只 log"我想扩/我想缩"，不动手。跑一轮真实 burst，对比"它想扩的时刻"和"你希望它扩的时刻"，把阈值调定型。
4. **接管验收**：打开 actuator，按 Q8 场景 A/B（/C）跑，曲线走 export 管线归档。
5. **留档**：对 90s 做分解表（像 D5 那张），关键数字贴当日 dev-journal；对标笔记（Knative KPA）落盘。

这个顺序的精神同 D10：先人工建立"什么算对"的直觉，再写自动化——顺序反了会把错误阈值固化进控制器，而且抖动起来你分不清是阈值错了还是代码错了。

## 二、重点问题与参考答案

### Q1 信号选型：凭什么决定扩缩？

**主信号：`queue_waiting`（`sum by (model_name) (vllm:num_requests_waiting)`，5s 粒度）**。排队是"容量不够"的硬证据：continuous batching 下，只要引擎吃得下，QPS 再高 waiting 也是 0；waiting 一抬头就是供给缺口，没有歧义。

**副信号：`queue_running`**（饱和度，用于缩容判据）和 **`gateway_ttft_p99`**（用户视角，只观测不触发——它受输出长度分布干扰，做触发器会误动作）。

明确不用的：

- **GPU 利用率**：dcgm 没装（D10 Q1 让位 D12）；且 batching 引擎的 util 与积压非线性——util 100% 时可能零排队，扩它纯属浪费。
- **QPS/RPS**：见上，高 QPS ≠ 该扩。
- **CPU/内存**：vLLM 瓶颈在 GPU 与 KV cache，CPU 信号对本项目是噪声。

阈值 v1 默认值（dry-run 校准后定稿）：扩容 `avg_over_time(queue_waiting[30s]) ≥ 4` 连续 2 个样本（≈10-15s 确认）；缩容 `queue_waiting == 0 且 queue_running ≤ 1` 持续 5 分钟。4 的含义：单实例 maxNumSeqs=64 的上下文里，排队超过个位数说明 batch 已满且开始积压；5 分钟静默的含义：覆盖"突发间隙"的典型尺度，比一次扩容的代价（60s 冷启动 + 显存抖动）保守一个量级。

### Q2 架构：autoscaler 住哪？怎么写副本数？为什么不用现成的？

**住 operator 仓库 `cmd/autoscaler`，独立 main、独立进程**——不塞进 controller-runtime manager。理由：controller 是事件驱动的"期望 vs 实际"收敛，autoscaler 是时间驱动的轮询生产者（它的输入在 Prometheus，不在 etcd），硬写成 controller 得靠 `RequeueAfter` 模拟定时器，语义拧巴还白背一套 reconcile 框架。kubebuilder 脚手架里加第二个 cmd 是标准做法（`PROJECT` 文件不用动，`go run ./cmd/autoscaler` 直接起）。

**写 `Model CR` 的 `spec.serving.replicas`（merge patch），绝不直接改 Deployment**。Deployment 的 replicas 归 operator reconcile 管（model_controller.go 里 `deployment.Spec.Replicas = desired.Spec.Replicas`），双写会被 reconcile 覆盖回去，还要猜谁的时序赢。**也不给 CRD 加 scale subresource**：那是 HPA 这类外部控制器寻址副本数的标准接口，自研 loop 直接 patch spec 字段即可，加了只是摆设（D14 想接 HPA 时再补不迟）。

**为什么不用现成的 HPA / KEDA**：

- HPA 路线要通到 CR，得实现 scale subresource + 部署 custom-metrics adapter（prometheus-adapter）把 PromQL 暴露成 metrics API——链路比自研长，调试面更大，而且 HPA 的决策逻辑（比率公式 + stabilization）对"队列深度"这种矢量信号不是天生契合。
- KEDA 的 Prometheus scaler 其实能直接用（ScaledObject 指 Deployment + PromQL），**它是今天的降级方案 B**：自研 loop 抖动修不好时，半天内可接上，里程碑以"半自研"降级认定。
- 但自研是项目文档写明的要求（"Go 自研 loop：指标采集 → 决策 → 修改 CR spec.replicas"），学习收益就在 Q3 的决策逻辑里——用现成的一天能交付，但什么都留不下。

单副本运行即可，不做 leader election（多活双写的问题在本项目规模不存在，做了是镀金）。

### Q3 决策算法：阈值规则还是比率公式？

生产参照两家：HPA 用比率公式 `desired = ceil(current × observed / target)`；Knative KPA 用并发目标 `desired = observedConcurrency / targetConcurrency` 外加 **stable/panic 双窗口**（默认 stable 60s、panic 窗口是 stable 的 10%，panic 模式下按峰值瞬时决策抢速度）。

**v1 用规则式步进（+1/-1），不用比率公式**。理由：单卡 maxReplicas=2 的世界里，比率公式算出来也只有 1 或 2，与 +1 完全等价，却多了"目标值该设多少"的玄学参数；规则式每个分支都能对着日志解释"它为什么动手"。比率公式留给 D12 多节点 max 放大后再引入（那时 `ceil(running / targetRunning)` 才有意义）。

决策表（参考答案，写进 decider 单测）：

| 条件 | 动作 | 防抖 |
|---|---|---|
| `waiting_30s_avg ≥ 4` 连续 2 样本，且 current < max，且无未就绪扩容 | replicas +1 | 扩容后记录 lastScaleUpAt |
| `waiting == 0 且 running ≤ 1` 持续 ≥ 5min，且 current > min，且距 lastScaleUpAt > 冷却期 | replicas −1 | 缩容后记录 lastScaleDownAt |
| 其他（含 Prometheus 查询失败） | 不动 | 见 Q6 |

三条不变量，全部进单测：

1. **一次只动一副本**：来回振荡一次的代价（60s 冷启动 + 显存重分配）远大于响应慢 10s 的代价。
2. **扩容未落地不追加**：`replicas_actual < spec.replicas`（新 Pod 还没 Ready）时不再往上叠——等待中的扩容不是扩容失败的证据。
3. **扩后冷却才准缩**：lastScaleUpAt 之后 5 分钟内不许缩（防"扩完队列瞬时泄空 → 误判静默 → 立刻缩回 → 队列又积"的死循环）。注意这和缩容窗口是两道独立闸门：冷却期管"别马上反悔"，5 分钟窗口管"别被毛刺骗"。

### Q4 90 秒预算怎么分？（今天的核心计算题）

计时口径（验收场景 A 通用）：**t0 = burst 第一发请求离开 bench；t1 = 第二副本出现在网关路由表且成功接流**（观测：`route_decisions` 出现新 endpoint 分布 + TTFT P99 尖峰回落）。要求 t1 − t0 ≤ 90s。

预算分解（底数来自 D5 实测表，今天 Step 1 用新管线复测）：

| 段 | 内容 | 实测/预算 | 可谈判性 |
|---|---|---|---|
| 决策 | scrape 5s + tick 5s + 连续 2 样本确认 | 10~15s | **主要谈判空间**（样本数、tick） |
| 控制面 | patch CR + reconcile + 调度 | ~2s | 已到极限（D5） |
| 容量 | Pod 启动 → Ready（warm） | ~60s | 最大头 CUDA ctx 35s 物理成本；readiness 量化最坏 10s 是白送的 |
| 接管 | endpoints 回填 + 网关 informer | ~1s | 已验证亚秒 |
| **合计** | | **~73-78s** | 达标，余量 ~12s |

容量段里有一笔 D5 留下、今天必须取的存款：**readiness `periodSeconds: 10 → 2`**（buildDeployment 里一行）。D5 验收时它"踩点运气好"只花了 2s，最坏值是 8-10s——90s 预算里没有给运气留的位置。这是今天唯一允许动 operator 代码的地方，改完 D4 的五连回归要重跑一遍。

预算守不住时按序拧（每拧一步记录取舍）：① readiness periodSeconds→2（先取这笔）；② 确认样本 2→1（代价：毛刺敏感，用阈值从 4 提到 8 补偿）；③ tick 5s→2s（代价：Prometheus 查询压力，无所谓）。**不许拧的**：`--enforce-eager`（D5 纪律：毁 baseline 口径，等于拿 D2 以来全部实验数据给今天陪葬）；time-slicing 份数（Q7）。

### Q5 缩容安全：会不会掐死在途请求？

**缩容语义先认清**：Deployment replicas 2→1 是直接删一个 Pod，vLLM 收 SIGTERM 就走，在途流式连接当场断，网关侧表现为这条流 5xx/断流。**今天的缩容判据（waiting==0 且 running ≤1 持续 5 分钟）保证缩容时该副本基本无在途**，接受残余风险并记录在案：running ≤ 1 意味着最多一条流可能被掐，且这只发生在"安静了 5 分钟后恰好来了一发"的极小窗口。

**minReplicas=1 是硬底，永不缩 0**。冷启动 60s 的服务缩到 0 等于服务下线；scale-to-zero 是 Knative 那个世界的设计（它需要冷启 <1s 的 runtime + 请求排队激活层），本项目 vLLM 的启动成本决定了这门课不修。

graceful drain（preStop sleep + 等 endpoint 摘除 + 在途排空）进 README roadmap，v2 再做——它是对的，但今天它会吃掉决策逻辑的时间预算。

### Q6 失效模式与兜底

| 失效 | 行为 | 理由 |
|---|---|---|
| Prometheus 不可达 / 查询报错 | **保持现状，连续失败只 log** | 查不到信号时任何动作都是赌博；"不动"是唯一可辩护的默认 |
| 指标返回空序列（模型名写错等） | 同上，log 区分"无数据"与"零值" | 零值是缩容信号，无数据什么都不是——混了会误缩 |
| patch CR 冲突 | 指数退避重试 3 次 | 单写者下几乎不发生，发生了就是有人在手改（Q1 纪律） |
| operator 挂了 | 写完 CR 没反应 → 观测 `replicas_actual` 长期 ≠ spec → log | 不重复 patch（patch 幂等，重复无意义）；这是"闭环断在执行段"的可观测性 |
| 新 Pod OOM / 起不来 | 不动，`replicas_actual` 卡住 → log "容量供给不足" | 供给账本是 Q7 的事，autoscaler 不该自作主张继续加 |
| 网关路由不更新 | 不管 | D6/D8 已验证亚秒，不在今天兜底范围 |

### Q7 单卡 GPU 供给账本（今天最容易翻车的操作题）

D5 的 time-slicing 配置让 node 广播 `nvidia.com/gpu: 2`，当前 **7b×1 + 3b×1 已把 2 份占满**——直接扩 7b 第二副本，Pod 永远 Pending（`Insufficient nvidia.com/gpu`），D5 日志的思考题 5 预告的正是这个实景。

**方案 A（推荐）：D11 实验期间把 3b 缩到 0**（`serving.replicas: 0`，CRD 允许 Minimum=0）或直接删 CR，7b 独占两份供给。顺手把"3b 让位"录成多模型资源调度的演示素材。D12 云上多卡后 3b 恢复。

方案 B（不推荐）：time-slicing 调到 3 份——谎报更深，24G 被三份摊，显存账本的兜底责任加重一分，只为演示不值得。

相应的配置约束写进 autoscaler：**`maxReplicas=2` 对应显存账本 0.45×2=0.90 ≤ 1.0**；config 里 min/max 与 CR 的 `gpuMemoryUtilization` 是同一本账的两页，改一个必须想另一个。D12 多节点后此约束解除，改数字即可。

另注意：实验起点是把 7b 的 CR（D8 起一直是 replicas=2）**先收回 1**，从此 1↔2 的切换权交给 autoscaler。

### Q8 验收场景与"闭环成"判定

三个场景，全部走 D10 export 管线出曲线（`replicas_actual` 阶梯 + `queue_waiting` + TTFT P99 同图）：

- **场景 A 突发扩容（里程碑主判据）**：7b 单副本稳态（c=8，确认吃得下、waiting 贴 0）→ bench burst c=48。判据：t1−t0 ≤ 90s（Q4 口径）；曲线上可见 waiting 抬头 → replicas_actual 跳 2 → TTFT P99 尖峰回落 → route_decisions 双 endpoint 分布。
- **场景 B 静默缩容**：停 bench。判据：5 分钟窗口内**不缩**（防毛刺的证据，同样重要），窗口过后 1-2 个 tick 内缩到 1；全程 bench 侧零 5xx。
- **场景 C 振荡压力（可选，进度超前才做）**：burst 60s / 停 60s 循环 5 轮。判据：缩容窗口按住不动，只在 A 场景条件下反复扩、不抖。

**里程碑"闭环成"判定清单**：A、B 全绿 + 90s 分解表落盘 + decider 单测覆盖 Q3 三张表 + 对标笔记（Knative KPA）。**降级判据**：决策逻辑修不稳、或 90s 怎么都压不进 → 转半自动（loop 常驻 dry-run 打印建议，人 `kubectl patch` 执行），或接 KEDA（Q2 方案 B）；里程碑记"半闭环"，缺口与根因写进 README roadmap，D12 复核。降级不丢人，糊涂过关才丢人（D5 原话）。

### Q9 今天不装什么

- 不加 scale subresource、不接 HPA（KEDA 只作为降级预案备着，不预装）。
- 不做多模型同时伸缩：loop 只盯 7b（`--model` flag 单值）；多模型资源仲裁（谁该让位）是调度器的事，今天不越界。
- 不做 graceful drain（Q5）、不做预测性/定时伸缩、不做 leader election（Q2）。
- 不动 operator 逻辑（readiness periodSeconds 一处数字除外，Q4）、不动网关、不动 Prometheus 配置——今天的改动面收敛在"一个新进程 + 一行 probe 数字"。
- bench 不改：burst 模式 D2 已有，直接用。

## 三、给后续日子留的接缝

- **D12（上云）**：`maxReplicas` 调大、3b 恢复；多节点后 signal 口径不变（PromQL 按 model_name 聚合天然跨实例），但 GPU 供给账本按云机重算；dcgm 补上后 GPU util 可作副信号进 decider（留好接口：`Signal` 结构体加字段即可，决策表不动）。
- **D13（实验日）**：**每组实验前必须固定容量**——autoscaler 停掉或 min=max 设为实验要求的副本数，否则组间容量不可比、数据全部作废。这条纪律今天写死，runbook 里抄一遍。
- **D14（报告/demo）**：场景 A 的"waiting 抬头 → replicas 阶梯 → TTFT 尖峰回落"三曲线同图是全场最有说服力的一张图，今天归档时就把这张图渲染好；README 的 autoscaling 一节拿 Q4 的预算表当骨架。

## 四、延伸阅读

按"今天就要用"到"回头再看"排序：

1. **Knative Autoscaler（KPA）概念与算法** — 今日对标笔记素材：stable/panic 双窗口、并发目标、desired 计算：<https://knative.dev/docs/serving/autoscaling/> 与 <https://knative.dev/docs/serving/autoscaling/kpa-specific/>
2. **HPA 算法细节（官方文档 "Algorithm details" 一节）** — 比率公式、tolerance、stabilizationWindowSeconds、缩容速率限制的出处：<https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/#algorithm-details>
3. **KEDA Prometheus Scaler** — 降级方案 B 的说明书，顺便看它的 `activationThreshold`/`metricType` 设计：<https://keda.sh/docs/latest/scalers/prometheus/>
4. **CRD scale subresource** — 知道它存在、它为谁服务（HPA）、今天为什么不用：<https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#scale-subresource>
5. **vLLM metrics 官方文档** — `num_requests_waiting/running` 的口径复核（D10 已读，今天只查这两个 gauge 的采样语义）：<https://docs.vllm.ai/en/latest/usage/metrics.html>
6. **Google SRE Book, Ch. 21 "Handling Overload"** — 用队列深度/负载做容量决策的古典依据，顺便看 client-side throttling 与 criticality 的思路：<https://sre.google/sre-book/handling-overload/>
7. **controller-runtime 多 main 布局** — operator 仓库加 `cmd/autoscaler` 时参考 kubebuilder 的 multi-group/multi-cmd 约定：<https://book.kubebuilder.io/>

当日对标笔记：方向 1（Knative KPA，项目文档 4.4 对照表"弹性伸缩"行），≤30 行三段式，存 `docs/design-notes/`。建议聚焦三个问题：它为什么用并发目标而不是 CPU；panic 窗口解决什么；它 scale-to-zero 的代价是什么、为什么 vLLM 学不了。
