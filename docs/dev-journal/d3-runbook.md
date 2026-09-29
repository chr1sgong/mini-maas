# D3 Runbook — kubebuilder 脚手架 + Model CRD 定稿

验收：

1. Model CRD 安装进集群；
2. 样例 CR `kubectl apply` 成功且 `kubectl get model` 可见；
3. 非法 CR 被拒绝、默认值被注入（证明 schema 校验真的在工作）

## ✅ Step 0 概念课：理解CRD

### 0.1 CRD 是一张多方契约，不是配置文件

Model CRD 的 scheme 同时是四个组件的接口：

| 字段 | 写入方 | 读取方 | 用途 |
| ---- | ---- | ---- | ---- |
| `spec.artifactURI / engine / engineConfig` | 你（kubectl apply）或管理后台 | Operator |（翻译成 vLLM args/env） |
| `spec.serving.replicas` | **Autoscaler（唯一写入字段）** | Operator | 修改 Deployment replicas，实现最终的扩容 |
| `status.phase / status.endpoints` | Operator | 网关 | 网关用来做路由表 |

> 先以最小范围冻结这张表。每个字段都要能回答"谁写、谁读、缺省是什么、非法值长什么样"。回答不出的字段，今天就不要加。

### 0.2 spec / status 分界与 status subresource

* spec = 期望状态（人/autoscaler 写），status = 观测状态（只有 operator 写）。

* 必须启用 `+kubebuilder:subresource:status`：否则 operator 回填 status 时会和 autoscaler 改 spec 互相覆盖（同一条 Update 写整个对象）。开了 subresource 后 status 走独立端点，两边不打架，RBAC 也能分开授权。

### 0.3 声明式与 level-based reconcile

> Operator 的模式是"对比期望与实际，让实际趋近期望"，不是"响应事件执行动作"。事件（CR 变更、Pod 状态变化）只是**触发器**，reconcile 进来后读的是当前全量状态。

***

## ✅ Step 1 预检

```bash
go version                    # kubebuilder 4.x 需要 go 1.22+，建议与脚手架 go.mod 对齐
kubectl version --short 2>/dev/null || kubectl version
kubebuilder version           # 没有则装，注意下面版本问题
```

安装 kubebuilder：

```bash
# 方式一：直接下 release 二进制（推荐，版本可控）
curl -L -o kubebuilder "https://go.kubebuilder.io/dl/latest/$(go env GOOS)/$(go env GOARCH)"
chmod +x kubebuilder
sudo mv kubebuilder /usr/local/bin
# 方式二：go install sigs.k8s.io/kubebuilder/v4/cmd/kubebuilder@latest
```

## ✅ Step 2 CRD设计

与CRD设计相关的6个设计问题:

Q1. **artifactURI 为什么设计成 `hf://...` 带 scheme，而不是 `modelName: Qwen/...`？** 未来还有哪些 scheme（s3://、pvc://、本地路径）？ Operator 怎么解析它？——如果你想不清解析逻辑，scheme 就是过度设计，老实先用 modelName。

Q2. **engineConfig 里每个字段，能映射到 `deploy/manifests/vllm/vllm-qwen7b-awq.yaml` 的哪一行 args？** 逐个对一遍。映射不上的字段是删掉。反过来，YAML 里哪几行**不该**进 CRD（如 HF\_ENDPOINT、shm 大小）？为什么？

Q3. **`gpuMemoryUtilization` 用 float 还是 string？** CRD 对浮点的支持有坑（kubebuilder 里 float 序列化/默认值的老问题），KServe 和很多项目用 `resource.Quantity` 或 string 绕开。你的选择是什么，代价是什么？

Q4. **image 字段要不要进 spec？** 加了灵活性高，但"模型声明"就和"引擎实现细节"耦合了。

Q5. **status.endpoints 为什么存在？** 网关可以直接 watch Service/Endpointslices，为什么还要 Operator 回填一份？（提示：多模型共卡、未来跨节点时, endpoint 的来源会变化吗？）如果想不出理由，这个字段就是 KServe 抄来的赘肉。

Q6. **校验规则的边界**：`maxModelLen` 允许 0 吗？`replicas` 上限写多少（0.45+0.45 共卡最多几个实例）？`quantization` 用 enum 枚举 awq/fp8/none 还是自由 string？枚举死了将来加 GPTQ 怎么办？

