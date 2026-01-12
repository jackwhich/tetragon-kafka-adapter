# 网络事件 Pod 关联指南

## 问题描述

Tetragon 的网络监控事件（如 `tcp_connect`）可以捕获：
- **源 Pod 信息**：通过 `process.pod` 获取（发起连接的 Pod）
- **网络连接信息**：源 IP、目标 IP、源端口、目标端口
- **目标 Pod 信息**：需要通过目标 IP 反向查找

## 当前状态

### 已提取的信息

1. **源 Pod 信息**（已可用）：
   - `k8s.namespace` - 源 Pod 的 namespace
   - `k8s.pod` - 源 Pod 的名称
   - `k8s.container` - 源容器的名称

2. **网络连接信息**（已提取）：
   - `network.source_ip` - 源 IP 地址
   - `network.source_port` - 源端口
   - `network.destination_ip` - 目标 IP 地址
   - `network.destination_port` - 目标端口
   - `network.protocol` - 协议（TCP/UDP）
   - `network.family` - 地址族（IPv4/IPv6）

### 缺失的信息

- **目标 Pod 信息**：需要通过目标 IP 查找

## 解决方案

### 方案 1：在 Kibana 中通过查询关联（推荐）

#### 步骤 1：查询源 Pod 到目标 IP 的连接

```
event_type: syscall_kprobe AND extra.function_name: tcp_connect AND network.destination_ip: *
```

#### 步骤 2：通过目标 IP 查找目标 Pod

在 Kibana 中，可以通过以下方式查找目标 Pod：

**方法 A：查询目标 IP 对应的 Pod**

```
# 查询所有 Pod 的 IP 地址（需要先有 Pod 信息的事件）
k8s.pod: * AND process.pid: *

# 然后通过 network.destination_ip 匹配
network.destination_ip: <目标IP>
```

**方法 B：使用 Elasticsearch 查询**

在 Kibana Dev Tools 中执行：

```json
GET /logstash-k8s-tetragon-policy-*/_search
{
  "query": {
    "bool": {
      "must": [
        { "exists": { "field": "k8s.pod" } },
        { "term": { "network.source_ip": "<目标IP>" } }
      ]
    }
  },
  "size": 1
}
```

### 方案 2：在 Logstash 中添加 IP 到 Pod 的映射（高级）

需要维护一个 IP 到 Pod 的映射表，可以通过：

1. **定期查询 Kubernetes API**：获取所有 Pod 的 IP 地址
2. **使用 Elasticsearch 查询**：从已有事件中提取 IP 到 Pod 的映射
3. **使用 Redis 缓存**：存储 IP 到 Pod 的映射关系

#### 示例 Logstash 配置（使用 Elasticsearch 查询）

```ruby
# 在 filter 中添加
if [network] and [network][destination_ip] {
  # 查询目标 IP 对应的 Pod
  elasticsearch {
    hosts => ["http://172.30.32.71:9200"]
    query => "network.source_ip:%{[network][destination_ip]}"
    fields => {
      "k8s.namespace" => "network.destination.namespace"
      "k8s.pod" => "network.destination.pod"
      "k8s.container" => "network.destination.container"
    }
  }
}
```

**注意**：这种方法有性能开销，建议使用方案 1。

### 方案 3：使用 Tetragon 内置的 PROCESS_CONNECT 事件

Tetragon 内置的 `PROCESS_CONNECT` 事件可能包含更多信息。检查 `valu.yaml`：

```yaml
exportAllowList: |-
  {"event_set":["PROCESS_EXEC", "PROCESS_EXIT", "PROCESS_KPROBE", "PROCESS_UPROBE", "PROCESS_TRACEPOINT", "PROCESS_LSM"]}
```

可以添加 `PROCESS_CONNECT`：

```yaml
exportAllowList: |-
  {"event_set":["PROCESS_EXEC", "PROCESS_EXIT", "PROCESS_CONNECT", "PROCESS_KPROBE", "PROCESS_UPROBE", "PROCESS_TRACEPOINT", "PROCESS_LSM"]}
```

`PROCESS_CONNECT` 事件可能已经包含了目标信息。

## 在 Kibana 中查看 Pod 到 Pod 的流量

### 查询示例

#### 1. 查看特定源 Pod 的所有连接

```
k8s.pod: "source-pod-name" AND event_type: syscall_kprobe AND extra.function_name: tcp_connect
```

#### 2. 查看特定目标 IP 的所有连接

```
network.destination_ip: "10.244.1.5" AND event_type: syscall_kprobe
```

#### 3. 查看特定源 Pod 到特定目标 IP 的连接

```
k8s.pod: "source-pod-name" AND network.destination_ip: "10.244.1.5" AND event_type: syscall_kprobe
```

#### 4. 查看所有容器间的连接（排除 host）

```
event_type: syscall_kprobe AND extra.function_name: tcp_connect AND k8s.namespace: * AND NOT k8s.namespace: "host" AND network.destination_ip: *
```

### 可视化建议

在 Kibana 中创建可视化：

1. **网络流量矩阵**：
   - X 轴：`k8s.pod.keyword`（源 Pod）
   - Y 轴：`network.destination_ip.keyword`（目标 IP）
   - 指标：文档计数

2. **连接统计**：
   - 按 `network.destination_port` 分组
   - 按 `network.protocol` 分组

3. **Pod 网络活动**：
   - 按 `k8s.namespace.keyword` 和 `k8s.pod.keyword` 分组
   - 显示连接数量

## 手动查找目标 Pod

如果知道目标 IP，可以通过以下命令查找对应的 Pod：

```bash
# 方法 1：通过 kubectl 查询
kubectl get pods --all-namespaces -o wide | grep <目标IP>

# 方法 2：通过 Kubernetes API
kubectl get pods --all-namespaces -o json | jq '.items[] | select(.status.podIP == "<目标IP>") | {namespace: .metadata.namespace, name: .metadata.name, ip: .status.podIP}'
```

## 改进建议

### 短期方案（当前）

1. ✅ 已提取网络连接信息（IP、端口）
2. ✅ 已提取源 Pod 信息
3. ⚠️ 在 Kibana 中通过查询手动关联目标 Pod

### 长期方案（推荐）

1. **启用 PROCESS_CONNECT 事件**：检查是否包含更多目标信息
2. **添加 IP 映射服务**：定期同步 Kubernetes Pod IP 到映射表
3. **使用 Service Mesh**：如果使用 Istio/Linkerd，它们提供了更完整的 Pod 到 Pod 流量信息

## 相关文件

- **Kafka Adapter**：`internal/normalize/process_kprobe.go` - 提取网络信息
- **Schema**：`internal/schema/v1/schema.go` - NetworkInfo 结构
- **Logstash 配置**：`logstash-k8s-tetragon-policy.conf` - 提取网络字段

## 注意事项

1. **性能影响**：如果使用 Logstash 查询 Elasticsearch 来查找目标 Pod，会有性能开销
2. **IP 变化**：Pod IP 可能会变化，需要定期更新映射
3. **外部连接**：如果目标 IP 是集群外部地址，无法关联到 Pod
4. **Service IP**：如果目标 IP 是 Kubernetes Service IP，需要进一步查询 Service 对应的 Pod
