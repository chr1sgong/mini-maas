# docs/experiments — 实验记录规范

一次实验一个目录，命名 `yyyymmdd-<short-name>`，固定五件：

```bash
README.md     配置 / 曲线 / 结论（模板见下，复制后填空）
run.json      bench 产出的 run 清单（锚点 + argv + git rev + CR/config 快照）
bench.md      bench --md 追加的汇总行（客户端精确口径）
metrics/      export_run.py 产出的 CSV + summary.json（系统/引擎口径）
charts/       render_charts.py 产出的四张 PNG
```

## README 模板

```markdown
# <yyyymmdd>-<实验名>

## 配置
- run_id / 模型 / 引擎参数（run.json 的 model_cr_spec 关键行）：
- 网关 routing.strategy / 限流档位（run.json 的 config_snapshot 关键行）：
- bench 参数（argv 关键行：mode/concurrency/input/output/duration）：
- git_rev：

## 曲线
- ![latency](charts/latency.png)：一句读法（例：TTFT P99 在 90s 处阶跃，对应 waiting 爬升）
- ![throughput](charts/throughput.png)：一句读法
- ![queue](charts/queue.png)：一句读法
- ![kv_cache](charts/kv_cache.png)：一句读法

## 指标结果
| 指标 | bench（精确） | 网关 | 引擎 | 差值解读 |
|---|---|---|---|---|
| 请求数 |  |  |  | 必须精确相等 |
| TTFT P50/P99 |  |  |  | 网关与引擎 应 <10ms 且稳定 |
| 吞吐 tok/s |  | — |  | 差 <5%（bench 含爬坡略低） |

## 结论
- 一句话结论（数字说话）：
- 与上次/基线的对比：
- 遗留问题：
```
