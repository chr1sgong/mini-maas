# D7 Runbook — 网关：鉴权 + QPS/TPM 双维限流

验收：**401/429 行为正确**（量化口径见 Step 6），且鉴权+限流全开后 TTFT 网关税仍 < 5ms
策略：今天表面是"加两个中间件"，实质是一场时序考试——**限流的判定时刻与成本的知晓时刻不匹配**。QPS 的成本在请求进门时已知（恒为 1），TPM 的成本在响应走完才确知。怎么把"事后才知的成本"塞进"事前就要做的判定"，是 MaaS 限流与普通 HTTP 限流的本质差异。另一个考点是 D6 思考题 2 的兑现：旁路数 token 不能污染 TTFT。代码量依然不大，但今天的每一个"先这样吧"，都会在 D11 autoscaler 和 P1 多租户配额那里变成债。

## 工作方式（同 D6）

* 代码在你自己的仓库 gateway/ 内完成；本仓库只放指导文档。
* Step 2 问题与方案答完拿给我 review，通过再动代码。
* 当日产出对标笔记一篇（LiteLLM 的 tpm/rpm 限流：预检/结算时机、窗口计数 vs 令牌桶，≤30 行，固定三段结构），存 docs/design-notes/。
* D6 欠的 design-notes/gateway.md 还空着，今天一并补上或记 roadmap。

***

## Step 0 概念课

### 0.1 中间件次序：先认证、后限流——用日志证明，别猜

main.go 留的接缝是 `Chain(mux, Logging, /* Auth, RateLimit */)`。动手之前先回答：**按 Chain 的实现，请求实际经过中间件的顺序是什么？**临时挂两个只打日志的中间件跑一次，用输出说话。你会发现一个事实：包裹顺序 ≠ 执行顺序。

次序为什么重要：

* 限流的键是身份。per-key 限流必须先知道"你是谁"，桶才有名字。身份从 Auth 传到 RateLimit 走 context value，键类型自定义（未导出的 struct{}），避免字符串键冲突。
* 未认证请求消耗不消耗限流额度？如果限流跑在认证外层，任何人拿假 key 猛打就能耗尽真用户的配额——一次免费的 DoS。结论：**认证必须在限流外层，401 的请求不得触碰任何桶**。
* Logging 该在最内还是最外？它现在记的时长含不含未来 D8/D11 的排队？今天用 Allow 无排队，但埋点口径要想在前面。

### 0.2 令牌桶语义：x/time/rate 只进不出，而 TPM 需要"退款"

令牌桶三要素：容量 capacity、补充速率 rate、当前余额。`Allow()` 语义是"有就扣、没有就拒"——**拒绝不消耗令牌**（想清楚：如果 429 也扣令牌会发生什么？惩罚效应，越被限越翻不了身）。

去读 golang.org/x/time/rate 的公开 API 列表，回答：**它有没有"事后返还 n 个令牌"的方法？**没有。`Reservation.Cancel` 能反悔一次预留，但语义绕、且要求先 Reserve。这就是 TPM 维要自研 bucket 的正当理由——不是造轮子，是标准库的语义配不上"预检估算、事后按实际结算"这个模型。自研 bucket 四十行以内：mutex + tokens float64 + lastRefill time.Time，方法集 Allow / AllowN / Refund。三个单测必须过：补充速率正确、扣减不超额、**Refund 封顶 capacity**（退款把桶灌爆是经典 bug）。

QPS 维可直接用 x/time/rate（成本恒为 1、无结算），也可与 TPM 共用自研实现——问题与方案里定，说清理由。

### 0.3 TPM 的时序问题：预检估、结算实、容忍负余额

TPM 判定的全部困难在于：请求进门时你不知道它花多少 token。拆开看：

* prompt tokens：进门时理论可估，但你没有 tokenizer（为估数引入 tokenizer 依赖是本末倒置）。粗估方案：body 字节数 / 4；或干脆不估、预检只算生成侧。
* completion tokens：响应走完才确知；客户端传的 max_tokens 是上限不是实际。

所以语义只能是：**预检扣估算值 est，结算按实际值 actual 回填差额（est − actual）**。est 怎么取（prompt 估不估、max_tokens 缺省时的默认值）写进问题与方案——没有标准答案，但"语义定义"必须白纸黑字，D10 指标对齐时要引用它。

