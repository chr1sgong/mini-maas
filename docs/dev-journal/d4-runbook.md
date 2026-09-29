# D4 Runbook — Reconcile：生成 Deployment/Service，回填 status

**验收事项：**

1. apply Model CR 后 Operator 自动创建 Deployment/Service；
2. status.phase 变 Ready、status.endpoints 回填；
3. 控制器日志可见 reconcile 过程

## Step 0 基础概念

### 0.1 Reconcile 是闭环，不是事件处理器

错误心智："CR 创建事件来了 → 我创建 Deployment"。正确心智："reconcile 被触发（原因不重要）→ 我读当前全量状态 → 让实际趋近期望"。

推论有三条，每一条都对应一类经典 bug：

1. **幂等**：跑两次和跑一次结果必须相同。第二次 reconcile 进来发现 Deployment 已存在且符合期望，就应该什么都不做——如果你的实现每次都 Update，会产生无休止的 resourceVersion 自旋。
2. **崩溃可恢复**：reconcile 可能在任何一行之后崩溃（创建了 Deployment、还没创建 Service 就 OOM 了）。下次进来必须能接着走，不能假设上次完成到哪一步。这就是为什么不许在内存里记"我创建过没有"。
3. **事件可丢**：controller-runtime 的 watch 事件只负责"叫醒" reconcile，不携带可信状态。错过一个事件不能导致状态永久不一致——level-based 的含义就是"我关心的是当前水位，不是水位变化的脉冲"。

### 0.2 OwnerReference：级联删除与反向触发

对子资源调 `ctrl.SetControllerReference(model, deployment, scheme)` 之后，能拿到两个能力：

* **GC 级联删除**：删 Model CR，API server 的垃圾回收器自动删它的 Deployment/Service。不需要再写删除逻辑。

* **反向 watch**：`SetupWithManager` 里 `Owns(&appsv1.Deployment{})` 让子资源的变化（被删、replicas 变化、状态更新）也触发父 CR 的 reconcile。这是"自愈"的来源：有人手删 Deployment，reconcile 被叫醒，重建。

注意 owner 与 owned 必须同 namespace（namespace 级资源的 ownerRef 不能跨 namespace），设计里 确保Model CR 和 vLLM 工作负载同 namespace。

### 0.3 Status 更新：独立端点、独立冲突域

CRD的设置中已开 status subresource，对当前的实际影响：

* 写 status 用 `r.Status().Update(ctx, model)`，写 spec 用 `r.Update()`，两者走不同 API 端点，互不覆盖。

* Status 更新照样会 409 Conflict（GET 之后有人改了对象，resourceVersion 过期）。标准处理：重新 GET 再试，或直接 `return ctrl.Result{Requeue: true}, nil` 让下一轮 reconcile 处理。**不要**把 409 当致命错误 return err 刷屏。

* 时序建议：先确保子资源到位，最后一步才写 status。中途写 status 会让 phase 撒谎。

### 0.4 子资源的"对比-更新"策略

三种主流写法对比：

| 策略 | 做法 | 代价 |
| --- | --- | --- |
| GET → 深比较 → 不一致才 Update | 自己写 diff 逻辑 | 比较逻辑易漏字段；默认值回填导致永远"不一致" |
| ✅ `controllerutil.CreateOrUpdate` | 官方辅助函数， mutateFn 里声明期望  | mutateFn 必须幂等；对 Deployment 这类有默认值的资源，只改拥有的字段 |
| Server-Side Apply | Patch with fieldManager | 声明式最干净，但 controller-runtime 里写法啰嗦，字段所有权语义需要理解 |

提醒一个隐蔽坑：Deployment 经 API server 准入后会被注入默认值（如 `dnsPolicy`、`restartPolicy`、`strategy.rollingUpdate` 的默认）。你 GET 回来的对象 ≠ 你创建的对象，直接 DeepEqual 必然 false。要么只比较你关心的字段，要么 CreateOrUpdate 的 mutateFn 只覆盖你的字段。

---

## ✅  Step 1 预检

```bash
cd operator
go build ./...          # 脚手架应已可编译
kubectl get crd models.maas.chr1sgong.io   # 确保 CRD 还在
kubectl get model -n mini-maas             # 确保样例 CR 还在（没有就重新 apply）
```

