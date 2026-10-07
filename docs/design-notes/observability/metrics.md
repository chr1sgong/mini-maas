# 指标说明

## 一, 口径总则

### 1.1 观测视角

| 视角 | 观测点 | 覆盖区间 | 数据形态 |
| --- | --- | --- | --- |
| 用户(bench) | 客户端发出请求 -> 首个非空content chunk | 客户端网络 + 网关 + 引擎排队 + prefill + 首token | 逐样本请求 -> 精确分位 |
| 网关(gateway) | RoundTrip 发起 -> 首个上游chunk到达 | 引擎排队 + prefill + 首token + 上游网络开销 | histogram |
| 引擎(vLLM) | 引擎收到请求 -> 首token | 排队 + prefill + 首token计算 | histogram |

### 1.2 指标单位

- 时间单位统一使用秒(Unix秒时间戳), 展示的时候进行转换
- 分位数两口径:
  - bench 逐请求青雀分为
  - 曲线用histogram_quantile, histogram_quantile必须先increase/rate再聚合

## 二, 网关指标(job=gateway, scrape 15s, namespace="maas")

| 指标 | 类型 | 标签 | 口径 |
| --- | --- | --- | --- |
| `maas_gateway_requests_total` | counter | model, tenant, code | handler出口统一计数. **tenant很多的话, 不适合放在tag里面** |
| `maas_gateway_request_duration_seconds` | histogram | model, stream | e2e 耗时 |
| `maas_gateway_ttft_seconds` | histogram | model | 引擎TTFT + proxy开销 |
| `maas_gateway_tpot_seconds` | histogram | model | SSE chunk间隔均值 |
| `maas_gateway_route_decisions_total` | counter | strategy, reason, endpoint | 路由决策计数 |
| `maas_gateway_inflight_requests` | gauge | endpoint | 各上游在途请求数 |
| `maas_gateway_ratelimit_rejected_total` | counter | tenant, dimension | 429计数, **tenant很多的话, 不适合放在tag里面** |

## 三, vLLM引擎指标(job=vllm, scrape 5s)

### 3.1 队列

| 指标 | 类型 | 标签 | 口径 |
| --- | --- | --- | --- |
| `vllm:num_requests_running` | gaugg | model | requests in model execution batches | 
| `vllm:num_requests_waiting` | gaugg | model | waiting requests to be processed, 排队深度, 扩容信号 |
| `vllm:num_requests_waiting_by_reason` | gaugg | reason | capacity->等调度容量; deferred->暂态约束(LoRA预算/KV传输/阻塞). sum(reason) = num_requests_waiting |
| `vllm: kv_cache_usage_perc` | gaugg | kv cache用量百分比, 100%是要发生抢占调度的前兆 |
| `vllm:num_preemptions_total` | gaugg | model | 引擎累计抢占数. > 0 说明KV水位要超了 | 

### 3.2 延迟

| 指标 | 类型 | 标签 | 口径 |
| `vllm:time_to_first_token_seconds` | histogram | model | 引擎受到请求 -> 首token |
| `vllm:request_time_per_output_token_seconds` | histogram | model | "请求级别" TPOT | 
| `vllm:inter_token_latency_seconds` | histogram | model | "请求级别" token间隔分布 |
| `vllm_request_queue_time_seconds` | histogram | model | 请求WAITING阶段耗时 |
| `vllm:request_prefill_time_seconds` | histogram | model | 请求prefill阶段耗时 |
| `vllm:request_decode_time_seconds` | histogram | model | 请求decode阶段耗时 |
| `vllm:request_latency_seconds` | histogram | model | 引擎e2e耗时, ~prefill + decode |

### 3.3 吞吐与计数

| 指标 | 类型 | 标签 | 口径 |
| --- | --- | --- | --- |
| `vllm:prompt_tokens_total` | counter | model | 处理的prefill token数 -> 输入吞吐 |
| `vllm:generation_tokens_total` | counter | model | 生成token计数 -> 输出吞吐 |
| `vllm:request_success_total` | counter | finished_reason | 成功完成的请求数, reason in {stop, length, abort, error, repetition} |
| `vllm:iteration_tokens_total` | coutner | model | engine每step处理的token数 -> 批处理效率 |
| `vllm:request_prompt_tokens` | counter | model | 请求输入长度分布 |
| `vllm:request_generation_tokens` | counter | model | 请求输出长度分布 |

### 3.4 前缀缓存

