# D6 Runbook — Go 网关：协议转发 + SSE 透传 + 路由

验收：**经网关调用、流式不断流**（口径见 Step 5），同时满足 P0 硬标准"官方 OpenAI SDK 可直接调用"
策略：今天表面上是"写个反向代理"，实质是三个重点问题

1. 流式响应的生命周期管理（缓冲/超时/断连传播），这是 MaaS 网关和普通 HTTP 网关的本质差异所在；
2. 路由表与控制面的衔接（watch Model CR status，而不是读 Service）；
3. 给 D7（鉴权限流）、D8（调度策略）留的接缝形状。代码量不大，接缝和概念错了才会卡。

## 工作方式（同 D5）

* 代码在你自己的 git 仓库里完成（gateway/ 独立 go module）；本仓库只放指导文档与参考样例（gateway/go.mod 仅为样例）。
* Step 2 的决策表填完拿给我 review，再动代码。
* 当日产出对标笔记一篇（LiteLLM Proxy / Higress AI 网关，≤30 行，固定三段结构），存 docs/design-notes/。

---

## Step 0 概念课

### 0.1 "不断流"要解决的三个问题：缓冲、超时、断连不传播

D2 的 baseline 全部是直连 vLLM 量的。从 D6 起，每一字节都要多过一站。**网关在流式路径上每引入一次缓冲，TTFT 就被污染一次**——而 TTFT 恰是 D13 全部实验的核心指标。三个问题分别说清楚：

1. **缓冲**：上游 chunk 到了网关，必须立即转发给客户端。httputil.ReverseProxy 有一个 FlushInterval 字段控制这个行为——但在设它之前，先去读 Go 1.23 源码里 ReverseProxy.flushInterval() 的实现，回答："对 Content-Type: text/event-stream 的响应，零值 FlushInterval 的实际行为是什么？vLLM 的 SSE 响应会不会命中这条路径？"想清楚再决定设不设、设什么。真正危险的缓冲往往不在 ReverseProxy 本身，而在你未来加的中间件里（gzip、统一响应日志、读完 body 再处理）。

2. **超时**：http.Server.WriteTimeout 覆盖的是**从请求头读完到响应写完的整个生命周期**——流式响应一次生成几十秒是常态，设了它等于给每条流挂定时炸弹。同理 Transport.ResponseHeaderTimeout 只等响应头，但 vLLM 排队时 TTFT 可以到秒级甚至十秒级，设小了会把排队中的请求误杀。把"哪些超时可以设、设多少、哪些绝对不能设"列进决策表。

3. **断连传播**：客户端中途断开（用户关页面、SDK 超时），网关必须让上游 vLLM 知道——否则 GPU 继续白跑一条没人看的生成，KV cache 被孤儿请求占着。Go 里这件事的开关是：发上游请求时用的是不是**进来那条请求的 context**。ReverseProxy 默认帮你做对了，但任何一处 context.Background() 脱钩都会悄悄打断这条链。验收时要拿 vLLM 日志证明 abort 真的发生了，不是"理论上会"。

### 0.2 路由表：为什么 watch Model CR，而不是用 Service

Operator 已经把你需要的数据算好了：status.endpoints 是"Running + Ready" Pod 的 PodIP:8000 有序列表（model_controller.go 第 5 步），随 reconcile 实时刷新。一个自然的问题是：**为什么不直接把请求丢给 Service ClusterIP，让 kube-proxy 去负载均衡？**

答案在 D8：RR 只是基线，最少在途和 prefix 亲和都需要**实例级寻址**——你得知道请求具体去了哪个 Pod，才能数在途、做粘性。Service 只有一个 VIP，LB 决策对你不可见。所以 D6 就把"实例级路由"这个架构事实定下来，CR status 是路由表的唯一数据源。