| 字段 | 类型 | 谁写 | 谁读 | 默认值 | 校验规则 | 不写的后果 |
| --- | --- | --- | --- | --- | --- | --- |
| `spec.artifactURI` | string | 用户 | Operator | 无默认值 | 1. 非空； 当前只支持huggingface模式，hf:// | 镜像不知道怎么加载模型，无法运行起来 |
| `spec.engine` | string | 用户 | Operator | 无默认值 | 非空且在指定的枚举值上，当前仅支持\[vllm] | Operator不知道跑什么引擎，后续推理镜像就确定不了，推理服务跑不起来 |
| `spec.engineConfig.maxModelLen` | int | 用户 | Operator | 无默认值 | 大于0且小于一个理论最大值（这个可能需要查资料或者测试） | 推理引擎无法确认最常推理长度，直接报错 |
| `spec.engineConfig.maxNumSeqs` | int | 用户 | Operator | 无默认值 | 大于0 | 推理引擎无法确认最大批次长度，报错 |
| `spec.engineConfig.enablePrefixCaching` | bool | 用户 | Operator | True | bool值已经限制了输入 | 不写引擎默认为True |
| `spec.serving.replicas` | int | 用户和Autoscaler | Operator | 1 | >=0  | 不写就是默认值 |
| `spec.serving.gpuMemoryUtilization` | string | 用户 | Operator | 默认0.85 | (0, 1) | 系给默认值 |
| `status.phase` | string | Operator | 用户和网关 | 无默认值 | 固定的枚举值, 当前先确定\[Pending, Creating, Ready, Failed] 四个值 | 不知道endpoint ready前的状态，不好查问题，运维难度高 |
| `status.endpoints` | \[]string | Operator | 网关 | 无默认值 | ip:port形式 | 不知道pod地址，无法做有效路由 |

## ✅ Step 3 脚手架

在 repo 里（本仓库 `operator/README.md`）：

```bash
mkdir -p operator && cd operator    # 独立 go module，勿提到仓库根
kubebuilder init --domain maas.chr1sgong.io --repo github.com/chr1sgong/mini-maas/operator
kubebuilder create api --group maas --version v1alpha1 --kind Model --resource --controller
```

重点文件说明：

* `PROJECT` 文件——kubebuilder 的元数据，后续 create api 都改它

* `api/v1alpha1/model_types.go`——CRD通过它定义，然后通过`controller-gen`生成yaml文件

* `internal/controller/model_controller.go`——控制器，先保持空 reconcile

* `config/crd/bases/maas.chr1sgong.io_models.yaml`——`make manifests` 生成的产物

**验收标准:** `kubebuilder create api` 输出无报错，`go build ./...` 通过

## ✅ Step 4 写 types + 生成 CRD

按 Step 2 的设计改 `model_types.go`。（参考 kubebuilder book）：

* `+kubebuilder:subresource:status` 开在 type 上

* 校验用 marker 表达：`+kubebuilder:validation:Minimum/Maximum/Enum/Pattern/MinItems`

* 默认值用 `+kubebuilder:default:=...`；想清楚哪些字段**不该有默认值**

* `+kubebuilder:printcolumn` 加一些必要字段 （比如 Phase / Replicas / Age），`kubectl get model` 就能看到这些信息

* 可选字段用指针 + `omitempty`，必选字段不用指针——这个区分本身就是 schema 语义的一部分

```bash
make generate    # deepcopy
make manifests   # CRD YAML
```

**验收标准：**生成 CRD 里能看到全部校验规则、默认值、printer columns

## ✅ Step 5 安装CRD + 验收

```bash
make install     # 把 CRD apply 进 k3s（本质 kubectl apply -k config/crd）
kubectl get crd models.maas.chr1sgong.io
```

**验收：**

```bash
# ① 合法 CR 能 apply（参考样例：本仓库 deploy/models/qwen2-5-7b.yaml）
kubectl apply -f deploy/models/qwen2-5-7b.yaml
kubectl get model                      # printer columns 生效
kubectl get model qwen2-5-7b -o yaml   # 看默认值是否被注入（如 replicas）
```

## 踩坑记录

