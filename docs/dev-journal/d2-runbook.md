# D2 Runbook - bench.py 骨架 + 基线测试数据

## ✅ Step 0 指标口径和测试方式

### 0.1 指标口径

| 指标 | 定义 | 为什么这样定义 |
|---|---|---|
| TTFT | 请求发出 → 首个**非空** content chunk | 用户体感延迟；空 chunk（role 帧）不算，否则测的是网络不是推理 |
| TPOT | (末 chunk − 首 chunk) / (chunk 数 − 1) | 解码速率；要求 chunks>1，否则分母为 0 |
| 吞吐 | Σ completion tokens / **墙钟总时长** | 墙钟是系统容量口径；逐请求时长相加会高估（请求间有空隙） |
| token 计数 | 流式末帧 `usage`（`stream_options.include_usage`） | 精确计数，不用 tokenizer 估算——估算会被 special tokens 带偏 |

解码时固定`temperature = 0`，排除采样随机性，否则两次基线会不一样。

### 0.2 流量模型

- **steady（闭环）**： N个worker，各自“发请求 -> 等待完成 -> 发下一个”，持续 T 秒。目的是测试“稳态容量”：给定并发，系统能抗多少token/s、延迟是多少
- **burst（开环）**：瞬时间打入N个请求不管是否完成。目的是测试**队列行为**：TTFT如何随排队深度劣化

#### 问题：为什么不使用每秒固定发N个请求（持续开环）的方式测试？这个更加类似于传统业务的QPS

系统过载时，请求会无限堆积，最终会逐步把系统打崩溃。

### 0.3 确保baseline可信

1. **开启prefix cache**： baseline必须使用随机prompt（`prefix-len`为0），否则重复命中前缀缓存，TTFT会很低。
2. **预热**：CUDA graph 捕获/编译发生在首个请求，不要计入baseline.
3. **client与server同机器的情况**：限制client使用的核心数，否则压测进程和vLLM抢CPU，导致数据失真。

## ✅ Step 1 环境检查

```bash
# 确保部署的vLLM引擎pod还在运行
kubectl -n mini-maas get pod

# 确保端口转发正常运行
curl -sf http://localhost:8000/v1/models    # 应返回 qwen2.5-7b；不通则先 port-forward
```

## ✅ Step 2 bench.py 脚本

1. **单请求流式**：用官方 openai SDK 的 AsyncOpenAI，stream=True，逐 chunk 打印。 自测：肉眼确认是逐 chunk 到达而非整段返回。
2. **加计时**：记录 t0 / t_first / t_last / chunks，算 TTFT、TPOT、e2e。 自测：单请求 TTFT 应在 300ms 量级（对照 D1 冒烟 330ms）；差一个数量级就是口径错了。
3. **加 usage**：stream_options={"include_usage": True}，从末帧取 prompt/completion tokens。 自测：completion_tokens ≈ max_tokens（128），差太多说明没取到 usage。
4. **加并发**：steady 闭环 worker + burst 瞬时 gather；结果落 JSONL（首行 config+summary，其后逐请求）。 自测：c=4 跑 10s，请求数应 ≈ 4 × (10s / 单请求 e2e)，数量级对即可。

## ✅ Step 3 单点测试

**确保以下几点**：

- [x] 成功率 100%
- [x] TTFT P50 ≈20ms（c=1 无排队，需要预热）
- [x] TPOT P50：7B-AWQ 单并发解码应在 20-40ms 量级（≈25-50 tok/s）
- [x] 吞吐 ≈ 单请求解码速率（c=1 时两者应吻合，理解为什么）


## ✅ Step 4 基线测试

steady 和 burst测试数据全部落盘，成功率均 100%

```bash
make 20-baseline
```

## Step 5 GPU 采样

另开一个终端，在 c=16 和 burst 期间各采一段：

```bash
nvidia-smi dmon -s u -d 1 -c 70 > results/dmon-c16.log   # 每秒采 GPU 利用率
nvidia-smi --query-gpu=memory.used --format=csv -l 5 -c 15 > results/mem-burst.log
```

把利用率区间和显存占用填进 baseline.md 第 4 节

## 踩坑记录区（遇到即记，含报错原文与解法）