actual 从哪来：

* 非流式：响应 JSON 的 usage 字段（旁路读，见 0.4）。
* 流式：vLLM 默认 chunk 不带 usage；OpenAI 协议有 `stream_options.include_usage`，vLLM 支持——**网关给上游请求注入 include_usage=true**，最后一个 chunk（choices 为空）即 usage。客户端会因此多收一个 usage chunk：这是官方功能的正常形态，官方 SDK 兼容，但写进验收实测。客户端自己传了 stream_options 怎么办？合并，不覆盖。

如果 actual > est（客户端没传 max_tokens 却生成超长），允许余额变负吗？建议允许——负余额即"欠债"，靠自然补充偿还，语义诚实、实现零分支。这也是令牌桶相对固定窗口计数的优势：窗口在边界处会双重扣费，桶不会。

### 0.4 旁路的零污染纪律（D6 思考题 2 的兑现）

usage 在响应体里，而响应体正在流式透传——**转发路径上任何同步解析都会直接加进 TTFT/TPOT**。纪律只有一条：读路径只 tee，不解析。

具体形状：在 transport/response 层把上游 resp.Body 包一层 ReadCloser，Read 时把字节同时喂给带 buffer 的旁路 channel；解析 goroutine 按 SSE 行切分、只关心 usage 字段，解析完回调 TPM 结算。转发路径的额外成本 = 一次 channel send（buffer 满了就丢——丢一条 usage 只是少结算，绝不阻塞转发）。解析失败静默降级：打日志，绝不影响响应。"可开关"的形状：配置关闭时根本不包这层 wrapper，透传路径零开销——D6 思考题问的设计，今天落地。

非流式路径同理（body 完整，更好解析）。两条路径的结算必须收敛到同一个函数。

### 0.5 错误语义：401/429 的格式与头

P0 验收只说了状态码，但格式决定"官方 SDK 可直接调用"还成不成立：

* 401：`{"error":{"message":"...","type":"invalid_request_error","code":"invalid_api_key"}}`——你 D6 的 writeError 已经长这样，直接复用。
* 429：type 用 `requests`（QPS 超限）/ `tokens`（TPM 超限），code 用 `rate_limit_exceeded`，**带 Retry-After 头**（按桶的补充速率算还差几秒，别拍脑袋写死）。SDK 的自动重试读这个头。
* 豁免清单：/healthz 必须免鉴权（探活不能被自己人限死）；/v1/models 鉴不鉴？OpenAI 是鉴的，问题与方案里定。
* 401/429 的请求**绝不能转发到上游**——验收拿 vLLM 日志证明，不是"理论上不会"。

## Step 1 前置核查（约 15 分钟）

1. bench.py 与 smoke_stream.py 的 `api_key="EMPTY"` 是硬编码——**先参数化**（--api-key 参数或读环境变量），不然 Step 6 没法跑。改完直连 vLLM 回归一次，确认没改坏。
2. 规划测试 key：至少两个（模拟两个租户，为 P1 配额留观察位），形状 `sk-<tenant>-<random>`。
3. 决定 API key 存哪。D7 从简（配置文件 / 启动 flag，启动加载、不热更），但结构必须是 `key → {tenant, 配额}` 的映射，不是 []string——P1 的多租户配额直接长在这个结构上。示例配置文件脱敏入库，真实 key 不进 git。

## Step 2 问题与方案（今天 60% 的学习量在这里）

每个问题写下你的方案，一两句话说清"为什么是它"；没有标准答案的问题，写清你定义的语义。全部答完拿给我 review，通过再动手。

### 中间件与身份

**Q1. Chain 的实际执行顺序是什么（先按 0.1 实测再答）？Auth / RateLimit / Logging 应该怎么排，为什么这个顺序？**
方案： 注册顺序是RateLimit -> Auth -> Logging, 对应的执行顺序是Logging->Auth->RateLimit. 原因是在Chain注册是从里到外的,执行的顺序与注册顺序相反.

**Q2. Identity 怎么从 Auth 传到 RateLimit？context 键的类型与命名？Identity 里现在就要有哪些字段（tenant？配额？）？**
方案： 通过context传递. 键的类型使用未导出类型`identityCtxKey`, 类型是 "Identity". Identity目前需要models, 表示用户能调用哪几种模型, tenant 表示租户信息, quota表示token配额.

