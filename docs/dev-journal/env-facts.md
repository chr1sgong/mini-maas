# 环境

> 只记录探测到的真实输出，不记推测。后续环境变更后更新本文件。

## 硬件 / 系统

| 项 | 实测值 |
|---|---|
| CPU | Intel i9 14 代 |
| 内存 | 128GB，Swap 16Gi |
| GPU | RTX 4090，显存 24564 MiB |
| NVIDIA 驱动 | 595.91.07，CUDA 13.2；WSL 内 `nvidia-smi` 正常 ✓ |

## 软件 / 已有组件（基础软件）

| 组件 | 版本 | 状态 | 与 本项目 的关系 |
|---|---|---|---|
| Docker  | 29.4.0 | active  | 与 k3s 共存；拉镜像可走 docker 再导入 k3s containerd |
| nvidia-container-toolkit  | 1.20.1 | 已装 | 容器模拟推理池，需要往容器注入nvidia先看 |
| kubectl | Client Version v1.36.4+k3s1 | 已装 | kubernetes客户端，开发测试使用 |
| k3s | v1.36.4+k3s1 | 已装 | k8s环jj境 |
| go | 1.27.1 | 已装 | 网关和Operator开发 |
| kubebuilder | v4.16.0 | 已装 | Operator脚手架 |
| uv | 0.12.20 | 已装 | python环境管理 |
| Python | 3.12(uv管理: uv venv --python=3.12) | 已装 | 统一Python环境 | 