| 现象 | 原因 | 解法 |
| --- | --- | --- |
| 重启机器后旧 vLLM Pod 滞留 `Unknown`，同时 Deployment 新建了同名前缀的新 Pod | 非优雅关机，kubelet 未 drain 即失联；node controller 把失联节点上的 Pod 标记 NodeLost(Unknown)，ReplicaSet 发现可用副本为 0 遂补新 Pod；旧容器已随关机销毁，仅 etcd 记录滞留 | `kubectl -n mini-maas delete pod <old-pod>`；删不掉加 `--force --grace-period=0`。预防：重启前 `kubectl drain <node> --ignore-daemonsets`（云机开关机纪律同用）|
| c=1 基线每请求仅解码 \~30 token（期望 128），吞吐 137 tok/s 虚低且输出长度失控 | `max_tokens` 是上限不是定长：模型说完即吐 EOS 停止（EOS 裁决在引擎层 StopChecker，非模型行为）；输出长度随 prompt 内容漂移，污染并发间对比 | bench.py 加 `--ignore-eos`（`extra_body={"ignore_eos": True}`，默认开），强制解码满 max\_tokens 使输出长度成为受控变量。验证三预测：completion\_tokens≈128、请求数≈65-70、吞吐≈140（实测 145.66，全中）。仅用于压测，生产禁用（EOS 后内容是训练分布外噪声） |
| burst-32 两次结果差 5 倍：单独跑 TTFT P50 83.5ms，接在 c=1-64 矩阵后跑 421-428ms | **前置状态敏感**：排队行为依赖引擎当时的内部状态（KV cache 池碎片化、功耗/温度、prefix cache 内容等），burst 探针把这些差异放大；steady 闭环的"稳定"反而掩盖了它 | 注意事项（D13 实验纪律）：① burst 类实验必须记录前置状态（跑了什么、GPU 温度、是否重启过 pod）；② 关键对比实验前重启 pod 或固定实验顺序，消除顺序效应；③ 同一实验不同时间复跑一次，差异大则说明有未控制变量 |  


## TODO

### TODO-1：chunks vs completion_tokens 对比验证（D13 前完成）

背景：bench.py 的 TPOT 分母从 chunks 改成了 completion_tokens（chunks 系统性偏小 → TPOT 高估）。旧口径的偏差幅度从未实测过。

做什么：用存量 JSONL 即可，不用重跑压测——对比每个请求的 chunks 和 completion_tokens 字段，统计差异分布：

```bash
python -c "
import json
lines = [json.loads(l) for l in open('results/steady-c64.jsonl')][1:]
diffs = [r['completion_tokens'] - r['chunks'] for r in lines if r['error'] is None]
print(f'请求数 {len(diffs)}，差值分布: min {min(diffs)} / 中位 {sorted(diffs)[len(diffs)//2]} / max {max(diffs)}')"
```

预期：差值 ≥ 0（EOS 有 token 无 chunk + UTF-8 半字符滞留），中位数估计 1-5。若出现负值（chunks > tokens），说明还有没理解的机制，要追。

价值：定量回答"旧口径 TPOT 高估了多少"，D13 对比新旧数据时知道该不该做口径修正。

### TODO-2：CUDA graph capture 档位机制深入理解（D13 批处理参数扫描前完成）

背景：已知结论——capture 档位按 token 数计，上限 = min(max_num_seqs × decode_query_len × 2, 512)（源码 vllm/config/vllm.py:2207）；batch 落在档位间向上 padding，padding 空耗算力（c=52 实测）。但机制细节还没吃透。

要搞懂的三个问题：

1. PIECEWISE 和 FULL 两种 cudagraph mode 分别覆盖什么路径？（日志里 PIECEWISE 19 个 + FULL 2 个，为什么 FULL 只有 2 个？）
2. 混合批次（decode + prefill chunk）如何映射到 capture 档位？×2 余量具体怎么被消费的？
3. 扫 max_num_seqs（比如 64→128）时，capture 档位、显存占用、启动时间分别怎么变？改动前先用公式预测，改后用日志验证。

方法：pod 内源码就在 /usr/local/lib/python3.12/dist-packages/vllm/config/vllm.py（2200-2260 行是入口），配合启动日志逐行对。

价值：这是"配置 → 编译 → 性能"因果链的最后一环，D13 批处理参数扫描实验的解释框架依赖它。