| 指标 | 类型 | 标签 | 口径 | 
| --- | --- | --- | --- |
| `vllm:prefix_cache_queries_total` | counter | model | prefix_cache 查询的token数 |
| `vllm:prefix_cache_hits_total` | coutner | model | prefix_cache 命中的token数 -> 命中率 = hits / queries |
| `vllm:prompt_tokens_cached_total` | counter | model | 命中缓存的prompt token数 (local + external) |
| `vllm:request_prefill_kv_computed_tokens` | counter | model | 每请求prefill实际新算的KV token数, request_prompt_tokens - request_prefill_kv_computed_tokens = request_prefix_cache_hits | 
| `vllm:external_prefix_cache_*` | counter | model |  跨实例外部缓存(KV connector), 当前不用, 忽略 |
| `vllm:mm_cache_*` | counter | model | 多模态缓存, 当前不需要, 忽略 |

## 附 dump出来的全部vllm指标

vllm:cache_config_info
vllm:e2e_request_latency_seconds_bucket
vllm:e2e_request_latency_seconds_count
vllm:e2e_request_latency_seconds_created
vllm:e2e_request_latency_seconds_sum
vllm:engine_sleep_state
vllm:estimated_flops_per_gpu_created
vllm:estimated_flops_per_gpu_total
vllm:estimated_read_bytes_per_gpu_created
vllm:estimated_read_bytes_per_gpu_total
vllm:estimated_write_bytes_per_gpu_created
vllm:estimated_write_bytes_per_gpu_total
vllm:external_prefix_cache_hits_created
vllm:external_prefix_cache_hits_total
vllm:external_prefix_cache_queries_created
vllm:external_prefix_cache_queries_total
vllm:generation_tokens_created
vllm:generation_tokens_total
vllm:inter_token_latency_seconds_bucket
vllm:inter_token_latency_seconds_count
vllm:inter_token_latency_seconds_created
vllm:inter_token_latency_seconds_sum
vllm:iteration_tokens_total_bucket
vllm:iteration_tokens_total_count
vllm:iteration_tokens_total_created
vllm:iteration_tokens_total_sum
vllm:kv_cache_usage_perc
vllm:mm_cache_hits_created
vllm:mm_cache_hits_total
vllm:mm_cache_queries_created
vllm:mm_cache_queries_total
vllm:num_preemptions_created
vllm:num_preemptions_total
vllm:num_requests_running
vllm:num_requests_waiting
vllm:num_requests_waiting_by_reason
vllm:prefix_cache_hits_created
vllm:prefix_cache_hits_total
vllm:prefix_cache_queries_created
vllm:prefix_cache_queries_total
vllm:prompt_tokens_by_source_created
vllm:prompt_tokens_by_source_total
vllm:prompt_tokens_cached_created
vllm:prompt_tokens_cached_total
vllm:prompt_tokens_created
vllm:prompt_tokens_total
vllm:request_decode_time_seconds_bucket
vllm:request_decode_time_seconds_count
vllm:request_decode_time_seconds_created
vllm:request_decode_time_seconds_sum
vllm:request_generation_tokens_bucket
vllm:request_generation_tokens_count
vllm:request_generation_tokens_created
vllm:request_generation_tokens_sum
vllm:request_inference_time_seconds_bucket
vllm:request_inference_time_seconds_count
vllm:request_inference_time_seconds_created
vllm:request_inference_time_seconds_sum
vllm:request_max_num_generation_tokens_bucket
vllm:request_max_num_generation_tokens_count
vllm:request_max_num_generation_tokens_created
vllm:request_max_num_generation_tokens_sum
vllm:request_num_preemptions_bucket
vllm:request_num_preemptions_count
vllm:request_num_preemptions_created
vllm:request_num_preemptions_sum
vllm:request_params_max_tokens_bucket
vllm:request_params_max_tokens_count
vllm:request_params_max_tokens_created
vllm:request_params_max_tokens_sum
vllm:request_params_n_bucket
vllm:request_params_n_count
vllm:request_params_n_created
vllm:request_params_n_sum
vllm:request_prefill_kv_computed_tokens_bucket
vllm:request_prefill_kv_computed_tokens_count
vllm:request_prefill_kv_computed_tokens_created
vllm:request_prefill_kv_computed_tokens_sum
vllm:request_prefill_time_seconds_bucket
vllm:request_prefill_time_seconds_created
vllm:request_prefill_time_seconds_sum
vllm:request_prompt_tokens_bucket
vllm:request_prompt_tokens_count
vllm:request_prompt_tokens_created
vllm:request_prompt_tokens_sum
vllm:request_queue_time_seconds_bucket
vllm:request_queue_time_seconds_count
vllm:request_queue_time_seconds_created
vllm:request_queue_time_seconds_sum
vllm:request_success_created
vllm:request_success_total
vllm:request_time_per_output_token_seconds_bucket
vllm:request_time_per_output_token_seconds_count
vllm:request_time_per_output_token_seconds_created
vllm:request_time_per_output_token_seconds_sum
vllm:time_to_first_token_seconds_bucket
vllm:time_to_first_token_seconds_count
vllm:time_to_first_token_seconds_created
vllm:time_to_first_token_seconds_sum