使用**本地跑**（`make run`，本机 kubeconfig 连 k3s）运行，不部署进集群。理由：改一行代码就重启一次进程，迭代快；日志直接打在终端。代价要想清楚一条：**本地跑用的是 admin kubeconfig，RBAC 漏洞全被掩盖**——`+kubebuilder:rbac` marker 写少了，本地永远不报错，部署进集群才暴露出来。所以 RBAC marker 要先写全（见 Step 3），`make manifests` 后检查 `config/rbac/role.yaml` 真的生成了对应规则。

* [x] 完成，`make run` 能启动（报 "unable to get" 之类先看 Step 5 踩坑区）

## ✅ Step 2 方案设计

| 决策点 | 方案 | 理由 / 不这么选的后果 |
| --- | --- | --- |
| 子资源命名规则（Model `qwen2-5-7b` → Deployment 叫什么）| 引擎 + 模型名称，例如 vllm-qwen2-5-7b | 引擎和模型方便用户一眼看出核心信息 |
| 子资源 labels 方案（至少哪些 label，谁用它们做 selector）| 至少有app, app.kubernates.io/managed-by, maas.chr1sgong.io/model 三个label | app标签给Service Selector用，app.kubenerates.io/managed-by区分资源所属，排查用，maas.chr1sgong.io/model 方便按模型查询资源，这是maas一个很常用的操作 |
| engineConfig → vLLM args 的映射函数长什么样（bool 字段 false 时怎么表达）| 手动遍历，往args里面添加 |  手动遍历能确保顺序，直接用map保证不了顺序，内容一致但结果可能不一致触发无效Update |
| status.phase 状态机：有哪几个值，各自进入条件 | Pending, Creating, Ready, Failed | Pending: Deployment还没创建，或者刚创建还没观测到；Creating: Deployment观测到了，但`/health`还不OK，拉镜像，加载模型，redinessProbe未通过的话，就是这个状态, Ready：模型就绪/health接口能调通；Failed: 终态错误，不支持的engine或Operator不支持的一些操作  |
| status.endpoints 填什么：Pod IP:port 还是 Service ClusterIP:port 还是 DNS 名 | Pod IP:port | 理由：路由策略要做到Pod级别的路由，而不是节点级别的，一个节点可能部署多个Pod副本，但是里面的kv cache不一样，要提高kv cache命中率，就需要找到对应的Pod，而不是在worker级别做负载均衡 |
| Deployment 更新策略：GET-比较-Update / CreateOrUpdate / SSA                | CreateOrUpdate |  三种策略的本质区别是“谁负责算出Diff”，CreateOrUpdate可以直接利用官方辅助函数，不用自己写diff逻辑  |
| update 已存在的 Deployment 时，replicas 字段覆不覆盖 | 覆盖 | Operator是Deployment spec.replicas字段唯一更新的地方，不然无法触发扩缩容  |
| 今天要不要 finalizer | 先不要 |  主流程是核心，有时间再做 |

### 6 个写代码前需要考虑清楚的问题

Q1. **args 生成的确定性**：engineConfig 如果是 map 或你用遍历方式拼 args，Go map 迭代顺序随机 → 同一 CR 两次 reconcile 生成不同 args 顺序 → Deployment 永远"有差异" → 无限滚动更新。你的生成函数怎么保证字节级确定？

A1. 关键是控制遍历方式，无论engineConfig是map还是其他struct，都只按照固定顺序解析arg。具体方式是，如果是map，先对所有的key排序，再遍历；如果是struct, 就手动解析，缺点是新增配置字段后，这个地方得更新上限，或者先把struct转成map，按照map的方式遍历，这样顺序就固定了。

Q2. **`enablePrefixCaching: false` 怎么表达？** vLLM 的 flag 是 `--enable-prefix-caching`（出现即开），没有 `--disable-prefix-caching`。bool false 时你的映射是"不传该 flag"还是传 `--no-enable-prefix-caching`？去查 vLLM 的 argparse 行为再回答，别猜。
A2. enablePrefixCaching: false对应的传--no-enable-prefix-caching. 这个结论就是从vLLM源码里找到的，具体代码是在`add_cli_args`函数中添加了"--enable-prefix-caching"，CacheConfig中定义了enable_prefix_caching的bool值，而bool参数使用的是argparse.BooleanOptionalAction, 这个action会同时添加一个"--no-enable-prefix-caching"的命令行参数识别器。这样只要传了"--no-enable-prefix-caching"就行了。

