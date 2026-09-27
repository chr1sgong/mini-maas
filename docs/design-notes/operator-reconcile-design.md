

| 决策点                                                                 | 你的选择   | 理由 / 不这么选的后果 |
| ------------------------------------------------------------------- | ------ | ------------ |
| 子资源命名规则（Model `qwen2-5-7b` → Deployment 叫什么）                        | <br /> | <br />       |
| 子资源 labels 方案（至少哪些 label，谁用它们做 selector）                            | <br /> | <br />       |
| engineConfig → vLLM args 的映射函数长什么样（bool 字段 false 时怎么表达）             | <br /> | <br />       |
| status.phase 状态机：有哪几个值，各自进入条件                                       | <br /> | <br />       |
| status.endpoints 填什么：Pod IP:port 还是 Service ClusterIP:port 还是 DNS 名 | <br /> | <br />       |
| Deployment 更新策略：GET-比较-Update / CreateOrUpdate / SSA                | <br /> | <br />       |
| update 已存在的 Deployment 时，replicas 字段覆不覆盖                            | <br /> | <br />       |
| 今天要不要 finalizer                                                     | <br /> | <br />       |