**Q3. 401 的请求会不会消耗限流额度？你从什么机制上保证它不会？**
方案： 不会. Auth在RateLimit外层执行, 401的请求在Auth阶段就返回了,不会到RateLimit层.

### Key 与 KeyStore

**Q4. key 存哪、什么结构、什么时候加载？示例配置怎么脱敏，真实 key 怎么保证不进 git？**
方案： 生产环境下, key应该存在数据库, 结构就是Identity. 当前环境在启动的时候加载一次全量数据, 后续都是在内存中读取; . 示例配置使用假占位符"sk-tenant-a-EXAMPLE". 另外,真实key只存在于gitignore文件或者环境变量中,确保不会被提交.

**Q5. KeyStore 接口的方法签名是什么？key 不存在用 error 还是 comma-ok？接口和实现各放哪个包？谁在什么时候构造、怎么注入到 Auth 中间件？**
方案： 方法签名: Get(key string) (*Identity, error). 用error, 因为查询过程中可能报错,error更适合一些. 接口和实现都放在auth包里面. 但是error也要分类, 确保查不到和查询报错是能区分开的. 通过main.go在启动的时候构造StaticKeyStore, 作为参数传给Auth中间件.

**Q6. key 比较要不要 subtle.ConstantTimeCompare？你的威胁模型是什么，成本又是什么？**
方案： `subtle.ConstantTimeCompare` 只是防止时序攻击,暂时不要, 先确保主链路跑通.可以加个TODO.

**Q7. 哪些路径豁免鉴权？/v1/models 鉴不鉴，依据是什么（OpenAI 怎么做的）？**
方案： /healthz豁免, /v1/models要. 模型相关的都是敏感信息.

### QPS 限流

**Q8. QPS 桶用 x/time/rate 还是自研？limit 和 burst 各取多少、单位口径是每秒还是每分钟？依据是什么？**
方案：自研. 理由是可以和"TPM公用一套实现". 对于验收档,limit取5/s, burst取5. 按照baseline数据, limit和burst都能打满,能较快观测到限流现象.对于压测档, 从baseline来看, 引擎能支持约23/s的qps,网关取高一些,limit设定为30/s, burst 要大于压测并发数, 可以设置成64, 避免请求进来出现大片429

**Q9. 超限是排队还是拒绝？vLLM 自己已有队列这个事实怎么影响你的选择？**
方案： 拒绝. vLLM的continuous batching的队列本身就是接受请求然后排队的,网关再排队属于双重排队,占用网关资源.

### TPM 限流与结算

**Q10. x/time/rate 为什么不能直接用于 TPM 维？自研 bucket 的方法集（Allow / AllowN / Refund？）和负余额语义是什么？哪三个单测必须过？**
方案：TPM的成本只有事后才知道, 语义上必须是"预检扣估算, 结算退差额". 退的动作是x/time/rate没有的.Allow() — 等价于 AllowN(1)，QPS 维用（两维共用一套实现是 Q8 的决定）。
AllowN(n) — 先按时间补充（封顶容量），余额够就扣、返回 true；不够则余额原封不动、返回 false。
Refund(n) — 余额加 n，封顶容量；n 允许为负（即补扣），不封底。
结算就是 Refund(预估扣减量 − 实际用量)——实际超估时差值为负，Refund 自动变成补扣，正向反向共用一个方法. 负余额语义：允许，且不设下限。 含义是"欠债"：客户端没传 max_tokens 却生成超长，预检估少了，结算把余额打成负的，之后靠自然补充偿还，偿还期内该租户预检自然全部 429。语义诚实（超发就记账）、实现零分支。不设下限的理由：单请求最大超发量有界（被引擎 max_model_len 封住），不可能无限欠债。
三个必须过的单侧:
1-补充正确性：空桶经过 t 秒 ≈ perSec × t，且封顶容量；
2-AllowN 语义：余额不足返回 false 且余额不变；连续扣减直到耗尽的轨迹符合预期；
3-Refund 边界：回填超过容量时封顶；Refund(负数) 允许余额变负；

