# Pod 信息关联问题排查指南

## 问题描述

在 Kibana 中查看 Tetragon 事件时，某些事件没有显示 Pod 信息（`k8s.namespace`、`k8s.pod`、`k8s.container`），只有 `node` IP 和 `process.pid`。

## 可能的原因

### 1. Host 进程（不在容器中）

**最常见的原因**：进程运行在 host 上，不在 Kubernetes Pod 中。

**特征**：
- 没有 `k8s` 字段
- `process.pid` 通常是系统进程或守护进程
- 例如：`/bin/cat /proc/sys/net/core/somaxconn` 可能是系统监控工具执行的

**解决方法**：
- 这是正常现象，host 进程本身就没有 Pod 信息
- 可以通过 `is_container: false` 字段识别

### 2. Tetragon 未成功关联 Pod

**可能原因**：
- Kubernetes API 连接问题
- 进程执行太快，Tetragon 还没关联上
- Pod 信息在进程执行时还未同步到 Tetragon

**排查步骤**：

#### 检查 Tetragon K8s API 连接

```bash
# 查看 Tetragon Pod 日志
kubectl logs -n falco -l app.kubernetes.io/name=tetragon | grep -i "k8s\|kubernetes\|api"

# 检查是否有连接错误
kubectl logs -n falco -l app.kubernetes.io/name=tetragon | grep -i "error\|fail"
```

#### 检查配置

确认 `valu.yaml` 中已启用 K8s API：

```yaml
enableK8sAPI: true  # 必须为 true
```

#### 检查 ServiceAccount 权限

```bash
# 查看 Tetragon ServiceAccount
kubectl get serviceaccount -n falco | grep tetragon

# 查看是否有 RBAC 权限
kubectl get clusterrolebinding | grep tetragon
```

### 3. 进程执行时机问题

**场景**：容器刚启动时，进程执行太快，Tetragon 还没从 K8s API 获取到 Pod 信息。

**特征**：
- 容器中的进程
- 但事件中没有 k8s 信息
- 后续事件可能有 k8s 信息

**解决方法**：
- 这是正常现象，短暂延迟后应该能关联上
- 如果持续没有，考虑启用 rthooks（需要 containerd 1.7+）

## 改进后的 Logstash 配置

已更新 `logstash-k8s-tetragon-policy.conf`，现在会：

1. **更可靠地提取 k8s 字段**：使用 Ruby 代码提取，避免类型问题
2. **添加标识字段**：
   - `is_container: true` - 容器进程
   - `is_container: false` - host 进程
   - `k8s.namespace: 'host'` - host 进程标记

## 如何识别事件来源

### 在 Kibana 中查询

#### 查看所有容器进程事件
```
k8s.namespace: * AND NOT k8s.namespace: "host"
```

#### 查看所有 host 进程事件
```
k8s.namespace: "host" OR is_container: false
```

#### 查看特定 Pod 的事件
```
k8s.pod: "your-pod-name" AND k8s.namespace: "your-namespace"
```

#### 查看没有 Pod 信息的事件
```
NOT k8s.pod: * OR k8s.pod: "host"
```

## 手动关联 Pod 信息

如果事件中没有 Pod 信息，可以通过以下方式手动关联：

### 方法 1：通过 PID 和节点 IP

```bash
# 在对应节点上执行
NODE_IP="172.30.32.41"
PID="111721"

# 查找该 PID 对应的容器
crictl ps --pid $PID

# 或者使用 containerd
ctr -n k8s.io containers ls | grep $PID
```

### 方法 2：通过时间戳和节点查询 K8s API

```bash
# 查询该时间点该节点上的所有 Pod
kubectl get pods --all-namespaces -o wide --field-selector spec.nodeName=<node-name>

# 然后通过进程特征（binary、args）匹配
```

## 验证配置

### 测试容器进程事件

在 Pod 中执行命令，查看是否有 k8s 信息：

```bash
# 创建一个测试 Pod
kubectl run test-pod --image=busybox --rm -it -- sh

# 在 Pod 中执行命令
cat /proc/version

# 在 Kibana 中查询，应该能看到 k8s.pod: "test-pod"
```

### 检查 Tetragon 日志

```bash
# 查看是否有 Pod 关联成功的日志
kubectl logs -n falco -l app.kubernetes.io/name=tetragon | grep -i "pod\|container"
```

## 常见问题

### Q: 为什么有些事件有 Pod 信息，有些没有？

**A**: 
- 有 Pod 信息：容器中的进程
- 没有 Pod 信息：host 进程或 Tetragon 未及时关联

### Q: 如何确保所有容器进程都有 Pod 信息？

**A**: 
1. 确保 `enableK8sAPI: true`
2. 检查 Tetragon 与 K8s API 的连接
3. 考虑启用 rthooks（需要 containerd 1.7+）

### Q: host 进程的事件需要关注吗？

**A**: 
- 取决于您的安全策略
- 通常 host 进程（如系统监控、Tetragon 自身）是正常的
- 可以通过 `is_container: false` 过滤掉

## 相关配置

- **Tetragon 配置**：`valu.yaml` - `enableK8sAPI: true`
- **Logstash 配置**：`logstash-k8s-tetragon-policy.conf` - k8s 字段提取逻辑
- **Tetragon Kafka Adapter**：`internal/normalize/process_exec.go` - Pod 信息提取

## 下一步

1. **重启 Logstash**：应用新的配置
2. **观察新事件**：查看是否有 `is_container` 和 `k8s.namespace` 字段
3. **排查连接问题**：如果容器进程仍没有 Pod 信息，检查 Tetragon 日志