| 现象 | 原因 | 解法 |
|---|---|---|
| 更新 vLLM 镜像时 rollout 卡死：`error: deployment "vllm-qwen7b-awq" exceeded its progress deadline`；`get pods` 见新旧 Pod 并存——新 Pod Pending 14m+（Events：`FailedScheduling: 0/1 nodes are available: 1 Insufficient nvidia.com/gpu`），旧 Pod 一直 Running | 两层根因：① **单 GPU 不可超售 + RollingUpdate 先起新后杀旧**（默认 maxSurge 25% 向上取整=1，新 Pod 要 `nvidia.com/gpu: 1` 但唯一的卡被旧 Pod 占着，永远 Pending）；② 第一次修改 `strategy: Recreate` **未生效**（改的不是 apply 的那份文件/或放错层级——`strategy` 必须在 Deployment spec 下与 `replicas` 平级，放错位置 kubectl 只打一行 unknown field 警告就静默丢弃） | Deployment spec 加 `strategy: {type: Recreate}`（先杀旧释放 GPU 再起新，代价是中断几分钟，单卡别无选择）；apply 后必须 `kubectl get deploy -o jsonpath='{.spec.strategy}'` 确认生效再观察 rollout。**D4 Operator 生成 Deployment 时 Recreate 是标配**；D12 云机多卡可放宽 |
| 镜像"换不掉"：Deployment 声明 `v0.30.0`、`ctr images ls` 显示 v0.30.0→`8a69ffad`，但 Pod imageID 始终是 `d076d77d`，疑似一直跑旧镜像；删 Pod 重建也没用 | **不是旧镜像，是 digest 口径误判**。一个镜像有一族 digest：manifest list digest（`8a69ffad`，RepoDigests/imagetools/`ctr images ls` 口径）≠ config digest（`d076d77d`，`crictl images`/kubelet imageID 口径）。env-facts 当年记的是 config 口径，拿它跟 manifest list 口径比，必然"不一致"，误判成镜像漂移/旧镜像。registry 实证：v0.30.0 的 amd64 manifest 的 `config.digest` == `d076d77d`，两个 digest 同属一个镜像 | **比对 digest 前先确认两边同一口径**。排查路径存档：① deploy/pod 声明的 image ② `ctr -n k8s.io images ls`（list 口径）③ `crictl images`（config 口径）④ Pod Events 看 `Pulling`（真拉取了）还是 `already present`（用本地）⑤ 终审判据：`docker buildx imagetools inspect --raw <镜像>@<amd64 manifest digest>` 取 `config.digest` 比对 |
| 验收"默认值注入"时白测一轮：CR 里手写了 `replicas: 1`、`gpuMemoryUtilization: "0.85"`，存储对象里也有这些值，看似注入成功，实际全是自己写的，default 机制根本没被触发 | 判读方法缺失：存储对象里有某值 ≠ 该值是注入的 | 用 `kubectl.kubernetes.io/last-applied-configuration` annotation（apply 时提交的原文）与存储对象对比：**存储对象有、last-applied 没有的字段才是注入的**。验收默认值必须用"最小 CR"（只填必填字段，optional 字段全留空） |
| `kubectl get model` 报 `No resources found in default namespace`，以为 CR 丢了 | CR 建在 `mini-maas` namespace，查询没加 `-n` | `kubectl get model -n mini-maas`；CRD 是集群级、CR 是 namespace 级，查 CR 永远确认 namespace |


## 常见坑速查

| 症状 | 真实原因 | 解法 |
| --- | --- | ---- |
| `kubebuilder init` 报目录非空/已初始化 | 目录里有旧 PROJECT 文件或脚手架残留 | 换新目录；或确认是重新 init 场景后清理 PROJECT |
| `make manifests` 下载 controller-gen 卡住 | 国内网络拉 sigs.k8s.io 慢 | `GOPROXY=https://goproxy.cn,direct`；或预装 controller-gen 到 \$GOBIN |
| marker 写了但 CRD 里没有对应校验 | marker 位置错（字段注释 vs 类型注释）或拼写错，controller-gen 静默忽略 | 以生成的 CRD YAML 为准逐字段核对；`make manifests` 不会报这类错 |
| apply CR 报 `unknown field` | schema 与 CR 字段名不匹配（大小写/camelCase）| types.go 的 json tag 与 CR YAML 逐字对 |
| apply 成功但默认值没注入 | 字段是指针且 default marker 位置不对；或 CR 里显式写了 null | `kubectl get model -o yaml` 看准入后的实际存储对象 |
| `make install` 报 kustomize 版本/下载问题 | Makefile 自动下 kustomize 失败 | 手动 `go install sigs.k8s.io/kustomize/kustomize/v5@latest`，确认 `which kustomize` |
| controller-runtime 与 k3s v1.36 编译/运行不兼容 | 版本矩阵错位 | 查 controller-runtime README 的兼容矩阵，升降 kubebuilder 重新生成，不要手改 go.mod 硬凑 |