**Q11. 预检 est 公式是什么？prompt 估不估、怎么估？max_tokens 缺省时取什么？**
方案：预检使用len(body) / 4 + maxTokens. maxTokens取请求的maxToken, 缺省可以给一个默认值,例如1024. 预检可以不用太准,结算的时候有"多退少补"兜底.

**Q12. 真实用量 从哪来？流式怎么拿 usage（include_usage 怎么注入，客户端已传 stream_options 时怎么合并）？非流式呢？两条路径的结算怎么收敛到同一个入口？**
方案：非流式： 上游响应是完整 JSON，其中 usage{prompt_tokens, completion_tokens, total_tokens} 就是实际用量。非流式响应体本来就一次到齐，旁路收齐后只解析 usage 字段（用一个只带 Usage 字段的轻量结构体，其余字段不解析）。
流式： vLLM 默认的 SSE chunk 不带 usage；OpenAI 协议的解法是 stream_options: {"include_usage": true}——引擎在 data: [DONE] 之前多发一个 choices 为空、带完整 usage 的 chunk，vLLM 支持。
注入与合并： 网关在转发前改写请求体（body 反正被 peek 过一次），强制把 stream_options.include_usage 置为 true。客户端已传 stream_options 时合并不覆盖：保留客户端的其他字段，只强制 include_usage 这一项；客户端显式传 false 也置为 true——计量是平台行为，对客户端的影响只是多收一个 usage chunk，这是官方功能的正常形态，SDK 兼容。

**Q13. 旁路 channel 容量多大、满了丢不丢？解析失败时的降级行为是什么？开关关掉时怎么做到透传路径零开销？**
方案：容量64, 满了就丢. 旁路 channel 里流的是响应体的字节块（每次 Read 一块）。发送必须用非阻塞写法：
select {
case ch <- chunk:
default: // 满了就丢这一条，记日志
}
丢弃的代价是"这一条响应的 usage 可能没数到，少结算一次"—— metering 受损，但转发路径绝不被解析速度拖住。64 的依据：旁路解析只是按行扫找 "usage"，消费速度远快于网络 Read，64 的缓冲已远超正常水位；真打满了说明解析 goroutine 出问题了，该丢就丢、该报警就报警。任何情况下都不允许用阻塞发送——那是把旁路的慢传染给主路径。
解析失败的降级：记日志、跳过本条结算、响应路径零感知。 旁路 goroutine 里所有错误（JSON 解不出来、usage 字段缺失、channel 提前关闭）都只产生一条日志，不向主路径传播；本条请求按"少结算"处理（预检扣的量不退，对租户偏保守、对平台偏安全）。旁路 goroutine 自身要 recover——它 panic 不能影响转发 goroutine。
开关关掉时的零开销：物理不存在，而不是空转。 关键在开关作用的层次——不是"包一层 wrapper 但什么都不干"，而是配置关闭时：
- 不创建 wrapper：上游 resp.Body 原样交给 ReverseProxy，转发路径和没写这个功能前一字节不差；
- 不注入 include_usage：请求体也不改写，上游收到客户端原样的请求；
- 不启动旁路 goroutine 和 channel。
也就是说，开和关是两条不同的装配路径（在构造 ReverseProxy/中间件时分支），不是同一条路径上的 if 判断。这样"关掉"的性能举证责任就变成了代码走查：转发路径上根本找不到计量相关的任何符号。

### 错误语义与配置

**Q14. 429 的 Retry-After 怎么算？QPS 超限与 TPM 超限各返回什么 type？**
方案：Retry-After = 桶攒够被拒的n个令牌还要多少秒. ceil((n - tokens)/perSec), 下限取1.
type可以参考OpenAI, QPS超限对应requests, TOM对应tokens, code都是rate_limit_exceeded. message带上Limit/Used/Requested,用于排查.

**Q15. 限流配置的形状（flag / 文件）？改配置要不要重启？不热更的话记 roadmap 了吗？**
方案：可以是一个yaml文件, 通过 --config flag指定路径. 通过tenant/models/quota,以及defaultMaxTokens, 旁路开关配置都在里面. 启动时加载,不需要热更新.可以通过watch/SIGHUP配置热更新key的方式管理API

### 范围确认