但要清楚这条链的新鲜度是有代价的：Pod 死亡 → readiness 探针发现（periodSeconds=10，最坏 +10s）→ Deployment status 变化触发 reconcile → CR status 更新 → 网关收到 watch 事件。**每一环的延迟量级是多少？期间打到已死 Pod 的请求会怎样？**今天只需要把现象记录下来（Step 5 有专项测试），但这个问题直接决定 D8 要不要在网关层自己做健康检查/熔断，思考题里展开。

watch 的实现方式三选一，决策表里填：client-go dynamic informer（轻，无需 codegen）/ controller-runtime cache（和 operator 同一套依赖，你熟）/ 手写 watch 循环（最裸，自己处理重连）。各自的重连、事件丢失、依赖体积权衡自己去查。

### 0.3 请求体窥探与模型名映射

OpenAI 协议里路由键在 **body** 里（model 字段），不在 URL 里。这意味着转发前必须读 body——读完记得原样放回去（Body 与 ContentLength 一起复原，少一个上游就 400/EOF）。只需要 model 一个字段，别把整个 schema 都 unmarshal 成强类型结构——网关对协议的了解越少，vLLM 升级字段时你越无痛。

映射关系零成本：Operator 起 vLLM 时用了 --served-model-name=<CR 名>，所以 **request 的 model 字段 == Model CR 名 == 路由表 key**。未知名 → 404；知名但 endpoints 为空 → 503。错误体按 OpenAI 错误格式（{"error": {"message": ..., "type": ..., "code": ...}}），官方 SDK 才能正确解析。

另一个容易被漏掉的端点：/v1/models。smoke_stream.py 的第一步就是 client.models.list()——网关掉这个，冒烟脚本第一行就死。聚合全部 Ready 的 Model CR 返回即可，注意 SDK 取的是 data[0]，两个模型时返回顺序要有确定性。

### 0.4 网关跑在哪

今天：宿主机进程，kubeconfig 连集群，直接打 PodIP（D5 冒烟已验证 PodIP 从宿主机可路由）。入集群（Deployment + RBAC + Service）留给 D9——那时 Prometheus 要抓网关指标，顺手落地。今天的代码要把"拿 kubeconfig 的方式"做成可切换（in-cluster 优先、fallback kubeconfig，或反过来），别写死。

网关本身无状态（RR 游标在内存里，思考题会追问这件事），这决定了它 D12 多节点部署时不需要任何改造——前提是今天别往里塞状态。

---

## Step 1 预检

    kubectl -n mini-maas get model -o wide                       # 两个模型 Ready，endpoints 非空
    kubectl get model qwen2-5-7b qwen2-5-3b -n mini-maas \
      -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.endpoints}{"\n"}{end}'
    python scripts/smoke_stream.py http://<endpoint7b>:8000/v1   # 直连仍正常（对照组）
    go version                                                    # ≥ 1.23
    kubectl config current-context                                # 网关将用这个 kubeconfig

* [x] 全部通过，把两个模型的 endpoints 记在下面（Step 5 对比用）

## Step 2 设计决策

写代码前填完，review 通过再动手：

| 决策点 | 选择 | 理由 / 不这么选的后果 |
|---|---|---|
| 代理实现：httputil.ReverseProxy 定制 vs 手写 io.Copy 循环 | httputil.ReverseProxy 定制 | httputil.ReverseProxy已经做好了很多底层工作,根据自己需求定制一方面工作量小,另一方面不会因为手写底层通讯逻辑触发不好排查的bug | 
| FlushInterval 设不设、设什么（先读 Go 1.23 源码 flushInterval() 再答） | | | 
| watch 方式：dynamic informer / ctrl-runtime cache / 手写 watch | | |
| 路由表快照的数据结构（读路径要不要加锁？热路径是每请求一次） | 读路径不要加锁 | 路由表是一个读多写少的结构, 读请求不要加锁,写请求要加锁 |
| Server 各项超时：哪些设、设多少、哪些绝不设 | | |
| Transport 连接池参数（MaxIdleConnsPerHost 默认值是多少？并发大于它时会发生什么？） | 默认值是2 | 超过它可能造成连接复用不足 |
| 404/503 的错误体格式 | | |
| D7 接缝：鉴权/限流中间件挂在处理链哪一环？TPM 需要在流式响应里数 completion token，旁路解析器插在哪一层？ | 在handler中添加健全/限流handler, TPM 在 Proxy返回数据的环节计数 | |
| D8 接缝：picker 抽象成什么接口？RoundRobin 的游标放哪？最少在途策略的各实例请求计数由谁维护，何时增减 | picker只需要一个Pick函数即可| 游标在具体的struct中保存, 最少在途策略是一种picker, 在自己的struct中维护请求计数|

