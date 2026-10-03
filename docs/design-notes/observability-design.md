# 设计文档 - 可观测：Prometheus/Grafana + vLLM/网关指标 

## 一、框架与流程

### 1.1 今天在整条链路里的位置

D6-D8 把请求路径修通了，但整条链目前是**盲跑**：压测时只有 bench 客户端在自己记数，服务端内部——排队多深、KV cache 水位、两副本是否真均分、亲和命中没有——全靠肉眼扫日志。D9 要把"接入 → 调度 → 推理 → **度量**"闭环的最后一条腿接上，验收"压测曲线可见"的含义是：压测进行中就能在 Grafana 看实时曲线，而不是压完看客户端报告。

这一天还是两个后续日子的地基：**D11 autoscaler 的感官**就是今天铺的指标（它从 Prometheus API 读排队深度和延迟，不会再装一套采集）；**D13 实验日**三件套里的"曲线"全部出自今天的面板和 API 落盘。

一个观念转变从今天开始：bench 的数是"用户视角"，Prometheus 里的数是"系统视角"，**两边对得上账，压测-看板闭环（D10 里程碑）才算成**。所以今天的核心纪律不是面板好看，而是口径先行（Q5/Q6/Q12）。

### 1.2 数据流：pull 模型 + 三方同源

```
vLLM pod   :8000/metrics   （vLLM 原生，今天零代码）
gateway    :8080/metrics   （今天新增，client_golang）
dcgm       :9400/metrics   （GPU，DaemonSet）
     ▲ 每 5~15s scrape（annotation 发现 pod）
Prometheus（集群内 Deployment，TSDB 落 local-path PVC）
     ▲ PromQL
Grafana（dashboard-as-code，provisioning 加载 JSON）
     ▲ HTTP API /api/v1/query_range
D10/D13：export 脚本 → CSV 落盘（实验数据的 source of truth）
```

选 pull 不选 push，和 D8 选 informer 同构：实例上下线由 service discovery 自动跟随，**D11 扩出来的新副本自动被抓，不用改任何配置**。今天定抓取间隔时要记住 autoscaler 在看——间隔就是它反应速度的下限（Q3）。

"三方同源"是今天的核心纪律：网关、引擎、bench 三个视角的 TTFT/TPOT/吞吐必须能对账，所以指标命名和标签口径先于面板。

### 1.3 组件分解