**Q16. 模型权限这类 authz 检查留不留？留的话：body 只被 peek 一次的机制是什么（谁读、谁放 context、谁复用）？失败返回什么状态码？**
方案： 留.虽然demo目前只有固定的模型,但这是一个很实际的生产问题. body只被peek一次可以通过加一个context注入handler来实现, 后续所有的handler都可以读. 失败返回的状态码用404

## ✅ Step 3 鉴权中间件

按顺序做，每步独立可验：

1. **401 先行**：挂上 Auth，任何 key 都拒，确认 401 格式与"未转发上游"（vLLM 日志零记录）。
2. **放行合法 key**：两个测试 key 都通；缺失头 / 错误 key / 无 Bearer 前缀三种 401 分别验证。
3. **身份注入**：key → tenant 解析后放 context，打日志验证后续环节拿得到 tenant。
4. **豁免路径**：/healthz 无 key 200。
5. 用实测证明中间件执行顺序（0.1），把 Chain 的注释改成实话。

## ✅ Step 4 QPS 限流

1. per-key 桶：`map[key]*limiter` + 并发安全（sync.Map 或 RWMutex，`go test -race` 说话）。未认证请求到不了这里（0.1 已保证）。
2. Allow 语义：超限立即 429 + Retry-After，**不排队**。为什么不用 Wait——vLLM 自己已有队列，网关排队是双重排队，徒增延迟不确定性。
3. 单测：补充速率、burst 边界、并发扣减（-race）。
4. 联调：限 5 QPS burst 5，循环打 20 个请求，数 200/429 各多少，等 2 秒再打验证恢复。

## ✅ Step 5 TPM 限流与旁路结算

1. 自研 bucket + 三个单测（0.2）。
2. 预检：从 peek 出的请求算 est，`AllowN(est)`，拒了 429（type=tokens）。
3. 旁路：transport 层 tee resp.Body → 解析 goroutine → 同一入口结算 `Refund(est − actual)`。非流式与流式各验一次。
4. include_usage 注入：抓网关→上游的请求体验证注入成功；客户端侧确认 SDK 正常收尾、能收到 usage chunk。
5. 降级演练：人为让旁路解析出错（临时改坏解析逻辑），确认响应不受影响、错误有日志、只是少结算。
6. 观测：per-key 当前余额 / 累计消耗打日志（或 /debug 端点），Step 6 验收引用这些数字。这只是验收证据，不是 P1 计量计费，别展开。

## Step 6 验收（401/429 的量化口径）

| # | 测试 | 通过标准 |
|---|---|---|
| 1 | 无头 / 错 key / 无 Bearer 前缀 | 三种均 401，OpenAI 错误格式，vLLM 日志零记录 |
| 2 | 合法 key 非流式 + 流式 | 正常响应，与 D6 行为一致 |
| 3 | /healthz 豁免 | 无 key 200 |
| 4 | 401 不消耗额度 | 假 key 打 50 次后，真 key 的 QPS 额度分文未动（日志/调试端点佐证） |
| 5 | QPS 超限 | 限 5 QPS burst 5 连打 20：429 数符合桶语义；带 Retry-After；429 未转发上游 |
| 6 | TPM 超限 | 低 TPM 配额下紧接的第二条请求 429（type=tokens）；等待补充后恢复 |
| 7 | 结算正确性 | 流式请求 max_tokens=300 实际生成约 50：结算后余额回填差额（旁路日志可见 est/actual/refund 三个数） |
| 8 | include_usage 注入 | 网关→上游请求体含 include_usage=true；官方 SDK 流式正常收尾且收到 usage |
| 9 | 并发安全 | `go test -race` 过；网关 race 构建下 bench 并发 32 压 60s 无 data race |
| 10 | TTFT 税 | 鉴权+双维限流+旁路全开，并发 1 经网关 vs 直连 TTFT 中位数差 < 5ms（D6 口径延续） |

## Step 7 收尾

* [ ] 问题与方案 + Step 6 数据填进本文档（你的仓库）
* [ ] 对标笔记：LiteLLM 的 tpm/rpm 限流（≤30 行）存 design-notes；D6 欠的 gateway.md 一并补上或记 roadmap
* [ ] dev-journal 记录：est/actual/refund 实测日志原文、429 Retry-After 实测、include_usage 注入的抓包证据
* [ ] 明日预告：D8 调度策略 v1 + prefix 亲和设计——**里程碑：主链路通**。D6 决策表里 picker 接缝的答案要兑现了

