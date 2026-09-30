**单机单卡在k3s环境下模拟单机多卡方案**
 
 使用NVIDIA Device Plugin的Time-Slicing技术，在Kubernetes层面模拟“多GPU资源”。因为物理上只有一张卡，因此只适合验证或测试推理池级别的调度策略，无法做TP, PP等并行策略，也无法做NCCL多卡通信等底层通信技术。

 核心思路是通过[nvidia-device-plugin](https://github.com/nvidia/k8s-device-plugin)谎报节点的GPU数量。nvidia-device-plugin是一个DaemonSet, 使用后，经过k8s的DaemonSet Contrlller和kube-scheduler一系列处理后，最终worker节点会拉起一个Daemon Pod, 运行nvcr.io/nvidia/k8s-device-plugin程序，这个程序的核心作用是向节点的kubelet注册自己，让kubelet通过`ListAndWatch`机制让device-plugin主动推送设备信息，kubelet后续给api server上报设备信息的时候会用到device-plugin的推送结果。具体流程：
 1. ConfigMap(timeSlicing.replicas=N) -> device-plugin重启
 2. device-plugin通过ListAndWatch推送设备清单从1变成N给kubelet
 3. kubelet把N个设备写进Node.status.capacity/allocatable
 4. scheduler的过滤器从此认为该节点有N份nvidia.com/gpu可用
 
 由于time-slicing对运行期没有任何强制，需要用户自觉管理好`gpu_memory_utilization`


 以当前硬件环境`RTX 4090`为例，