| 组件 | 位置（你的仓库） | 职责 | 状态 |
|---|---|---|---|
| Prometheus | `deploy/manifests/observability/prometheus.yaml` | Deployment + RBAC + PVC + scrape ConfigMap | 新建 |
| Grafana | `deploy/manifests/observability/grafana.yaml` | Deployment + provisioning（datasource/dashboard） | 新建 |
| MaaS Overview 面板 | `deploy/manifests/observability/dashboards/maas-overview.json` | 一屏六行（Q9） | 新建 |
| dcgm-exporter | `deploy/manifests/observability/dcgm-exporter.yaml` | GPU 指标 DaemonSet（Q8） | 新建 |
| 网关指标包 | `gateway/internal/metrics/` | 指标定义集中地，别处只调 Inc/Observe | 新建 |
| 网关埋点 | handler / picker 装配处 / transport / ratelimit | 请求、TTFT/TPOT、路由、限流计数 | 改造 |
| vLLM 抓取注解 | operator `buildDeployment`（model_controller.go） | pod template 加 prometheus.io/* 注解 | 改动 |
| 指标字典 | `docs/observability/metrics.md` | 三视角口径对账表（Q12） | 新建 |

### 1.4 实施顺序

1. **先验证别人的指标**：port-forward 一个 vLLM pod，`curl :8000/metrics | grep ^vllm`，把 v0.30 实际的指标名记下来（Q4）——面板和指标字典全部以这份实测为准，不猜。
2. **最小可见**：Prometheus + Grafana 部署起来，先只配 vLLM 一个 job，UI 里看到曲线再继续。这一步给的是信心和 PromQL 手感。
3. **网关埋点**（今天唯一的自研代码量）：指标包 → transport 计时 → handler/picker/ratelimit 计数 → `/metrics` 放行。
4. **dcgm-exporter**：便宜但别在第一小时碰，它是今天的甜点。
5. **面板组装 + 压测验收**：D2 steady 参数压 10 分钟，三方对账。

## 二、重点问题与参考答案

### Q1 债：`config.json` 缺 `routing` 节，网关启动即 fatal

D8 收尾留的坑：`NewPicker` 对空 strategy 返回 error，而 `config.json` / `gateway.e2e.json` 都没有 `routing` 节，`loadConfig` 也没给默认值——按现状启动网关会 `failed to create picker: unknown picker strategy: ""`。**先补这个**（D8 设计 Q9 的形状：`"routing": {"strategy": "prefix_affinity", "affinity_bytes": 256, "max_affinity_inflight": 8}`）。顺手把 `loadConfig` 里 `cfg.Listen = ":8080"` 这行覆盖 JSON 值的硬编码清掉，它和 `defaults` 下 JSON 写 `max_tokens` 而结构体读 `max_completion_tokens` 一样，都是 D7 留下的小毛边（不处理就记 roadmap）。今天 Q5 的路由分布指标依赖 picker 真的在跑。

### Q2 为什么手搓 YAML，不上 kube-prometheus-stack？

参考答案：**手搓 minimal manifests**。kube-prometheus-stack 的价值在 Prometheus Operator（ServiceMonitor CRD）、Alertmanager 和一整套 K8s 组件面板——我们的 target 一共三类、告警本期不做，Operator 那层控制循环是多余复杂度；helm 生成的资源层不可审查，违背 D14"30 分钟复现"。全项目至今都是 plain YAML，observability 不破例。原语（scrape_config / relabel / provisioning）和 stack 里完全一样，回头在生产见到 stack 不陌生。切换成立的条件：target 多到需要 CRD 管理、要告警、多集群——D12 多云节点也仍是 plain YAML 就够。

### Q3 Prometheus 怎么部署？存储和抓取间隔怎么定？

- 单 Deployment（不是 StatefulSet：无 HA、无远程写需求），镜像 pin 具体 tag，理由同 vLLM 锁 `v0.30.0`。独立 namespace `observability`，删除方便。
- 存储：k3s 自带 local-path provisioner 出 PVC，20Gi，`--storage.tsdb.retention.time=7d`。不用 emptyDir——pod 重启丢曲线，实验日不能接受；但也别迷信 TSDB，**实验数据的 source of truth 是 API 落盘**，TSDB 只是工作台。
- 资源：active series 量级约 1 万，requests 500m/1Gi、limits 2Gi，宽裕。
- 抓取间隔：**vLLM job 5s，其余 15s**。5s 的理由不在今天：D11 autoscaler 从 Prometheus 读排队深度，抓取间隔是它反应速度的下限，"突发扩容 < 90s"的预算里包含它。vLLM /metrics 输出几百行，5s 抓一次的代价可忽略。
- UI 无鉴权：Prometheus/Grafana 都 ClusterIP + `kubectl port-forward` 访问，单节点本地够用；生产暴露方式记 roadmap。

### Q4 vLLM 指标怎么接？（今天最大价值、零业务代码）

第一步永远是实测：`kubectl -n mini-maas port-forward <vllm-pod> 8000`，`curl localhost:8000/metrics | grep ^vllm`，把 v0.30 的实际指标名存进指标字典（Q12）。预期骨干（vLLM v1 引擎系名字，历史上漂移过，以实测为准）：

- `vllm:num_requests_running` / `vllm:num_requests_waiting` —— 批内/排队深度，autoscaler 主输入
- `vllm:kv_cache_usage_perc` —— KV cache 水位，抢占前兆（v0.30.0 实测：旧名 `gpu_cache_usage_perc` 已漂移）
- `vllm:time_to_first_token_seconds` / `vllm:request_time_per_output_token_seconds`（histogram）—— 引擎侧 TTFT/TPOT（v0.30.0 实测：TPOT 旧名 `time_per_output_token_seconds` 已漂移；另有 `vllm:inter_token_latency_seconds`）
- `vllm:prompt_tokens_total` / `vllm:generation_tokens_total` —— 吞吐
- `vllm:e2e_request_latency_seconds`、前缀缓存命中相关计数（名字漂移过几轮，实测确认）

发现机制参考答案：**annotation + kubernetes_sd**，不是按 label 硬筛。operator 的 `buildDeployment` 给 pod template 加注解（改完 apply Model CR 触发 reconcile 重建 Deployment 才生效）：

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8000"
```

scrape 配置（prometheus.yaml 的 ConfigMap 里）：

```yaml
- job_name: vllm
  scrape_interval: 5s
  kubernetes_sd_configs:
    - role: pod
      namespaces: { names: [mini-maas] }
  relabel_configs:
    - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
      action: keep
      regex: "true"
    - source_labels: [__meta_kubernetes_pod_ip]
      target_label: __address__
      replacement: $1:8000
    - source_labels: [__meta_kubernetes_pod_name]
      target_label: pod
    - source_labels: [__meta_kubernetes_pod_label_maas_chr1sgong_io_model]
      target_label: model
```

两个要点：

- relabel 后 `instance` 默认是 `podIP:8000`——**和网关 `X-Maas-Endpoint`、route_decisions 的 endpoint 标签同口径**。这不是巧合，是设计：路由分布（网关侧）和实例饱和度（引擎侧）能用同一个 key join，D13 调度实验靠这个对图。
- RBAC 别忘了：Prometheus 的 ServiceAccount 需要 pods 的 get/list/watch（ClusterRole）。targets 页看不到东西时，先查 Prometheus UI 的 /service-discovery 看 dropped targets，再查 RBAC。

### Q5 网关埋点：指标清单与标签纪律

参考答案（namespace `maas`，定义集中在新的 `internal/metrics` 包，业务代码只调 Inc/Observe，不散落 promauto）：

| 指标 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `maas_gateway_requests_total` | counter | model, tenant, code | handler 出口统一记 |
| `maas_gateway_request_duration_seconds` | histogram | model, stream | 网关视角 e2e |
| `maas_gateway_ttft_seconds` | histogram | model | 流式首 chunk（Q6） |
| `maas_gateway_tpot_seconds` | histogram | model | chunk 间隔均值 |
| `maas_gateway_route_decisions_total` | counter | strategy, reason, endpoint | D8 留的缝，picker 计数导出 |
| `maas_gateway_inflight_requests` | gauge | endpoint | tracker 导出 |
| `maas_gateway_ratelimit_rejected_total` | counter | tenant, dimension | 429，dimension ∈ qps/tpm |

标签纪律（每个标签过一遍基数检查）：model/tenant/code/reason/strategy 是低基数枚举，允许；endpoint 基数 = 副本数，允许；**affinity key、request id、prompt 等任何无界值，禁止**——无界 label 是把 TSDB 打爆的经典事故。code 取"响应开始时"的状态码；流式中途断连不单独立类，e2e 长尾里看得见。

histogram 桶参考答案：TTFT `{0.05,0.1,0.2,0.4,0.8,1.6,3.2}`，TPOT `{0.01,0.02,0.04,0.08,0.16,0.32}`，e2e `{0.1,0.5,1,2,5,10,30}`。桶多了只是多几个计数器；桶设错导致 P99 落在最大桶外算不出来，才要命——照 D2 baseline 的实测范围设。

### Q6 TTFT/TPOT 在哪测？

参考答案：**transport 层 tee**，就是 D7 metering 的那个 `teeBody`——它本来就能看到上游响应每个 chunk 的到达，加时间戳即可：

- TTFT = 首个 chunk 到达时刻 − RoundTrip 发起时刻（上游排队 + prefill + 首 token）
- TPOT = chunk 间隔均值（SSE chunk ≈ token，近似值；引擎侧 `vllm:time_per_output_token_seconds` 才是权威）

两个坑要绕开：

1. **观测不能挂在 metering 开关上**。现在 `teeBody` 只在 `HasSettle(ctx)` 时创建——`metering: false` 就没有 TTFT 了，不可接受。改成 tee 恒创建（观测是义务），`SettleFromContext` 仍按开关走（结算是功能）。
2. **口径命名别混**。从此有三套 TTFT：bench 客户端的（用户视角）、网关 tee 的（`maas_gateway_ttft_seconds`）、vLLM 引擎的（`vllm:time_to_first_token_seconds`）。单机环境下三者差值 = 网关处理 + 本机网络，应稳定且小；D10 口径对齐就是拿这三数对账。今天把名字取对、定义写进指标字典，D10 就没坑。

### Q7 `/metrics` 端点怎么挂？（auth 会拦）

现状：`middleware.Chain` 把整个 mux 包进 auth；auth 里硬编码放行了 `/v1/healthz`。`/metrics` 直接挂 mux 会被 401。

参考答案：趁今天把规则理顺——**auth 只守护 `/v1/` 业务前缀，infra 端点一律挪出 `/v1`**：`/v1/healthz` 挪成 `/healthz`，`/metrics` 同级。auth 的特例判断从"放行某路径"改成"非 `/v1` 前缀直接放行"，以后加 `/debug/pprof` 也不用再动 auth。网关是宿主进程、healthz 目前没有探针在调，挪路径零成本；比"每加一个 infra 端点改一次 auth 白名单"干净。

### Q8 GPU 指标：dcgm-exporter，以及"GPU 利用率没你想的有用"

部署要点（DaemonSet，port 9400）：

- 镜像 `nvcr.io/nvidia/k8s/dcgm-exporter`（pin tag），env `NVIDIA_VISIBLE_DEVICES=all` + `NVIDIA_DRIVER_CAPABILITIES=utility`。
- **绝不 request `nvidia.com/gpu`**——time-slicing 只超发 2 份，两份都给了 vLLM 双副本，dcgm 再要就把副本挤掉。dcgm 只读驱动计数器，不占 device-plugin 配额。
- 4090 消费卡能拿到：`DCGM_FI_DEV_GPU_UTIL`（采样%）、`DCGM_FI_DEV_FB_USED/FREE`、功耗、温度；细粒度 `PROF_*`（SM_ACTIVE 等）是数据中心卡专属，拿不到，无所谓。
- 备选：`utkuozdemir/nvidia_gpu_exporter`（NVML 直读，更轻），dcgm 在消费卡上闹脾气时换它。

认知重点（写进面板标题，防 D13 误读）：**连续批处理下 GPU util 要么 0 要么 ~100%，它回答不了"还能塞多少"**。容量信号在 vLLM 指标里：`num_requests_running / max_num_seqs`（批满没）、`num_requests_waiting`（排队多深）、`kv_cache_usage_perc`（KV 水位；v0 旧名 `gpu_cache_usage_perc` 已漂移，v0.30.0 实测无）。GPU util/FB_USED 的价值是共卡干扰视角（D13 共卡实验）和成本视角。

### Q9 Dashboard：一屏六行，dashboard-as-code

参考答案：一个 dashboard "MaaS / Overview"，行序 = 请求链路自外而内：

1. **Traffic**：`sum by (model) (rate(maas_gateway_requests_total[1m]))`；429 rate
2. **Latency**：`histogram_quantile(0.99, sum by (le, model) (rate(maas_gateway_ttft_seconds_bucket[1m])))`——网关 TTFT/TPOT 与 vLLM 同名指标**并排画**，口径差直接可见
3. **Throughput**：`sum by (instance) (rate(vllm:generation_tokens_total[1m]))`（tok/s 按副本）
4. **Routing**：`sum by (endpoint) (rate(maas_gateway_route_decisions_total[1m]))` 堆叠图 + 亲和命中率 `sum(rate(maas_gateway_route_decisions_total{reason="affinity"}[5m])) / (sum(rate(maas_gateway_route_decisions_total{reason="affinity"}[5m])) + sum(rate(maas_gateway_route_decisions_total{reason="affinity-fallback"}[5m])))`——D8"分布可见"验收的图形化兑现
5. **Saturation**：`vllm:num_requests_running` / `vllm:num_requests_waiting` by instance；`vllm:kv_cache_usage_perc`
6. **GPU**：`DCGM_FI_DEV_GPU_UTIL`、`DCGM_FI_DEV_FB_USED`

纪律：JSON 存 `deploy/manifests/observability/dashboards/`，ConfigMap + Grafana provisioning 挂载加载；**UI 里手改完必须 export 回 JSON 再提交**，否则 D12 上云就是一堆手工状态。Grafana 开匿名 admin（`GF_AUTH_ANONYMOUS_ENABLED=true` + `GF_AUTH_ANONYMOUS_ORG_ROLE=Admin`），本地便利优先。

### Q10 网关在宿主机，Prometheus 在集群里，怎么抓？

参考答案：**今天网关不进集群**。scrape config 加一个 `static_configs` job 指宿主机 IP:8080（`kubectl get node -o wide` 的 INTERNAL-IP）。理由：今天 main.go 还在高频改（埋点迭代），进集群后每次改代码要 build 镜像 + import，反馈环从 10 秒变 3 分钟。网关 Deployment 化是 D11 收尾/D12 上云的前置项，记进接缝。坑提醒：宿主机 IP 换网络环境会变，在 scrape config 注释里写明怎么查。

### Q11 埋点的性能账（D8 回归线延续）

client_golang 每次 Inc/Observe 是 atomic + map 查 metricVec，微秒以下。热路径真正的三个坑：每次 `WithLabelValues` 重复构造 label 集（低基数标签启动时预取；endpoint 集合随扩缩变化，miss 时兜底再取）、热路径打日志（D8 已立规）、无界 label（Q5 已立规）。回归线不变：**埋点前后各跑一次 D2 steady baseline，网关税 < 5ms**；`go test ./... -race` 全绿（teeBody 多了写指标的路径，race 重点盯它）。

### Q12 指标字典先行 & 今天不装什么

- 开工先建 `docs/observability/metrics.md` 骨架，每个指标三列：名字/标签/口径（用户·网关·引擎哪个视角、单位、怎么算的）。它是三份产物的源头：面板的 PromQL 从它抄、D10 拿它对账、D13 实验报告引它。边做边填，别指望周末补。
- 不装 Alertmanager（告警本期不做）、node-exporter/kube-state-metrics（CPU/内存 `kubectl top` 够用，k3s 自带 metrics-server，别为面板好看养 series）、Loki/ELK（`log.Printf` + `kubectl logs` 撑到项目结束）。

## 三、给后续日子留的接缝

- **D10（口径对齐）**：指标字典（Q12）是输入；export 脚本（query_range → CSV）明天写，今天验收只需 UI 可见 + 三方数值同量级。
- **D11（autoscaler）**：输入 = `vllm:num_requests_waiting/running` + 网关 TTFT P99，从 Prometheus HTTP API 读；Q3 的 5s 抓取间隔就是为它的反应预算定的。扩出来的新副本被 annotation 自动发现——Q4 的复利。
- **D12（上云）**：observability 全套 plain YAML 原样部署；dcgm DaemonSet 天然多节点；网关 Deployment 化后 static_configs 换成 annotation 发现。
- **D13（实验日）**：5.4 全部实验的"曲线"出自本面板 + API 落盘；prefix 实验的命中率 panel（Q9 第 4 行）今天已就位。

## 四、延伸阅读

按"今天就要用"到"回头再看"排序：

1. **vLLM metrics 官方文档** — 指标清单与口径的权威，版本漂移以它+实测为准：<https://docs.vllm.ai/en/latest/usage/metrics.html>
2. **vLLM 源码 `vllm/v1/metrics/`** — 它怎么实现指标（per-request histogram 在引擎循环里聚合、前缀缓存计数位置），今日对标笔记候选
3. **Prometheus relabel_config 文档** — Q4 的规则照它写；调试用 /service-discovery 页看 dropped targets：<https://prometheus.io/docs/prometheus/latest/configuration/configuration/#relabel_config>
4. **Prometheus histogram 实践** — 为什么必须先 rate 再 histogram_quantile、为什么 P99 精度取决于桶边界（线性插值）：<https://prometheus.io/docs/practices/histograms/>
5. **Grafana provisioning** — datasource/dashboard as code 官方机制：<https://grafana.com/docs/grafana/latest/administration/provisioning/>
6. **RED / USE 方法** — Q9 六行布局的理论出处（Tom Wilkie 的 RED：Rate/Errors/Duration；Brendan Gregg 的 USE：Utilization/Saturation/Errors）
7. **OTel GenAI 语义约定** — `gen_ai.*` 里 TTFT/TPOT/usage 的生产口径，我们不全盘采用但定义向它对齐，今日对标笔记候选：<https://opentelemetry.io/docs/specs/semconv/gen-ai/>
8. **dcgm-exporter 字段支持矩阵** — 消费卡能拿到哪些字段：<https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html>
9. **kube-prometheus-stack** — 没选它的对照组，扫一眼 values.yaml 感受它替你做了多少事：<https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack>

当日对标笔记：方向 2（vLLM 指标设计本身）或 7（OTel GenAI 口径）任选其一，≤30 行三段式，存 `docs/design-notes/`。
