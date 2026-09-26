独立 go module
kubebuild init在这里执行
autoscaler 作为cmd/autoscaler放本module
直接复用api/v1alpha1 的 CR类型，避免跨module依赖