## 附：命令速查（按场景组织）

### 1. CRD / CR 操作

```bash
kubectl get crd models.maas.chr1sgong.io              # 确认 CRD 已安装
kubectl apply -f deploy/models/qwen2-5-7b.yaml        # 创建/更新 CR（坏 CR 验收也是这条，看报错）
kubectl get model -n mini-maas                        # 列表（printer columns: Phase/Replicas/Age）；CR 是 namespace 级，必须加 -n
kubectl get model qwen2-5-7b -n mini-maas -o yaml     # 看存储对象全文（验默认值注入、验 status）
kubectl delete model <name> -n mini-maas              # 删 CR
```

判读技巧：存储对象 vs `kubectl.kubernetes.io/last-applied-configuration` annotation（apply 原文）对比，多出来的字段才是 API server 注入的默认值。

### 2. Deployment / Pod 状态与排障

```bash
kubectl -n mini-maas get pods [-o wide] [-w]          # Pod 现场；-w 跟踪变化；AGE 辨新旧
kubectl -n mini-maas rollout status deployment/vllm-qwen7b-awq   # 等滚动更新；超时 ≠ 回滚，是卡住待介入
kubectl -n mini-maas describe pod <pod> | sed -n '/Events/,$p'   # Events 是排障第一现场（调度失败/拉镜像/探针）
kubectl -n mini-maas describe pod <pod> | grep -A2 "Image:"      # Image（声明的 tag）vs Image ID（实际 digest）挨着看
kubectl -n mini-maas logs <pod> | grep -m1 version               # 启动日志取版本行
kubectl -n mini-maas delete pod <pod>                            # 让 RS 重建（tag 解析只在创建时发生，改映射后必须重建才生效）
kubectl -n mini-maas delete pod <pod> --force --grace-period=0   # 卡 Terminating 时的强删（GPU 容器老毛病）
kubectl -n mini-maas port-forward svc/vllm-qwen7b-awq 8000:8000  # 调试后门：直连单个 Pod，绕开 Service/EndpointSlice，不看 readiness
```

### 3. jsonpath 精确取字段（排障核心武器）

```bash
# Deployment 声明层
kubectl get deploy -n mini-maas vllm-qwen7b-awq -o jsonpath='{.spec.strategy}'
kubectl get deploy -n mini-maas vllm-qwen7b-awq -o jsonpath='{.spec.template.spec.containers[0].image}'

# Pod 声明层
kubectl get pod -n mini-maas <pod> -o jsonpath='{.spec.containers[0].image}{"\n"}{.spec.containers[0].imagePullPolicy}'

# Pod 事实层（运行时真相）
kubectl get pod -n mini-maas -l app=vllm-qwen7b-awq \
  -o jsonpath='{.items[0].status.containerStatuses[0].imageID}'
```

记忆法：`.spec` 一路是**声明**，`.status` 一路是**事实**；排障就是找这两路的 gap。

### 4. 镜像相关

```bash
docker images --digests | grep vllm                              # 本地 tag → digest（含 RepoDigests）
docker inspect vllm/vllm-openai:latest --format '{{json .RepoDigests}}'   # manifest list 口径
docker buildx imagetools inspect vllm/vllm-openai:v0.30.0        # registry 上的 tag → manifest list → 各平台 manifest
docker buildx imagetools inspect --raw <镜像>@<平台manifest digest> \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['config']['digest'])"   # 终审判据：config 口径
docker tag vllm/vllm-openai:latest vllm/vllm-openai:v0.30.0      # 本地打锁定 tag
docker save vllm/vllm-openai:v0.30.0 | sudo k3s ctr images import -   # 导入 k3s containerd（导出含该镜像全部 tag）
sudo k3s ctr -n k8s.io images ls | grep vllm                     # containerd 视角（manifest list 口径）
sudo k3s crictl images | grep vllm                               # CRI/kubelet 视角（config 口径）——两个口径不同，勿互比
```