Q3. **endpoints 填 Pod IP 还是 Service？** 项目文档 4.2 的样例是 Pod IP（`10.42.0.15:8000`）。Pod 重建 IP 就变，Operator 得持续回填；Service ClusterIP 稳定但网关只能拿到一个入口，D8 的"多实例负载均衡 + prefix 亲和"需要在**实例粒度**做路由决策——Service 后面挂了几个 Pod、分别是谁，网关从哪知道？想清楚 D6/D8 要什么，再定今天填什么。
A3. endpoints填Pod IP. 网关路由可以只需要到集群维度，因为maas本身支持多租户、多模型，业务网关不需要感知到Pod粒度，但是推理服务层需要做这部分工作，例如prefix亲和性设计就需要直到kv cache在哪个pod，然后将对应请求打到有kv cache的pod上，提高kv cache命中率. 虽然Pod重建IP就变，Operator得持续回填，但推理服务不像业务服务会频繁上线，这个回填频率可以接收。但"Operator 持续回填"藏着一个机制问题：reconcile 什么时候被触发去回填？ 你的 Owns(&appsv1.Deployment{})  watch 的是 Deployment，不是 Pod。Pod 重建时 Deployment 的 readyReplicas 会 1→0→1 变化，能间接触发 reconcile——但这是间接的，时序上有窗口。想清楚两件事：
Operator 回填 endpoints 时要 list Pods（按 label selector 过滤 Running+Ready 的）——这意味着 RBAC 要加 pods 的 get/list，我 runbook 里只列了五组，这里补第六组。
回填后存在一个窗口期：旧 IP 已死、status 还没更新，网关照着旧 endpoints 打会 502。D6 网关要有重试/剔除死 endpoint 的兜底，这记在 D6 的账上，今天不用解决，但 design-notes 里记一笔。

Q4. **replicas 覆盖问题**：reconcile 更新已存在的 Deployment 时，如果无脑覆盖 `spec.replicas`，那么任何人 `kubectl scale` 都会被立刻改回去（这其实是声明式的特性）。但 autoscaler 是改 CR 的 `spec.serving.replicas`，链路是 CR → Operator → Deployment，中间没有打架方。反过来想：什么场景下"不覆盖"才是对的？（提示：如果将来有别的控制器直接改 Deployment）
A4. 核心原则是：任何一个字段，全系统只能有一个写入方。问题不是"要不要覆盖"，而是"这个字段归谁管"。当前架构下，autoscaler 改 CR 的 spec.serving.replicas，Operator 是 Deployment spec.replicas 的唯一写入方。所以 Operator 必须覆盖——不覆盖的话 autoscaler 改的 CR 值就落不到 Deployment 上，伸缩直接失效。kubectl scale 被改回去不是 bug，是声明式在正常工作。

Q5. **phase 状态机的粒度**：Pending/Creating/Ready/Failed 够吗？Deployment 存在但 Pod 还在拉镜像（实测首次 15 分钟）算什么？Pod Ready 但 vLLM 的 `/health` 还没过算什么？（提示：readinessProbe 已经帮你定义了"Ready"的口径，直接读 Deployment 的 Available condition 就是白嫖这个口径。）
A5. 只是跑当前链路是够的。phase状态主要是给人看和debug用的，理论上来说每个环节都对应一个状态最好，这样对Pod的运行状态更加清晰，比如拉镜像的阶段可以叫Loading，vLLM的`/health`还没过可以叫Init. 当前可以只要这几个状态先跑通整个系统。Pod Ready 但vLLM的`/health`没过这种状态不应该存在，因为readinessProbe就是看`/health`通过才认为Ready的，如果真的出现这种情况，需要看readinessProbe的逻辑了。

Q6. **Engine 字段的校验时机**：`spec.engine: vllm` 之外的值（比如将来 `sglang`），reconcile 怎么办？直接 Failed 并写 status，还是忽略？schema 层 enum 拦不住新引擎（枚举死了），reconcile 层的 default 分支就是唯一的防线。
A6. 直接Failed，但是要加监控告警。出现这种情况说明CRD上支持了sglang，但是Operator还没有对应的兼容逻辑，直接将status.phase设置成Fail，然后函数返回nil即可，通过监控告警让用户知道。正确的做法应该是先更新Operator兼容其他枚举，再更新CRD的。

## ✅ Step 3 实现 Reconcile

参考`operator/internal/controller/model_controller.go`实现

## ✅ Step 4 调试&测试

```bash
make run    # 终端常驻，日志即验收材料
```

另开一个终端：

```bash
kubectl delete model qwen2-5-7b -n mini-maas --ignore-not-found   # 从干净状态开始
kubectl apply -f deploy/models/qwen2-5-7b.yaml
kubectl -n mini-maas get deploy,svc,pods -w                        # 看子资源出现
kubectl get model qwen2-5-7b -n mini-maas -o jsonpath='{.status}'  # 看 status 回填
```

