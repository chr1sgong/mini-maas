# 环境

> 只记录探测到的真实输出，不记推测。后续环境变更后更新本文件。

## 硬件 / 系统

| 项 | 实测值 |
|---|---|
| CPU | Intel i9 14 代，WSL 可见 32 逻辑核 |
| 内存 | 宿主 128GB；**WSL 当前仅见 62Gi**（未配 .wslconfig，默认 50% 上限），Swap 16Gi |
| GPU | RTX 4090，显存 24564 MiB，探测时已用 1634 MiB |
| WSL | Ubuntu 24.04.4 LTS，kernel 6.6.87.2-microsoft-standard-WSL2，**systemd 已开启** ✓ |
| NVIDIA 驱动 | 591.86（Windows 侧透传），CUDA 13.1；WSL 内 `nvidia-smi` 正常 ✓ |
| 磁盘 | WSL `/` 共 1007G，可用 634G |

## 软件 / 已有组件（基础软件）

| 组件 | 版本 | 状态 | 与 本项目 的关系 |
|---|---|---|---|
| Docker  | 29.4.0 | active  | 与 k3s 共存；拉镜像可走 docker 再导入 k3s containerd |
| nvidia-container-toolkit  | 1.19.0 | 已装 | 容器模拟推理池，需要往容器注入nvidia先看。 直接用，无需再装 |
| kubectl | Client Version v1.35.1 | 已装 | — |
| k3s | v1.36.4+k3s1 | 已装 | —— |
| go | 1.26.7 | 已装 | —— |
| kubebuilder | v4.16.0 | —— |