## Step 3 骨架与路由表

1. gateway/ 独立 module：cmd/gateway/main.go + 必要的 internal 包，先只跑 /healthz。配置项：监听地址、namespace、kubeconfig 路径。
2. 路由表组件：watch Model CR → 维护 model名 → endpoints 快照 → **先把每次变化打日志**，不接任何请求逻辑。
3. 验证路由表（这是本 Step 的验收，过了再走）：
   * kubectl delete pod 删一个 vLLM Pod，记录从删除到网关日志出现新 endpoints 的耗时（这就是 0.2 节新鲜度链的实测值）；
   * kubectl delete model qwen2-5-3b，网关应观察到该 key 消失；重新 apply 后回来。

## Step 4 转发与流式透传

按顺序做，每一步都有独立的通过标准：

1. **非流式**：body 窥探取 model → 查路由表 → RR 选一个 endpoint → 反代。stream: false 的 curl 通，响应体与直连一致。
2. **流式**：stream: true，肉眼确认 chunk 是**逐条到达**的（不是憋到最后一次性吐出）。用 curl -N --no-buffer 观察。
3. **断连传播**：流式中途 Ctrl-C，看 vLLM 日志的 abort 记录与网关日志，证明链路传到了。
4. **/v1/models**：聚合 Ready CR，顺序确定。
5. **官方 SDK**：smoke_stream.py --base-url http://localhost:<port>/v1 全程通过（P0 验收口径）。注意脚本取 data[0] 的模型，两个模型都在时要能指定打哪个——怎么解决自己定（改脚本加参数 / 其他），记进日志。
5. **官方 SDK**：smoke_stream.py --base-url http://localhost:<port>/v1 全程通过（P0 验收口径）。注意脚本取 data[0] 的模型，两个模型都在时要能指定打哪个——怎么解决自己定（改脚本加参数 / 其他），记进日志。

## Step 5 验收（不断流的量化口径）

| # | 测试 | 通过标准 |
|---|---|---|
| 1 | 非流式调用经网关 | 响应正确，模型字段路由到对应实例 |
| 2 | 流式调用经网关 | chunk 逐条到达；data: [DONE] 正常收尾 |
| 3 | 官方 SDK 经网关 | smoke_stream.py 通过（含 models.list） |
| 4 | TTFT 网关税 | 并发 1 下，经网关 vs 直连的 TTFT 中位数差 < 5ms，记录数据 |
| 5 | 断连传播 | curl 中断后 vLLM 日志 ~1s 内出现 abort，网关无 goroutine 泄漏迹象 |
| 6 | 路由表新鲜度 | 删 Pod 期间打请求：记录失败请求的表现（状态码、是否重试）；新 endpoints 生效耗时 |
| 7 | 双模型隔离 | 7b 请求只到 7b endpoints，3b 只到 3b（vLLM 日志佐证） |
| 8 | 带载 sanity | bench.py --mode steady --concurrency 8 --duration 60 经网关，TTFT/TPOT/吞吐与 baseline 同数量级（完整对比是 D13 的事，今天只抓异常） |

## Step 6 收尾

* [ ] 决策表 + Step 5 数据填进本文档（你的仓库）
* [ ] 对标笔记：LiteLLM Proxy / Higress（≤30 行）存 design-notes
* [ ] dev-journal 记录：今天的坑、断连测试的 vLLM 日志原文、新鲜度实测数字
* [ ] 明日预告：D7 鉴权 + 双维限流，今天留的中间件接缝要真的能用上