预期时间线：reconcile 秒级创建 Deployment/Service → Pod 拉镜像+加载权重（D1 实测数分钟，权重已有 hf-cache 会快很多）→ readinessProbe 通过 → Deployment Available → 下一次 reconcile（由 Owns 触发）把 phase 写为 Ready。

## ✅ Step 5 最终验收

```bash
# ① 主链路：apply CR → 子资源自动创建 → status Ready + endpoints 非空
kubectl get model qwen2-5-7b -n mini-maas -o jsonpath='{.status.phase}{"\n"}{.status.endpoints}'

# ② 幂等：强制再触发一次 reconcile（给 CR 打个无关 annotation），
#    观察 Deployment 的 resourceVersion 不变、日志无 Update 动作
kubectl annotate model qwen2-5-7b -n mini-maas test/trigger=$(date +%s)
kubectl -n mini-maas get deploy <name> -o jsonpath='{.metadata.resourceVersion}'   # 前后对比

# ③ 级联删除：删 CR → Deployment/Service 被 GC 自动删除（你不写任何删除代码）
kubectl delete model qwen2-5-7b -n mini-maas
kubectl -n mini-maas get deploy,svc     # 应全部消失

# ④ 热更新：重新 apply 后改 engineConfig（如 maxNumSeqs 64→32），
#    Deployment 被更新、Recreate 策略滚动、status 保持正确
#    （单卡 GPU 场景，观察新 Pod Pending→旧 Pod 被杀→新 Pod 起来的完整过程）

# ⑤ 自愈：保持 controller 运行，手动删 Deployment，观察它被自动重建
kubectl -n mini-maas delete deploy <name>
#    这一条是 level-based 的直接证据：你删的是"实际状态"，reconcile 发现与期望不符，重建
```

## Step 6 踩坑记录

| # | 坑 | 核心教训 |
| --- | --- | --- |
| 1 | args 切片展开 vs 字符串拼接 | 纯函数配单测，进集群前拦截 |
| 2 | buildDeployment 漏"不写就坏"字段 | 每个字段都要能说出"不写会怎样" |
| 3 | artifactURI 横杠 vs 点号 | schema 验形态，运行时验存在 |
| 4 | kubectl apply -f deploy/models/qwen2-5-7b.yaml 后容器起不来| 旧容器占据了gpu memory, 需要先清理。(EngineCore pid=273) ValueError: Free memory on device cuda:0 (3.76/23.52 GiB) on startup is less than desired GPU memory utilization (0.85, 19.99 GiB). Decrease GPU memory utilization or reduce GPU memory used by other processes. |
| 5 | CRD enum 没跟上状态机 | 谁先产生新值，谁的 schema 先放行 |
| 6 | mutateFn 没覆盖 Template  | CreateOrUpdate 的 GET 会覆盖传入对象；"首次 apply 成功"证明不了传导链 |
| 7 | Selector 漏赋值  | 验收要验证结果，不是验证"没报错" |
| 8 | 缺 Owns，phase 定格 Creating  | 触发与推导是两件事，分开排查 |
| 9 | 偶发 409 Conflict | 读走缓存写直连；偶发正常，刷屏才是环路 |

## 附：命令速查

```bash
# 控制器
cd operator && make run                              # 本地跑 controller（前台，日志即验收）
make manifests                                       # 改了 RBAC marker 后必跑，检查 config/rbac/role.yaml

# 观察 reconcile 结果
kubectl get model qwen2-5-7b -n mini-maas -o jsonpath='{.status}' | jq
kubectl -n mini-maas get deploy,svc,endpoints -l <你的label>
kubectl -n mini-maas get events --sort-by=.lastTimestamp | tail    # 事件流看触发顺序

# 强制触发 reconcile（不动 spec 的办法）
kubectl annotate model qwen2-5-7b -n mini-maas test/trigger=$(date +%s) --overwrite

# 验证 ownerRef / GC
kubectl -n mini-maas get deploy <name> -o jsonpath='{.metadata.ownerReferences}'

# 验证生成的 Deployment 与 D1 模板的差异（逐字段对，解释每一处不同）
kubectl -n mini-maas get deploy <name> -o yaml > /tmp/generated.yaml
diff <(yq eval '.spec' /tmp/generated.yaml) <(yq eval '.spec' deploy/manifests/vllm/vllm-qwen7b-awq.yaml)
```