## 思考题（review 时我会问）

1. 429 拒绝为什么不消耗令牌？如果消耗，配合 Retry-After 会产生什么行为？
2. 高并发瞬间多个请求同时通过预检（余额够每个的 est），结算后余额深度为负——什么场景下负余额会累积成"永远还不清"？要不要设下限？
3. include_usage 注入意味着网关改了客户端的请求体。网关在什么意义上"有权"这么做？如果客户端显式传了 include_usage=false 呢？
4. per-key 桶在网关内存里。D12 网关 2 副本时配额变 2 倍——和 D6 思考题 3（RR 游标）是不是同一个问题的两种形态？集中式限流（Redis）本期不做，用一句话写清取舍进 roadmap。
5. 旁路 channel 满了丢 usage = 少结算 = 租户白嫖。压测时丢弃率多少？要不要暴露成 metric（D9 的素材）？
6. TPM 的"分钟"在哪？令牌桶里没有窗口，rate 是每秒补充。产品文档写"TPM 限 6000"时你的 rate 配多少？这个换算在突刺流量下与真窗口的行为差异是什么？

## 踩坑记录区（遇到即记，含报错原文与解法）

*（空着，今天填）*

## 常见坑速查

| 症状 | 真实原因 | 解法 |
|---|---|---|
| 401 也消耗限流额度 | 中间件顺序反了：Chain 是反向包裹，后传的最外层 | 实测执行顺序；认证必须比限流外层 |
| 流式拿不到 usage | vLLM 默认不返回，需 include_usage | 网关注入；客户端已传则合并 |
| 注入后 SDK 流式报错 | 多半是旁路改动了转发路径 | 旁路只 tee 不改流；usage chunk choices 为空属正常形态 |
| TPM 永不触发 / 老是误触 | 结算没扣上（旁路静默死）或 est 公式错 | 旁路错误必打日志；est/actual/refund 三数进日志 |
| 桶只扣不补 / 退款灌爆桶 | refill 时间基准错 / Refund 没封顶 capacity | 0.2 的三个单测 |
| 429 后永远恢复不了 | rate 单位搞错（每分钟当成每秒）或 burst=0 | rate.Limit 单位是每秒；换算写进配置注释 |
| -race 报警 | per-key map 并发读写 / bucket 字段无锁 | sync.Map 或 RWMutex；bucket 内部自锁 |
| TTFT 增量 > 5ms | 旁路在转发路径上同步解析了 | 读路径只 tee；解析全在旁路 goroutine |
| 压测时 429 抖动大 | burst 太小，正常并发被误伤 | burst ≥ 预期并发峰值，依据写进问题与方案 |

## 附：D7 命令速查

    # 鉴权三态（$BODY 为你的测试请求体）
    curl -i http://localhost:8080/v1/chat/completions -H 'Content-Type: application/json' -d "$BODY"        # 无头 → 401
    curl -i http://localhost:8080/v1/chat/completions -H 'Authorization: Bearer sk-wrong' -d "$BODY"        # 错 key → 401
    curl -i http://localhost:8080/v1/chat/completions -H "Authorization: Bearer $KEY" -d "$BODY"            # 合法 → 200

    # QPS 超限观察
    for i in $(seq 20); do curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $KEY" \
      http://localhost:8080/v1/chat/completions -d "$BODY"; done

    # 旁路结算观察：盯网关日志的 est/actual/refund
    curl -N --no-buffer http://localhost:8080/v1/chat/completions -H "Authorization: Bearer $KEY" -d '{
      "model":"qwen2-5-7b","messages":[{"role":"user","content":"数数到20"}],"max_tokens":300,"stream":true}'

    # 单测与 race
    cd gateway && go test ./... -race -count=1
    go build -race -o /tmp/gateway-race ./cmd/gateway        # race 构建供压测用

    # 带鉴权压测（前置：bench.py 已参数化 api key）
    python bench/bench.py --base-url http://localhost:8080/v1 --api-key $KEY --model qwen2-5-7b \
      --mode steady --concurrency 32 --duration 60
