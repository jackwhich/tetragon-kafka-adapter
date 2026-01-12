# 快速开始指南

## 方案说明

本项目使用 **方案 1：Kafka Compacted Topic + 消息 Key** 实现自动去重：

- ✅ 所有 Pod 使用相同的消息 Key（基于事件唯一标识：node:type:pid:timestamp）
- ✅ Kafka Compacted Topic 自动去重，只保留每个 Key 的最新消息
- ✅ 不依赖外部服务（Redis/etcd/K8s Leader Election）
- ✅ 真正的分布式，所有 Pod 都在工作，无单点故障
- ✅ 零切换中断，Pod 故障不影响其他 Pod

## 配置要点

### 1. Kafka Topic 配置（关键）

```yaml
kafka:
  topic_admin:
    cleanup_policy: "compact"  # ⭐ 启用 log compaction
    min_cleanable_dirty_ratio: "0.5"
```

### 2. Partition Key 配置（关键）

```yaml
routing:
  partition_key:
    mode: "deduplication"  # ⭐ 用于去重的 key 模式
    fields: ["node", "type", "process.pid", "timestamp"]  # 生成唯一 key
    separator: ":"
```

### 3. Producer 配置（关键）

```yaml
kafka:
  producer:
    enable_idempotence: true  # ⭐ 启用幂等性（防止网络重试重复）
```

## 构建和运行

### 本地开发

```bash
# 安装依赖
go mod download

# 运行
go run cmd/consumer/main.go -config configs/config.yaml
```

### Docker 构建

```bash
docker build -t tetragon-kafka-adapter:latest .
```

### Kubernetes 部署

```bash
# 部署
kubectl apply -f deployments/k8s/

# 查看状态
kubectl get pods -n kube-system -l app=tetragon-kafka-adapter

# 查看日志
kubectl logs -n kube-system -l app=tetragon-kafka-adapter -f
```

## 环境变量配置

```bash
export TETRAGON_GRPC_ADDR="tetragon.kube-system.svc:54321"
export KAFKA_BROKERS="kafka-0:9092,kafka-1:9092"
export KAFKA_CLIENT_ID="tetragon-kafka-adapter"
export KAFKA_TOPIC_AUTO_CREATE="true"
export KAFKA_TOPIC_CLEANUP_POLICY="compact"
export LOG_LEVEL="info"
```

## 验证

### 1. 检查健康状态

```bash
curl http://localhost:8080/health
```

### 2. 查看指标

```bash
curl http://localhost:9090/metrics
```

### 3. 验证 Kafka Topic

```bash
# 检查 topic 是否创建
kafka-topics.sh --list --bootstrap-server kafka:9092

# 检查 topic 配置（应该看到 cleanup.policy=compact）
kafka-configs.sh --describe --entity-type topics --entity-name tetragon.process.exec --bootstrap-server kafka:9092
```

## 监控指标

关键指标：

- `events_in_total{type=...}` - 接收的事件总数
- `events_out_total{topic=...,status=...}` - 写入 Kafka 的事件数
- `queue_depth` - 当前队列深度
- `drops_total{reason=...}` - 丢弃的事件数
- `kafka_write_latency_ms_bucket{topic=...}` - Kafka 写入延迟
- `grpc_reconnect_total` - gRPC 重连次数

## 故障排查

### 1. 事件重复

- 检查 Kafka Topic 是否配置为 `cleanup.policy=compact`
- 检查消息 Key 是否正确生成
- 检查 Producer 是否启用 `enable_idempotence=true`

### 2. 队列满

- 增加 `writer_workers`
- 增加 `batch.max_messages`
- 降低 `sample_ratio`（采样）

### 3. gRPC 连接失败

- 检查 `TETRAGON_GRPC_ADDR` 是否正确
- 检查网络连通性
- 查看 `grpc_reconnect_total` 指标

## 下一步

1. 根据实际需求调整配置参数
2. 配置 Prometheus 告警规则（参考文档中的告警规则）
3. 监控关键指标，优化性能
4. 根据业务需求扩展事件类型支持