## 思考题（review 时我会问）

1. Step 5-#6 里，请求打到刚死未摘牌的 Pod（连接拒绝）时，网关应该自动重试下一个 endpoint 吗？分三种情况讨论：非流式未出响应 / 流式未出首 token / 流式已出部分 token。各自的语义陷阱是什么？
2. D7 的 TPM 限流要求在流式响应里数 completion token。旁路解析 SSE chunk 的成本会不会污染 TTFT？怎么设计才能做到"可开关"——关掉时透传路径零开销？
3. RR 游标在网关内存里。如果 D12 把网关部署成 2 副本，会发生什么？这是 bug 还是 feature？
4. 新鲜度链（0.2 节）实测下来哪一环最慢？如果 D8 要求"摘牌延迟 < 2s"，你会改哪一环——改 Operator 的判定口径、改网关的健康检查、还是改 reconcile 触发源？
5. 网关对 OpenAI 协议的"了解程度"应该止于哪里？如果 vLLM 某天支持了 responses API 新端点，你的网关要改代码吗？

## 踩坑记录区（遇到即记，含报错原文与解法）

*（空着，今天填）*

## 常见坑速查

| 症状 | 真实原因 | 解法 |
|---|---|---|
| 流式变成一次性返回，TTFT≈总时延 | 某层在缓冲：中间件读完了 body / gzip / 手写 copy 没 Flush | 逐层排查谁碰了响应体；ReverseProxy 对 SSE 的默认行为读源码确认 |
| 流式在固定秒数被掐断 | http.Server.WriteTimeout 覆盖响应全生命周期 | 流式服务不设 WriteTimeout；只留 ReadHeaderTimeout/IdleTimeout |
| client 断了 vLLM 还在生成 | 上游请求没用 incoming request 的 context | 检查每一处 context.Background() / context.TODO() 脱钩 |
| vLLM 返回 400 / EOF | body 窥探后没复原完整（Body 或 ContentLength 缺一） | 两者一起重设；或在 ReverseProxy 的 Director/Rewrite 里统一处理 |
| 404 model not found 但 CR 明明在 | watch 没起来 / namespace 不对 / GVR 的 group 拼错 | 路由表变化打日志；对照 CRD models.maas.chr1sgong.io |
| smoke 脚本 data[0] 越界 | /v1/models 没实现或返回空 | 聚合全部 Ready CR |
| 经网关 P99 飙升、直连正常 | http.DefaultTransport 的 MaxIdleConnsPerHost=2，并发大于 2 反复建连 | 自建 Transport 调大连接池 |
| watch 事件延迟远超预期 | 先看是不是 operator 没 reconcile（新鲜度链逐环定位），再怀疑网关 | 0.2 节逐环打时间戳 |

## 附：D6 命令速查

    # 路由表数据源
    kubectl get model -n mini-maas -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.phase}{"\t"}{.status.endpoints}{"\n"}{end}'
    kubectl get model qwen2-5-7b -n mini-maas -o yaml -w        # 盯 status 变化

    # 经网关调用（端口以你的配置为准）
    curl http://localhost:8080/v1/models
    curl http://localhost:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{
      "model":"qwen2-5-7b","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":false}'
    curl -N --no-buffer http://localhost:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{
      "model":"qwen2-5-7b","messages":[{"role":"user","content":"数数到50"}],"max_tokens":128,"stream":true}'

    # 冒烟与带载
    python scripts/smoke_stream.py http://localhost:8080/v1
    python bench/bench.py --base-url http://localhost:8080/v1 --model qwen2-5-7b \
      --mode steady --concurrency 8 --duration 60 --md docs/baseline/gateway-d6.md

    # 新鲜度测试
    kubectl -n mini-maas delete pod -l app=qwen2-5-7b            # 同时盯网关日志与 CR status
