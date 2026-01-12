# Tetragon gRPC → Kafka Consumer（Go）专业级方案（高并发/可扩展/配置分离）

> 面向生产：高吞吐、可控背压、断线重连、可观测、可演进（后期新增事件/字段/Topic）。
>
> 适用：Kubernetes 上运行 Tetragon，通过 gRPC 流式订阅事件，写入 Kafka 供 SIEM / 风控 / 安全分析 / 实时检测使用。

---

## 1. 目标与约束

### 1.1 目标
- **不落盘**：事件从 Tetragon 直接进入 Kafka（避免 `tetragon.log` 文件链路）。
- **高并发/高吞吐**：支撑高 QPS 事件流（尤其是 syscall/kprobe 类事件）。
- **配置分离**：Topic、路由、采样、背压策略全部在配置中定义；支持环境变量覆盖。
- **可演进**：后期新增事件类型、字段、Topic，不需要大改架构。
- **可观测**：关键指标（吞吐量、队列水位/容量、drop/采样统计、各阶段处理延迟、Kafka 写入延迟/字节数/速率、Topic 级别指标/分区分布、DLQ 死信队列统计、gRPC 连接状态/重连次数、规范化/序列化错误、批处理统计、Topic 管理状态、资源使用率）。

### 1.2 约束（真实世界）
- Tetragon **不原生直写 Kafka**；推荐通过 gRPC 订阅 + 独立 Consumer。
- Kafka 集群吞吐、Topic 分区数、ACK 策略会决定上限。
- 若开启大量低层事件（kprobe/syscall），事件量可能非常大，需要采样/过滤/分 topic。

---

## 2. 总体架构

```
Kernel
  ↓ eBPF
Tetragon (gRPC GetEvents stream)
  ↓ protobuf stream
Go Consumer
  ├─ Decode + Normalize（protobuf → 稳定 JSON Schema）
  ├─ Route（按事件类型/标签映射到 Topic）
  ├─ Backpressure（队列/丢弃/采样）
  └─ Kafka Producer（批量+并发+压缩）
  ↓
Kafka
  ↓
Flink / Spark / SIEM / ES / ClickHouse / Loki / 自研检测
```

---

## 3. 推荐的生产级实现要点（性能 & 稳定性）

### 3.1 并发模型（推荐）
采用 **“单 stream 读取 + 多 worker 写 Kafka”** 或 **“多 stream + 多 worker”**：

- **Reader**：负责 gRPC `Recv()`，将事件快速入队（尽可能少做重 CPU 的逻辑）。
- **Normalizer/Router**：轻量字段抽取与 topic 决策（可与 Reader 同线程，也可拆成处理池）。
- **Kafka Writers**：多 worker 批量写入 Kafka（每个 worker 有自己的 batch buffer）。

> 典型配置：
- writer workers：`min(16, CPU核数*2)` 起步，观察 Kafka broker 与网络瓶颈再调。
- batch：`max_messages=1000~5000`，`flush_interval=50~200ms`（取决于延迟目标）。
- 压缩：`snappy` / `lz4`（吞吐优先）。

### 3.2 背压策略（必须明确）
事件高峰时要保证 consumer 不 OOM，也不要把 gRPC 卡死导致整体不可控：

**推荐默认：Drop 模式（可配置）**
- 队列满 → 丢弃新事件，并记录 `drop_total{reason="queue_full"}`。
- 适合：高频 syscall/kprobe；防止大流量拖垮系统。

**审计强需求：Block 模式**
- 队列满 → 阻塞 reader，让上游自然“降速”。
- 风险：可能影响 Tetragon stream 的实时性；需谨慎评估。

**采样（可选）**
- 对高频事件（例如某些 syscall）进行采样：`sample_ratio=0.1` 或按事件类型单独配置。

### 3.3 Kafka 写入策略（吞吐关键）
- **异步写入 + 批量**：显著提升吞吐。
- **分 topic**：把高频与低频事件拆开，避免相互影响：
  - `tetragon.process.exec`（低频但高价值）
  - `tetragon.syscall.*`（高频）
  - `tetragon.security.lsm`（策略相关）
- **分区 key**：用 `namespace|pod|binary` 组合做 hash，保证同一工作负载事件聚合，便于后续分析。
- **ACK 策略**：
  - 安全审计：`acks=all`
  - 吞吐优先：`acks=1`（需要结合你们可靠性要求）
- **错误处理**：
  - 写失败：建议进入 **DLQ topic**（如 `tetragon.dlq`）或内存重试队列（带上限）。

### 3.4 断线重连（必须）
- gRPC 断开时：指数退避 + jitter 重连（1s → 2s → 4s … 上限 30s）。
- 重连次数/最后一次错误原因要作为指标输出，便于运维。

---

## 4. 配置设计（Topic 在配置里创建/管理）

### 4.1 配置文件示例（YAML）
> 你要求：topic 由配置决定、可自动创建（开关）

```yaml
tetragon:
  grpc_addr: "tetragon.kube-system.svc:54321"
  tls:
    enabled: false
  stream:
    max_queue: 50000
    drop_if_queue_full: true
    sample_ratio: 1.0

kafka:
  brokers: ["kafka-0.kafka:9092","kafka-1.kafka:9092"]
  client_id: "tetragon-consumer"
  acks: "all"
  compression: "snappy"
  batch:
    max_messages: 3000
    max_bytes: 1048576
    flush_interval_ms: 100
  writer_workers: 12
  topic_admin:
    auto_create: true
    partitions: 24
    replication_factor: 3

routing:
  topics:
    process_exec: "tetragon.process.exec"
    process_exit: "tetragon.process.exit"
    process_lsm: "tetragon.security.lsm"
    process_kprobe: "tetragon.syscall.kprobe"
    process_tracepoint: "tetragon.kernel.tracepoint"
    unknown: "tetragon.unknown"

  partition_key:
    mode: "deduplication"  # ⭐ 关键：用于去重的 key 模式（配合 Compacted Topic）
    fields: ["k8s.namespace","k8s.pod","process.binary","process.pid","timestamp"]  # 生成唯一 key
    separator: ":"

schema:
  version: 1
  mode: "stable_json"  # stable_json / raw_string_fallback
```

### 4.2 Topic 自动创建（建议）
- 开启 `topic_admin.auto_create=true`
- consumer 启动时遍历 `routing.topics` 创建 topic（若已存在则跳过）
- 注意：生产中很多 Kafka 集群禁止客户端创建 topic，需要与平台策略一致。

### 4.3 环境变量覆盖（推荐）
支持通过环境变量覆盖配置，便于 K8s 部署：

```bash
# Tetragon gRPC 连接
export TETRAGON_GRPC_ADDR="tetragon.kube-system.svc:54321"
export TETRAGON_TLS_ENABLED="false"

# Kafka 配置
export KAFKA_BROKERS="kafka-0.kafka:9092,kafka-1.kafka:9092"
export KAFKA_CLIENT_ID="tetragon-consumer"
export KAFKA_ACKS="all"
export KAFKA_COMPRESSION="snappy"

# 性能调优
export STREAM_MAX_QUEUE="50000"
export STREAM_DROP_IF_QUEUE_FULL="true"
export KAFKA_WRITER_WORKERS="12"
export KAFKA_BATCH_MAX_MESSAGES="3000"
export KAFKA_BATCH_FLUSH_INTERVAL_MS="100"

# Topic 路由（JSON 格式）
export ROUTING_TOPICS='{"process_exec":"tetragon.process.exec","process_exit":"tetragon.process.exit"}'

# 日志级别
export LOG_LEVEL="info"  # debug/info/warn/error
```

**优先级**：环境变量 > 配置文件 > 默认值

### 4.4 配置验证（启动时）
- 验证 gRPC 地址格式
- 验证 Kafka brokers 列表非空
- 验证 topic 路由映射完整性
- 验证队列大小、worker 数量合理性
- 验证 TLS/SASL 证书路径（如启用）

---

## 5. 事件 → Topic 的“最佳映射方案”（推荐）

### 5.1 推荐映射原则
1. **低频高价值** 与 **高频低价值** 拆 Topic（避免抢资源）
2. **语义稳定** 的事件单独一个 topic（便于 schema 演进）
3. 需要实时告警的事件：保持 topic 小而精

### 5.2 建议 Topic 集合
- `tetragon.process.exec`：进程执行（告警核心）
- `tetragon.process.exit`：进程退出（关联闭环）
- `tetragon.security.lsm`：LSM 安全事件（阻断/权限检查）
- `tetragon.syscall.kprobe`：kprobe/syscall（高频，建议采样/过滤）
- `tetragon.kernel.tracepoint`：tracepoint（通常更稳定）
- `tetragon.unknown`：兜底（避免丢事件类型）
- `tetragon.dlq`：写入失败/序列化失败/字段异常

### 5.3 Partition key（推荐）
- 默认：`namespace|pod|binary`
- 若你更关心“进程树”：`host|pid|tgid` 组合也可，但跨 pod 聚合较弱。

---

## 6. 事件规范化（稳定 JSON Schema，强烈推荐）

### 6.1 为什么要规范化
protobuf 事件结构会随版本演进；下游消费（Flink/ES）更适合稳定 JSON schema。

### 6.2 推荐输出 JSON（示例）
```json
{
  "schema_version": 1,
  "type": "process_exec",
  "ts": "2026-01-10T12:34:56.789Z",
  "node": "node-1",
  "k8s": { "namespace": "default", "pod": "nginx-123", "container": "nginx" },
  "process": { "pid": 1234, "ppid": 1, "uid": 0, "binary": "/bin/bash", "args": ["-c","curl","http://x"] },
  "labels": { "source": "tetragon", "cluster": "prod" },
  "raw": null
}
```

### 6.3 兼容策略（避免升级炸裂）
- 必备字段缺失时：保底填空值，保证 JSON 可解析。
- 对未知事件：输出 `type="unknown"` + `raw`（字符串/压缩后 pb bytes）。
- schema 版本：`schema_version` 放在每条消息中，下游按版本处理。

---

## 7. 大并发/性能调优清单（你可以按这个压测）

### 7.1 关键参数建议
- `max_queue`: 20k~200k（结合内存与峰值）
- `writer_workers`: 8~32（先从 12/16 起步）
- `batch.max_messages`: 1000~5000
- `flush_interval_ms`: 50~200
- `compression`: snappy 或 lz4
- `acks`: all（可靠）或 1（吞吐）

### 7.2 典型瓶颈定位
1. **Kafka broker 写入上限**：分区数不足、ACK 太严、磁盘慢
2. **网络带宽**：consumer → broker 网络吞吐
3. **CPU**：JSON 生成/字段抽取/序列化
4. **队列水位**：持续满说明下游写入跟不上，必须扩 worker 或降采样

### 7.3 建议的指标（Prometheus）

**吞吐量指标**
- `events_in_total{type=...}` - 接收的事件总数（按类型）
- `events_out_total{topic=...,status=...}` - 写入 Kafka 的事件数（status: success/failed）
- `grpc_events_received_total` - gRPC 接收的事件总数

**队列指标**
- `queue_depth` - 当前队列深度
- `queue_capacity` - 队列容量
- `queue_usage_ratio` - 队列使用率（queue_depth / queue_capacity）

**丢弃与采样指标**
- `drops_total{reason=...}` - 丢弃的事件数（reason: queue_full/sampled/invalid）
- `sampled_total{type=...}` - 采样的事件数（按类型）

**延迟指标**
- `kafka_write_latency_ms_bucket{topic=...}` - Kafka 写入延迟（Histogram，分桶）
- `normalize_latency_ms_bucket{type=...}` - 规范化处理延迟
- `route_latency_ms_bucket` - 路由决策延迟

**Kafka 写入指标**
- `kafka_write_bytes_total{topic=...}` - 写入 Kafka 的字节数
- `kafka_write_messages_total{topic=...}` - 写入 Kafka 的消息数
- `kafka_batch_size_bucket{topic=...}` - 批处理大小分布（按 topic）
- `kafka_batch_flush_total{topic=...}` - 批处理刷新次数（按 topic）
- `kafka_message_size_bytes_bucket{topic=...}` - 单条消息大小分布

**Topic 级别指标**
- `kafka_topic_write_rate{topic=...}` - Topic 写入速率（messages/sec）
- `kafka_topic_write_bytes_rate{topic=...}` - Topic 写入字节速率（bytes/sec）
- `kafka_topic_partition_count{topic=...}` - Topic 分区数
- `kafka_topic_write_errors_total{topic=...,partition=...}` - Topic 分区写入错误数
- `kafka_topic_metadata_age_seconds{topic=...}` - Topic 元数据年龄（用于检测元数据过期）

**DLQ（死信队列）指标**
- `dlq_events_total{reason=...}` - 写入 DLQ 的事件数（reason: write_failed/serialize_failed/invalid_schema/too_large）
- `dlq_events_bytes_total` - 写入 DLQ 的字节总数
- `dlq_retry_attempts_total{reason=...}` - DLQ 重试次数
- `dlq_events_by_topic_total{topic=...,reason=...}` - 按原始 topic 分类的 DLQ 事件数

**错误指标**
- `kafka_errors_total{error_type=...,topic=...}` - Kafka 错误数（error_type: write/timeout/network/partition_leader_not_available）
- `normalize_errors_total{event_type=...}` - 规范化失败数
- `grpc_errors_total{error_type=...}` - gRPC 错误数
- `route_errors_total{reason=...}` - 路由错误数（reason: unknown_topic/invalid_config）
- `serialize_errors_total{event_type=...}` - 序列化错误数

**Topic 管理指标**
- `kafka_topic_create_total{status=...}` - Topic 创建次数（status: success/failed）
- `kafka_topic_create_duration_seconds` - Topic 创建耗时
- `kafka_topic_exists{topic=...}` - Topic 是否存在（0/1）
- `kafka_topic_metadata_refresh_total` - Topic 元数据刷新次数
- `kafka_topic_metadata_refresh_errors_total` - Topic 元数据刷新失败次数

**Topic 健康度指标（Producer 视角）**
- `kafka_topic_partition_leader_available{topic=...,partition=...}` - 分区 Leader 是否可用（0/1）
- `kafka_topic_partition_write_success_rate{topic=...,partition=...}` - 分区写入成功率（0-1）
- `kafka_topic_unavailable_partitions{topic=...}` - Topic 不可用分区数
- `kafka_topic_write_throttle_total{topic=...}` - Topic 写入被限流次数（如果 broker 配置了限流）

**注意**：作为 Producer，无法直接监控 Consumer Lag（积压），但可以通过以下方式间接评估：
- 监控各 Topic 写入速率，如果持续高于预期可能表示下游消费慢
- 监控 DLQ 增长速率，如果 DLQ 持续增长说明有持续写入失败
- 建议在 Kafka 集群层面监控 Consumer Lag（使用 Kafka 自带的 JMX 指标或第三方工具）

### 7.4 告警规则建议（Prometheus AlertManager）

```yaml
groups:
- name: tetragon_consumer
  interval: 30s
  rules:
  # 队列告警
  - alert: ConsumerQueueFull
    expr: queue_depth / queue_capacity > 0.9
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Consumer queue is nearly full ({{ $value | humanizePercentage }})"
      description: "Queue usage: {{ $value | humanizePercentage }}, depth: {{ $labels.queue_depth }}, capacity: {{ $labels.queue_capacity }}"
      
  - alert: ConsumerHighDropRate
    expr: rate(drops_total[5m]) > 100
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "High event drop rate: {{ $value | humanize }} events/sec"
      description: "Drop rate: {{ $value | humanize }}/sec, reason: {{ $labels.reason }}"
      
  # Kafka 写入告警
  - alert: ConsumerKafkaWriteFailure
    expr: rate(kafka_errors_total[5m]) > 10
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "Kafka write failures detected: {{ $value | humanize }}/sec"
      description: "Error type: {{ $labels.error_type }}, topic: {{ $labels.topic }}"
      
  - alert: ConsumerKafkaWriteLatencyHigh
    expr: histogram_quantile(0.99, kafka_write_latency_ms_bucket) > 1000
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "Kafka write latency P99 > 1s"
      description: "P99 latency: {{ $value }}ms for topic {{ $labels.topic }}"
      
  # Topic 级别告警
  - alert: ConsumerTopicWriteRateLow
    expr: kafka_topic_write_rate < 1
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "Topic {{ $labels.topic }} write rate is very low"
      description: "Write rate: {{ $value }} messages/sec"
      
  - alert: ConsumerTopicPartitionUnavailable
    expr: kafka_topic_unavailable_partitions > 0
    for: 2m
    labels:
      severity: critical
    annotations:
      summary: "Topic {{ $labels.topic }} has unavailable partitions"
      description: "Unavailable partitions: {{ $value }}"
      
  # DLQ 告警
  - alert: ConsumerDLQEventsHigh
    expr: rate(dlq_events_total[5m]) > 10
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "High DLQ event rate: {{ $value | humanize }}/sec"
      description: "DLQ events: {{ $value | humanize }}/sec, reason: {{ $labels.reason }}, original topic: {{ $labels.topic }}"
      
  - alert: ConsumerDLQGrowthRate
    expr: rate(dlq_events_bytes_total[10m]) > 1048576  # 1MB/sec
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "DLQ is growing rapidly"
      description: "DLQ growth rate: {{ $value | humanize1024 }}B/sec"
      
  # gRPC 连接告警
  - alert: ConsumerGrpcDisconnected
    expr: grpc_stream_uptime_seconds == 0
    for: 1m
    labels:
      severity: critical
    annotations:
      summary: "gRPC stream disconnected"
      description: "gRPC stream has been disconnected for more than 1 minute"
      
  - alert: ConsumerGrpcFrequentReconnect
    expr: rate(grpc_reconnect_total[10m]) > 3
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "gRPC frequent reconnects"
      description: "Reconnect rate: {{ $value | humanize }}/10min"
      
  # 规范化错误告警
  - alert: ConsumerNormalizeErrorsHigh
    expr: rate(normalize_errors_total[5m]) > 50
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "High normalization error rate"
      description: "Normalize errors: {{ $value | humanize }}/sec for event type {{ $labels.event_type }}"
      
  # 资源告警
  - alert: ConsumerHighMemoryUsage
    expr: go_memstats_alloc_bytes > 2147483648  # 2GB
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Consumer memory usage is high"
      description: "Memory allocated: {{ $value | humanize1024 }}B"
      
  - alert: ConsumerHighGoroutineCount
    expr: go_goroutines > 1000
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "Consumer has too many goroutines"
      description: "Goroutine count: {{ $value }}"
```

**连接状态指标**
- `grpc_reconnect_total` - gRPC 重连次数
- `grpc_stream_uptime_seconds` - gRPC stream 运行时长
- `kafka_producer_connected` - Kafka producer 连接状态（0/1）

**资源使用指标**
- `process_uptime_seconds` - 进程运行时长
- `go_memstats_alloc_bytes` - 当前内存分配（Go runtime）
- `go_goroutines` - 当前 goroutine 数量

---

## 8. 后期新增事件怎么做（你关心的重点）

你要新增事件（例如新增 `ProcessConnect` 或某类 tracepoint），建议按 **“配置驱动 + 插件式解析”** 来做。

### 8.1 新增事件的最小步骤（推荐流程）
1. **升级 Tetragon / pb**
   - 拉取新版本 protobuf 并重新生成 `*.pb.go`
2. **在 Router 里新增 detectType 分支**
   - 识别新事件 oneof / getter
3. **在 Normalizer 新增字段抽取（可选）**
   - 把新事件的关键字段抽平到稳定 JSON
4. **在配置中新增 topic 映射**
   - `routing.topics.<new_type> = "tetragon.xxx"`
5. **（可选）启用 topic_admin 自动创建**
6. **压测 + 观察指标**
   - 新事件是否导致吞吐波动

### 8.2 “不用改代码也能扩展”的方式（更专业）
- **规则 1：默认 unknown → 兜底 topic**
  - 新事件来了即使你没加代码，也不会丢（会进 unknown）
- **规则 2：raw_fallback**
  - 在 JSON 中保留 `raw`（字符串或 base64 pb bytes）
  - 之后再渐进式补齐 normalizer

> 推荐策略：
- **短期**：unknown topic + raw
- **中期**：为关键事件补字段抽取 + 独立 topic
- **长期**：版本化 schema（schema_version）

### 8.3 推荐的代码结构（便于扩展）
- `router/detect.go`：只负责事件类型识别
- `normalize/process_exec.go`：每类事件一个文件
- `schema/v1/*.go`：稳定 schema 输出
- `routing/config.go`：只读配置，新增 type 只改配置和一个 normalize 文件

---

## 12. 完整代码目录结构（推荐）

### 12.1 项目根目录结构

```
tetragon-kafka-consumer/
├── cmd/
│   └── consumer/
│       └── main.go                 # 程序入口，初始化、信号处理、优雅关闭
├── internal/
│   ├── config/
│   │   ├── config.go               # 配置结构体定义
│   │   ├── loader.go               # 配置加载（YAML + 环境变量）
│   │   └── validator.go            # 配置验证
│   ├── grpc/
│   │   ├── client.go               # gRPC 客户端封装
│   │   ├── stream.go               # GetEvents stream 管理
│   │   └── reconnect.go            # 断线重连逻辑（指数退避）
│   ├── router/
│   │   ├── detect.go               # 事件类型识别（switch on oneof）
│   │   └── router.go               # Topic 路由决策
│   ├── normalize/
│   │   ├── normalizer.go           # 规范化接口
│   │   ├── process_exec.go         # process_exec 事件规范化
│   │   ├── process_exit.go         # process_exit 事件规范化
│   │   ├── process_lsm.go          # LSM 事件规范化
│   │   ├── process_kprobe.go       # kprobe 事件规范化
│   │   ├── process_tracepoint.go   # tracepoint 事件规范化
│   │   └── unknown.go              # 未知事件兜底处理
│   ├── schema/
│   │   └── v1/
│   │       ├── schema.go           # 稳定 JSON schema 定义
│   │       └── encoder.go          # JSON 编码器
│   ├── queue/
│   │   ├── queue.go                # 内存队列（带背压策略）
│   │   └── sampler.go               # 采样器（按类型/比例）
│   ├── kafka/
│   │   ├── producer.go             # Kafka producer 封装
│   │   ├── writer.go               # 批量写入 worker
│   │   ├── topic_admin.go          # Topic 自动创建/管理
│   │   └── partition_key.go        # Partition key 生成
│   ├── metrics/
│   │   ├── prometheus.go           # Prometheus 指标定义
│   │   └── collector.go            # 指标收集器
│   ├── health/
│   │   └── server.go               # HTTP 健康检查端点
│   ├── leader/
│   │   ├── election.go             # K8s Leader Election（K8s 环境）
│   │   ├── redis_lock.go           # Redis 分布式锁（不依赖 K8s）
│   │   └── etcd_lock.go            # etcd 分布式锁（不依赖 K8s）
│   └── logger/
│       └── logger.go               # 结构化日志（zap/logrus）
├── pkg/
│   └── tetragon/                   # Tetragon protobuf 生成代码（可选，可引用官方包）
│       └── api/
├── configs/
│   ├── config.yaml                 # 默认配置文件
│   └── config.example.yaml         # 配置示例
├── deployments/
│   ├── k8s/
│   │   ├── deployment.yaml         # K8s Deployment
│   │   ├── daemonset.yaml          # K8s DaemonSet（可选）
│   │   ├── configmap.yaml          # ConfigMap
│   │   ├── service.yaml            # Service（健康检查）
│   │   └── serviceaccount.yaml     # ServiceAccount
│   └── helm/
│       └── tetragon-consumer/
│           ├── Chart.yaml
│           ├── values.yaml
│           └── templates/
├── scripts/
│   ├── generate-proto.sh           # protobuf 生成脚本
│   └── build.sh                    # 构建脚本
├── Dockerfile                       # 多阶段构建
├── .dockerignore
├── go.mod                           # Go 模块依赖
├── go.sum
├── Makefile                         # 构建/测试/运行命令
├── .gitignore
├── README.md                        # 项目说明
└── CHANGELOG.md                     # 版本变更日志
```

### 12.2 核心模块说明

#### `cmd/consumer/main.go`（主程序）
- 初始化配置、日志、metrics
- 启动 gRPC stream reader
- 启动 Kafka writer workers
- 启动健康检查 HTTP 服务器
- 处理 SIGTERM/SIGINT（优雅关闭）
- 等待所有 goroutine 退出

#### `internal/grpc/`（gRPC 客户端）
- `client.go`：连接管理、TLS 配置
- `stream.go`：`GetEvents` stream 订阅、事件接收循环
- `reconnect.go`：指数退避重连（1s → 2s → 4s ... max 30s）

#### `internal/router/`（路由）
- `detect.go`：从 protobuf 事件中识别类型（`process_exec` / `process_exit` / `kprobe` 等）
- `router.go`：根据事件类型 + 配置映射到 Kafka topic

#### `internal/normalize/`（规范化）
- 每个事件类型一个文件，抽取关键字段到稳定 JSON schema
- `unknown.go`：未知事件兜底（输出 raw protobuf bytes）

#### `internal/kafka/`（Kafka 写入）
- `producer.go`：Sarama/Confluent Kafka producer 封装
- `writer.go`：批量写入 worker（每个 worker 独立 batch buffer）
- `topic_admin.go`：启动时自动创建 topic（幂等）
- `partition_key.go`：根据配置生成 partition key

#### `internal/queue/`（队列与背压）
- `queue.go`：带容量限制的内存队列（channel-based）
- `sampler.go`：按事件类型/采样比例过滤

#### `internal/metrics/`（可观测）
- Prometheus 指标：吞吐、队列深度、drop、延迟、错误、重连

#### `internal/health/`（健康检查）
- HTTP `/health`：返回 gRPC 连接状态、队列水位、Kafka 连接状态
- HTTP `/ready`：返回是否可接收流量（用于 K8s readiness probe）

#### `internal/leader/`（分布式锁，可选）
- `election.go`：K8s Leader Election 实现（K8s 环境，使用 K8s Lease API）
- `redis_lock.go`：Redis 分布式锁实现（不依赖 K8s，通用方案）
- `etcd_lock.go`：etcd 分布式锁实现（不依赖 K8s，通用方案）
- 只有获得锁的 Pod 执行实际的 consumer 逻辑，避免重复数据

### 12.3 依赖管理（go.mod 示例）

```go
module github.com/yourorg/tetragon-kafka-consumer

go 1.21

require (
    // Tetragon gRPC API
    github.com/cilium/tetragon/api v1.0.0
    
    // Kafka 客户端（二选一）
    github.com/IBM/sarama v1.43.0          // 或
    github.com/confluentinc/confluent-kafka-go/v2 v2.3.0
    
    // 配置管理
    github.com/spf13/viper v1.18.0
    gopkg.in/yaml.v3 v3.0.1
    
    // gRPC
    google.golang.org/grpc v1.60.0
    google.golang.org/protobuf v1.31.0
    
    // 日志
    go.uber.org/zap v1.26.0                 // 或 github.com/sirupsen/logrus v1.9.3
    
    // 指标
    github.com/prometheus/client_golang v1.18.0
    
    // 工具
    golang.org/x/sync v0.5.0
    github.com/google/uuid v1.5.0
    
    // K8s 客户端（K8s Leader Election 需要，可选）
    k8s.io/client-go v0.28.0
    k8s.io/api v0.28.0
    k8s.io/apimachinery v0.28.0
    
    // Redis 客户端（Redis 分布式锁，可选）
    github.com/go-redis/redis/v8 v8.11.5
    
    // etcd 客户端（etcd 分布式锁，可选）
    go.etcd.io/etcd/clientv3 v3.5.9
)
```

---

## 9. 安全与可靠性建议（生产必看）

### 9.1 安全配置
- **Kafka TLS/SASL**：建议启用（特别是跨网络/多租户）
  ```yaml
  kafka:
    tls:
      enabled: true
      ca_cert: "/etc/kafka/ca.crt"
      client_cert: "/etc/kafka/client.crt"
      client_key: "/etc/kafka/client.key"
    sasl:
      enabled: true
      mechanism: "PLAIN"  # 或 SCRAM-SHA-256/SCRAM-SHA-512
      username: "tetragon-consumer"
      password_file: "/etc/kafka/password"  # 从 Secret 挂载
  ```
- **gRPC TLS**：生产环境建议启用
  ```yaml
  tetragon:
    tls:
      enabled: true
      ca_cert: "/etc/tetragon/ca.crt"
      client_cert: "/etc/tetragon/client.crt"
      client_key: "/etc/tetragon/client.key"
  ```

### 9.2 可靠性保障
- **消息大小限制**：限制单条 message 最大值（避免 args 超长导致 OOM）
  ```yaml
  kafka:
    max_message_bytes: 1048576  # 1MB
  ```
- **资源限制**：K8s 给 consumer 设置 requests/limits，避免抢占
  ```yaml
  resources:
    requests:
      cpu: "500m"
      memory: "512Mi"
    limits:
      cpu: "2000m"
      memory: "2Gi"
  ```
- **DLQ（Dead Letter Queue）**：写失败或序列化失败必须可回收（安全数据不能默默丢）
  - 所有写入失败的事件进入 `tetragon.dlq` topic
  - 记录失败原因、原始事件、时间戳
  - 定期人工审查 DLQ，修复后重新处理

### 9.3 优雅关闭（Graceful Shutdown）
- 接收 SIGTERM/SIGINT 后：
  1. 停止接收新事件（关闭 gRPC stream）
  2. 等待队列中的事件处理完成（设置超时，如 30s）
  3. 等待所有 Kafka writer workers 完成当前 batch 并 flush
  4. 关闭 Kafka producer
  5. 输出最终指标
  6. 退出

```go
// 伪代码示例
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// 启动所有组件
go grpcReader(ctx)
go kafkaWriters(ctx)

// 等待信号
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
<-sigChan

// 优雅关闭
cancel()
shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
defer shutdownCancel()

// 等待队列清空
waitForQueueEmpty(shutdownCtx)
// 等待 writers 完成
waitForWritersDone(shutdownCtx)
```

---

## 10. 部署建议（K8s）

### 10.0 ⚠️ 重要：分布式部署与重复数据问题

#### 10.0.1 问题分析

**场景 1：Deployment 多副本 + 同一 Tetragon endpoint**
```
Pod-1 ──┐
Pod-2 ──┼──> Tetragon gRPC (同一 endpoint) ──> 每个 Pod 收到相同事件流
Pod-3 ──┘
         ↓
    每个 Pod 都写入 Kafka
         ↓
    Kafka Topic: 每个事件被写入 3 次（重复！）
```

**问题**：
- 多个 Pod 订阅同一个 Tetragon gRPC endpoint 时，**每个 Pod 都会收到完整的事件流**
- 导致每个事件被写入 Kafka **N 次**（N = Pod 副本数）
- 下游消费者会收到重复数据，影响分析和存储成本

**场景 2：DaemonSet + 节点级 Tetragon**
```
Node-1: Pod-1 ──> Tetragon-1 (本节点) ──> Kafka (不重复)
Node-2: Pod-2 ──> Tetragon-2 (本节点) ──> Kafka (不重复)
Node-3: Pod-3 ──> Tetragon-3 (本节点) ──> Kafka (不重复)
```
**优点**：每个 Pod 只订阅本节点的 Tetragon，事件不重复

#### 10.0.2 解决方案对比（分布式 Deployment 模式）

> **⭐ 强烈推荐：方案 1 - Kafka Compacted Topic + 消息 Key**
> 
> 这是**最佳方案**，利用 Kafka 自身机制自动去重，不依赖任何外部服务，所有 Pod 可同时工作，实现简单。

| 方案 | 部署模式 | 依赖 | 是否重复 | 适用场景 | 复杂度 | 推荐度 |
|------|---------|------|---------|---------|--------|--------|
| **⭐ 方案 1：Kafka Compacted Topic + 消息 Key** | Deployment | ❌ 不依赖外部 | ❌ 不重复 | **任何环境，利用 Kafka 自身机制** | ⭐⭐ 低 | ⭐⭐⭐⭐⭐ **强烈推荐（首选）** |
| 方案 2：Kafka 事务性 Producer | Deployment | ❌ 不依赖外部 | ❌ 不重复 | 需要 exactly-once 语义 | ⭐⭐⭐ 中 | ⭐⭐⭐ 备选 |
| 方案 3：K8s Leader Election | Deployment | ⚠️ 依赖 K8s | ❌ 不重复 | K8s 环境，无法使用 Compacted Topic 时 | ⭐⭐⭐ 中 | ⭐⭐ 备选 |
| 方案 4：外部协调服务（Redis/etcd） | Deployment | ⚠️ 依赖外部服务 | ❌ 不重复 | 无法使用 Kafka 特性时 | ⭐⭐⭐ 中 | ⭐⭐ 备选 |
| 方案 5：下游去重 | Deployment | ❌ 不依赖外部 | ⚠️ 需要去重逻辑 | 下游已有去重能力 | ⭐⭐ 中 | ⭐⭐ 备选 |

#### 10.0.3 推荐方案详解（分布式 Deployment）

> **⭐ 首选方案：Kafka Compacted Topic + 消息 Key**

**方案 1：Kafka Compacted Topic + 消息 Key（⭐ 强烈推荐，利用 Kafka 自身机制）**

**架构**：
```
Deployment (replicas: 3)
  ├─ Pod-1 ──┐
  ├─ Pod-2 ──┼──> 都订阅同一 Tetragon ──> 都写入 Kafka（使用相同 Key）
  └─ Pod-3 ──┘
              ↓
    Kafka Compacted Topic
    (相同 Key 的消息自动去重，只保留最新)
              ↓
         Kafka Topic (无重复)
```

**核心原理**：
- 使用 **Kafka Compacted Topic**：Kafka 会自动保留每个 Key 的最新消息，删除旧消息
- 所有 Pod 使用**相同的消息 Key**（基于事件唯一标识生成）
- 即使多个 Pod 写入相同事件，Kafka 也会自动去重，只保留最后一条

**实现要点**：
1. **Topic 配置为 Compacted**：
   ```yaml
   kafka:
     topic_admin:
       cleanup_policy: "compact"  # 启用 log compaction
       min_cleanable_dirty_ratio: 0.5
   ```

2. **消息 Key 生成**（基于事件唯一标识）：
   ```go
   // 生成唯一的事件 Key
   messageKey := fmt.Sprintf("%s:%s:%d:%d", 
       event.NodeName,
       event.Type,
       event.Process.Pid,
       event.Timestamp)
   ```

3. **Producer 配置**：
   ```yaml
   kafka:
     producer:
       enable_idempotence: true  # 启用幂等性（防止网络重试重复）
       acks: "all"                # 确保消息持久化
   ```

**优点**：
- ✅ **不依赖外部服务**：完全利用 Kafka 自身机制
- ✅ **自动去重**：Kafka 自动处理，无需下游去重逻辑
- ✅ **无重复数据**：Kafka 层面保证每个 Key 只有最新消息
- ✅ **真正的分布式**：所有 Pod 都在工作，无单点故障
- ✅ **零切换中断**：Pod 故障不影响其他 Pod
- ✅ **实现简单**：只需要配置 Topic 为 Compacted，使用消息 Key

**缺点**：
- ⚠️ **需要合理设置 Key**：Key 必须能唯一标识事件
- ⚠️ **Compaction 延迟**：去重不是实时的，有短暂延迟（通常几秒到几分钟）
- ⚠️ **存储策略**：Compacted Topic 会保留所有 Key 的最新值，需要合理设置 retention

**适用场景**：
- ✅ **生产环境分布式部署（强烈推荐）**
- ✅ **任何部署环境（不依赖 K8s、Redis、etcd）**
- ✅ **所有 Pod 可同时工作，无单点故障**
- ✅ 可以接受 Compaction 延迟（通常几秒到几分钟）
- ✅ 事件有唯一标识（node + type + pid + timestamp）

> **💡 为什么选择这个方案？**
> - ✅ **最简单**：只需配置 Topic 为 Compacted，使用消息 Key
> - ✅ **最可靠**：利用 Kafka 自身机制，不依赖外部服务
> - ✅ **最高可用**：所有 Pod 都在工作，无单点故障
> - ✅ **零切换中断**：Pod 故障不影响其他 Pod
> - ✅ **自动去重**：Kafka 自动处理，无需额外逻辑

---

**方案 2：Kafka 事务性 Producer（Exactly-Once 语义，备选方案）**

**架构**：
```
Deployment (replicas: 3)
  ├─ Pod-1 ──┐
  ├─ Pod-2 ──┼──> 分布式锁（Redis/etcd） ──> 只有获得锁的 Pod 订阅 Tetragon
  └─ Pod-3 ──┘
              ↓
         Leader Pod ──> Tetragon gRPC ──> Kafka (无重复)
         
Leader 故障时：锁过期，其他 Pod 自动竞争获得锁（通常在 5-10 秒内完成切换）
```

**核心原理**：
- 使用 Redis 或 etcd 实现分布式锁
- 多个 Pod 竞争同一个锁，只有获得锁的 Pod 成为 Leader
- Leader Pod 负责订阅 gRPC stream 并写入 Kafka
- 其他 Pod 处于 Standby 状态，定期尝试获取锁

**实现要点**：
1. **分布式锁实现**：
   - Redis：使用 `SET key value NX EX ttl` 实现（推荐使用 `github.com/go-redis/redis/v8`）
   - etcd：使用 `etcd/clientv3/concurrency` 包实现
2. **锁续期机制**：Leader Pod 需要定期续期锁（heartbeat）
3. **故障切换**：Leader Pod 故障时，锁过期，其他 Pod 自动竞争获得锁
4. **优雅切换**：Leader 失去锁时，优雅关闭 gRPC stream 和 Kafka writer

**优点**：
- ✅ **不依赖 K8s**：可以在任何环境使用（Docker、VM、裸机等）
- ✅ **无重复数据**：同一时刻只有一个 Pod 在写入
- ✅ **高可用**：Leader 故障自动切换，通常 5-10 秒恢复
- ✅ **支持水平扩展**：可以增加 Pod 副本数提高可用性
- ✅ **通用方案**：适用于各种部署环境

**缺点**：
- ⚠️ Leader 切换时有短暂中断（5-10 秒）
- ⚠️ 需要额外的协调服务（Redis/etcd）
- ⚠️ 实现复杂度中等（需要处理锁续期和切换逻辑）

**适用场景**：
- ✅ 生产环境分布式部署（不依赖 K8s）
- ✅ 任何部署环境（Docker、VM、裸机、K8s 等）
- ✅ 已有 Redis/etcd 基础设施
- ✅ 需要高可用但可以接受短暂中断

**方案 3：K8s Leader Election（备选方案，仅当无法使用 Compacted Topic 时）**

> **注意**：此方案依赖 K8s 的 Lease API，仅适用于 K8s 环境。**优先使用方案 1（Kafka Compacted Topic）**，只有在无法使用 Compacted Topic 时才考虑此方案。

**方案 4：外部协调服务（Redis/etcd）实现分布式锁（备选方案，仅当无法使用 Compacted Topic 时）**

**方案 5：Kafka 端去重（下游去重，备选方案）**

**架构**：
```
Deployment (replicas: 3)
  ├─ Pod-1 ──┐
  ├─ Pod-2 ──┼──> 都订阅同一 Tetragon ──> 都写入 Kafka（可能有重复）
  └─ Pod-3 ──┘
              ↓
         Kafka Topic (可能有重复消息)
              ↓
         下游去重（Flink/Spark/数据库）
```

**核心原理**：
- 所有 Pod 都订阅同一个 Tetragon，都写入 Kafka
- 通过消息去重键（deduplication key）标识相同事件
- 下游消费时基于去重键去重

**实现要点**：
1. **消息去重键生成**：基于事件关键字段（node + type + pid + timestamp）
2. **Kafka Producer 幂等性**：`enable.idempotence=true`（防止网络重试重复）
3. **下游去重**：Flink/Spark 使用 `keyBy()` 去重，或数据库唯一索引

**优点**：
- ✅ **真正的分布式**：所有 Pod 都在工作，无单点故障
- ✅ **无切换中断**：Pod 故障不影响其他 Pod
- ✅ **实现相对简单**：不需要 Leader Election 逻辑

**缺点**：
- ⚠️ **增加存储开销**：Kafka 中可能有重复数据（N 倍）
- ⚠️ **增加网络开销**：重复数据在网络中传输
- ⚠️ **下游需要去重**：必须在消费端实现去重逻辑
- ⚠️ **去重窗口设置**：需要合理设置去重窗口，避免误删

**适用场景**：
- ✅ 生产环境分布式部署
- ✅ 下游已有去重能力（Flink/Spark/数据库）
- ✅ 可以接受 Kafka 存储开销增加
- ✅ 需要零切换中断

#### 10.0.4 代码设计建议（分布式 Deployment）

**1. Kafka Compacted Topic 实现（⭐ 推荐，利用 Kafka 自身机制）**

```go
// internal/kafka/dedup_key.go
package kafka

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    
    "github.com/cilium/tetragon/api/v1/tetragon"
)

// GenerateDedupKey 生成用于去重的消息 Key
// 这个 Key 会用于 Kafka Compacted Topic，相同 Key 的消息会自动去重
func GenerateDedupKey(event *tetragon.GetEventsResponse) string {
    // 基于事件唯一标识生成 Key
    // 格式：node:type:pid:timestamp
    var keyParts []string
    
    // Node 名称
    if event.NodeName != "" {
        keyParts = append(keyParts, event.NodeName)
    } else {
        keyParts = append(keyParts, "unknown")
    }
    
    // 事件类型
    eventType := detectEventType(event)
    keyParts = append(keyParts, eventType)
    
    // 进程信息（如果有）
    if event.ProcessExec != nil {
        keyParts = append(keyParts, fmt.Sprintf("%d:%d", 
            event.ProcessExec.Process.Pid,
            event.ProcessExec.Process.StartTime))
    } else if event.ProcessExit != nil {
        keyParts = append(keyParts, fmt.Sprintf("%d:%d", 
            event.ProcessExit.Process.Pid,
            event.ProcessExit.Process.StartTime))
    } else {
        // 其他事件类型，使用时间戳
        keyParts = append(keyParts, fmt.Sprintf("%d", event.Time))
    }
    
    // 时间戳（纳秒级，确保唯一性）
    keyParts = append(keyParts, fmt.Sprintf("%d", event.Time))
    
    // 组合并生成 Hash（可选，如果 Key 太长）
    key := fmt.Sprintf("%s", keyParts)
    if len(key) > 100 {
        // Key 太长，使用 Hash
        h := sha256.New()
        h.Write([]byte(key))
        return hex.EncodeToString(h.Sum(nil))[:32]
    }
    
    return key
}

// internal/kafka/producer.go
func (p *Producer) SendMessage(ctx context.Context, topic string, event *tetragon.GetEventsResponse, value []byte) error {
    // 生成去重 Key
    key := GenerateDedupKey(event)
    
    // 发送消息（使用 Key）
    msg := &sarama.ProducerMessage{
        Topic: topic,
        Key:   sarama.StringEncoder(key),  // 使用 Key，Kafka Compacted Topic 会自动去重
        Value: sarama.ByteEncoder(value),
        Headers: []sarama.RecordHeader{
            {Key: []byte("event_type"), Value: []byte(detectEventType(event))},
            {Key: []byte("node"), Value: []byte(event.NodeName)},
        },
    }
    
    _, _, err := p.producer.SendMessage(msg)
    return err
}

// internal/kafka/topic_admin.go
func (ta *TopicAdmin) CreateCompactedTopic(ctx context.Context, topic string, partitions int, replicationFactor int16) error {
    topicDetail := &sarama.TopicDetail{
        NumPartitions:     int32(partitions),
        ReplicationFactor: replicationFactor,
        ConfigEntries: map[string]*string{
            "cleanup.policy": stringPtr("compact"),  // 启用 log compaction
            "min.cleanable.dirty.ratio": stringPtr("0.5"),
            "segment.ms": stringPtr("3600000"),  // 1 小时
        },
    }
    
    return ta.admin.CreateTopic(topic, topicDetail, false)
}

func stringPtr(s string) *string {
    return &s
}
```

**2. Kafka 事务性 Producer 实现**

```go
// internal/kafka/transactional_producer.go
func NewTransactionalProducer(config *Config) (*TransactionalProducer, error) {
    saramaConfig := sarama.NewConfig()
    saramaConfig.Producer.Transactional.ID = config.Producer.TransactionalID  // 每个 Pod 唯一
    saramaConfig.Producer.Idempotent = true
    saramaConfig.Producer.RequiredAcks = sarama.WaitForAll
    saramaConfig.Producer.MaxInFlightRequests = 1  // 事务必需
    
    producer, err := sarama.NewSyncProducer(config.Brokers, saramaConfig)
    if err != nil {
        return nil, err
    }
    
    // 初始化事务
    err = producer.BeginTxn()
    if err != nil {
        return nil, err
    }
    
    return &TransactionalProducer{producer: producer}, nil
}

func (tp *TransactionalProducer) SendAndCommit(ctx context.Context, topic string, key string, value []byte) error {
    msg := &sarama.ProducerMessage{
        Topic: topic,
        Key:   sarama.StringEncoder(key),
        Value: sarama.ByteEncoder(value),
    }
    
    err := tp.producer.SendMessage(msg)
    if err != nil {
        tp.producer.AbortTxn()
        return err
    }
    
    // 提交事务
    return tp.producer.CommitTxn()
}
```

**3. K8s Leader Election 实现（K8s 环境，依赖 K8s）**

> **注意**：此方案依赖 K8s Lease API，仅适用于 K8s 环境。优先考虑方案 1（Kafka Compacted Topic）。

```go
// internal/leader/election.go
package leader

import (
    "context"
    "os"
    "time"
    
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/leaderelection"
    "k8s.io/client-go/tools/leaderelection/resourcelock"
)

type Config struct {
    LeaseName      string
    LeaseNamespace string
    LeaseDuration  time.Duration
    RenewDeadline  time.Duration
    RetryPeriod   time.Duration
}

func RunWithLeaderElection(ctx context.Context, cfg Config, kubeClient kubernetes.Interface, runFunc func(context.Context)) {
    podName := os.Getenv("POD_NAME")
    if podName == "" {
        podName = os.Getenv("HOSTNAME")
    }
    
    lock := &resourcelock.LeaseLock{
        LeaseMeta: metav1.ObjectMeta{
            Name:      cfg.LeaseName,
            Namespace: cfg.LeaseNamespace,
        },
        Client: kubeClient.CoordinationV1(),
        LockConfig: resourcelock.ResourceLockConfig{
            Identity: podName,
        },
    }
    
    lec := leaderelection.LeaderElectionConfig{
        Lock:            lock,
        LeaseDuration:   cfg.LeaseDuration,
        RenewDeadline:   cfg.RenewDeadline,
        RetryPeriod:    cfg.RetryPeriod,
        Callbacks: leaderelection.LeaderCallbacks{
            OnStartedLeading: func(ctx context.Context) {
                log.Info("Became leader, starting consumer...")
                runFunc(ctx)
            },
            OnStoppedLeading: func() {
                log.Info("Lost leadership, stopping consumer...")
            },
            OnNewLeader: func(identity string) {
                if identity != podName {
                    log.Info("New leader elected", "leader", identity)
                }
            },
        },
    }
    
    leaderelection.RunOrDie(ctx, lec)
}

// cmd/consumer/main.go
func main() {
    config := loadConfig()
    
    // 初始化 K8s client（用于 Leader Election）
    var kubeClient kubernetes.Interface
    if config.Deployment.LeaderElection.Enabled {
        kubeConfig, err := rest.InClusterConfig()
        if err != nil {
            log.Fatal("Failed to get in-cluster config", "error", err)
        }
        kubeClient, err = kubernetes.NewForConfig(kubeConfig)
        if err != nil {
            log.Fatal("Failed to create kube client", "error", err)
        }
    }
    
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    if config.Deployment.LeaderElection.Enabled {
        // Leader Election 模式
        leaderCfg := leader.Config{
            LeaseName:      config.Deployment.LeaderElection.LeaseName,
            LeaseNamespace: config.Deployment.LeaderElection.LeaseNamespace,
            LeaseDuration:  time.Duration(config.Deployment.LeaderElection.LeaseDurationSeconds) * time.Second,
            RenewDeadline:  time.Duration(config.Deployment.LeaderElection.RenewDeadlineSeconds) * time.Second,
            RetryPeriod:   2 * time.Second,
        }
        leader.RunWithLeaderElection(ctx, leaderCfg, kubeClient, func(ctx context.Context) {
            runConsumer(ctx, config)
        })
    } else {
        // 直接运行（单实例或测试）
        runConsumer(ctx, config)
    }
}
```

**2. Redis 分布式锁实现（不依赖 K8s，通用方案）**

```go
// internal/leader/redis_lock.go
package leader

import (
    "context"
    "fmt"
    "os"
    "time"
    
    "github.com/go-redis/redis/v8"
)

type RedisLockConfig struct {
    RedisAddr     string
    RedisPassword string
    LockKey       string
    LockTTL       time.Duration  // 锁的过期时间
    RenewInterval time.Duration  // 续期间隔（应该 < LockTTL/2）
}

type RedisLock struct {
    client        *redis.Client
    config        RedisLockConfig
    podID         string
    isLeader      bool
    stopRenew     chan struct{}
}

func NewRedisLock(config RedisLockConfig) *RedisLock {
    podID := os.Getenv("POD_NAME")
    if podID == "" {
        podID = os.Getenv("HOSTNAME")
    }
    if podID == "" {
        podID = fmt.Sprintf("pod-%d", os.Getpid())
    }
    
    rdb := redis.NewClient(&redis.Options{
        Addr:     config.RedisAddr,
        Password: config.RedisPassword,
        DB:       0,
    })
    
    return &RedisLock{
        client:    rdb,
        config:    config,
        podID:     podID,
        isLeader:  false,
        stopRenew: make(chan struct{}),
    }
}

// TryAcquireLock 尝试获取锁
func (rl *RedisLock) TryAcquireLock(ctx context.Context) (bool, error) {
    // SET key value NX EX ttl
    // NX: 只在 key 不存在时设置
    // EX: 设置过期时间（秒）
    result, err := rl.client.SetNX(ctx, rl.config.LockKey, rl.podID, rl.config.LockTTL).Result()
    if err != nil {
        return false, err
    }
    
    if result {
        rl.isLeader = true
        // 启动续期 goroutine
        go rl.renewLock(ctx)
        return true, nil
    }
    
    return false, nil
}

// renewLock 定期续期锁
func (rl *RedisLock) renewLock(ctx context.Context) {
    ticker := time.NewTicker(rl.config.RenewInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            // 检查是否还是 Leader（通过 GET 验证 value 是否还是自己的 podID）
            currentOwner, err := rl.client.Get(ctx, rl.config.LockKey).Result()
            if err == redis.Nil {
                // 锁已过期，不再是 Leader
                rl.isLeader = false
                return
            }
            if err != nil {
                log.Error("Failed to check lock ownership", "error", err)
                continue
            }
            
            if currentOwner != rl.podID {
                // 锁已被其他 Pod 获取
                rl.isLeader = false
                return
            }
            
            // 续期锁
            err = rl.client.Expire(ctx, rl.config.LockKey, rl.config.LockTTL).Err()
            if err != nil {
                log.Error("Failed to renew lock", "error", err)
                rl.isLeader = false
                return
            }
            
        case <-rl.stopRenew:
            return
        case <-ctx.Done():
            return
        }
    }
}

// ReleaseLock 释放锁
func (rl *RedisLock) ReleaseLock(ctx context.Context) error {
    close(rl.stopRenew)
    
    // 只有锁的拥有者才能释放
    currentOwner, err := rl.client.Get(ctx, rl.config.LockKey).Result()
    if err == redis.Nil {
        return nil  // 锁已不存在
    }
    if err != nil {
        return err
    }
    
    if currentOwner == rl.podID {
        return rl.client.Del(ctx, rl.config.LockKey).Err()
    }
    
    return nil
}

// RunWithRedisLock 使用 Redis 锁运行函数
func RunWithRedisLock(ctx context.Context, config RedisLockConfig, runFunc func(context.Context)) {
    lock := NewRedisLock(config)
    defer lock.ReleaseLock(ctx)
    
    // 定期尝试获取锁
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            acquired, err := lock.TryAcquireLock(ctx)
            if err != nil {
                log.Error("Failed to acquire lock", "error", err)
                continue
            }
            
            if acquired {
                log.Info("Acquired lock, becoming leader", "pod", lock.podID)
                // 运行实际的 consumer 逻辑
                runFunc(ctx)
                return
            }
        }
    }
}

// cmd/consumer/main.go
func main() {
    config := loadConfig()
    
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    if config.Deployment.LeaderElection.Enabled && config.Deployment.LeaderElection.Type == "redis" {
        // Redis 分布式锁模式（不依赖 K8s）
        redisConfig := leader.RedisLockConfig{
            RedisAddr:     config.Deployment.LeaderElection.RedisAddr,
            RedisPassword: config.Deployment.LeaderElection.RedisPassword,
            LockKey:       "tetragon-consumer-leader",
            LockTTL:       15 * time.Second,
            RenewInterval: 5 * time.Second,
        }
        leader.RunWithRedisLock(ctx, redisConfig, func(ctx context.Context) {
            runConsumer(ctx, config)
        })
    } else if config.Deployment.LeaderElection.Enabled && config.Deployment.LeaderElection.Type == "k8s" {
        // K8s Leader Election 模式
        // ... (之前的 K8s 代码)
    } else {
        // 直接运行（单实例或测试）
        runConsumer(ctx, config)
    }
}
```

**3. etcd 分布式锁实现（不依赖 K8s，通用方案）**

```go
// internal/leader/etcd_lock.go
package leader

import (
    "context"
    "time"
    
    "go.etcd.io/etcd/clientv3"
    "go.etcd.io/etcd/clientv3/concurrency"
)

func RunWithEtcdLock(ctx context.Context, etcdEndpoints []string, runFunc func(context.Context)) {
    cli, err := clientv3.New(clientv3.Config{
        Endpoints:   etcdEndpoints,
        DialTimeout: 5 * time.Second,
    })
    if err != nil {
        log.Fatal("Failed to connect to etcd", "error", err)
    }
    defer cli.Close()
    
    // 创建 session（带 TTL）
    session, err := concurrency.NewSession(cli, concurrency.WithTTL(10))
    if err != nil {
        log.Fatal("Failed to create etcd session", "error", err)
    }
    defer session.Close()
    
    // 创建 mutex
    mutex := concurrency.NewMutex(session, "/tetragon-consumer-leader")
    
    // 尝试获取锁
    err = mutex.Lock(ctx)
    if err != nil {
        log.Fatal("Failed to acquire lock", "error", err)
    }
    defer mutex.Unlock(ctx)
    
    log.Info("Acquired lock, becoming leader")
    // 运行实际的 consumer 逻辑
    runFunc(ctx)
}
```

**4. 消息去重键生成（方案 3：Kafka 端去重）**

```go
// internal/kafka/dedup.go
func GenerateDedupKey(event *tetragon.GetEventsResponse) string {
    h := sha256.New()
    h.Write([]byte(event.NodeName))
    h.Write([]byte(event.Type))
    if event.ProcessExec != nil {
        h.Write([]byte(fmt.Sprintf("%d:%d", 
            event.ProcessExec.Process.Pid,
            event.ProcessExec.Process.StartTime)))
    }
    return hex.EncodeToString(h.Sum(nil))[:16]
}
```

#### 10.0.5 配置示例（分布式 Deployment）

> **⭐ 推荐配置：Kafka Compacted Topic（方案 1）**

```yaml
# ⭐ 方案 1：Kafka Compacted Topic（推荐配置）
kafka:
  brokers: ["kafka-0:9092", "kafka-1:9092"]
  client_id: "tetragon-consumer"
  producer:
    enable_idempotence: true  # 启用幂等性（防止网络重试重复）
    acks: "all"               # 确保消息持久化
    compression: "snappy"
  topic_admin:
    auto_create: true
    cleanup_policy: "compact"  # ⭐ 关键：启用 log compaction，自动去重
    min_cleanable_dirty_ratio: 0.5
    retention_ms: 604800000  # 7 days
    partitions: 24
    replication_factor: 3

routing:
  partition_key:
    mode: "deduplication"  # ⭐ 关键：用于去重的 key 模式
    fields: ["node", "type", "process.pid", "timestamp"]  # 生成唯一 key
    separator: ":"

# 其他配置保持不变
tetragon:
  grpc_addr: "tetragon.kube-system.svc:54321"
  stream:
    max_queue: 50000
    drop_if_queue_full: true

# 方案 2：Kafka 事务性 Producer
kafka:
  producer:
    transactional_id: "tetragon-consumer-${POD_NAME}"  # 每个 Pod 唯一
    enable_idempotence: true
    acks: "all"
    max_in_flight_requests_per_connection: 1

# 方案 3：K8s Leader Election 模式（K8s 环境）
deployment:
  mode: "deployment"
  leader_election:
    enabled: true
    type: "k8s"
    lease_name: "tetragon-consumer-leader"
    lease_namespace: "kube-system"
    lease_duration_seconds: 15
    renew_deadline_seconds: 10

# 方案 4：Redis 分布式锁模式（不依赖 K8s）
deployment:
  mode: "deployment"
  leader_election:
    enabled: true
    type: "redis"
    redis_addr: "redis:6379"
    redis_password: ""
    lock_key: "tetragon-consumer-leader"
    lock_ttl_seconds: 15
    renew_interval_seconds: 5

# 方案 5：Kafka 端去重（下游去重，备选）
deployment:
  mode: "deployment"
  deduplication:
    enabled: true
    key_fields: ["node", "type", "process.pid", "timestamp"]
    window_seconds: 3600
```

#### 10.0.7 K8s RBAC 配置（Leader Election 必需）

```yaml
# ServiceAccount
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tetragon-consumer
  namespace: kube-system

---
# Role（Leader Election 需要）
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: tetragon-consumer-leader-election
  namespace: kube-system
rules:
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]

---
# RoleBinding
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: tetragon-consumer-leader-election
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: tetragon-consumer-leader-election
subjects:
- kind: ServiceAccount
  name: tetragon-consumer
  namespace: kube-system
```

#### 10.0.8 Deployment 配置示例（Leader Election）

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: tetragon-kafka-consumer
  namespace: kube-system
spec:
  replicas: 3  # 多副本，Leader Election 保证只有一个在工作
  selector:
    matchLabels:
      app: tetragon-kafka-consumer
  template:
    metadata:
      labels:
        app: tetragon-kafka-consumer
    spec:
      serviceAccountName: tetragon-consumer  # 使用上面创建的 SA
      containers:
      - name: consumer
        image: your-registry/tetragon-kafka-consumer:latest
        env:
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: TETRAGON_GRPC_ADDR
          value: "tetragon.kube-system.svc:54321"  # 集中式 Tetragon
        - name: DEPLOYMENT_MODE
          value: "deployment"
        - name: LEADER_ELECTION_ENABLED
          value: "true"
        - name: LEADER_ELECTION_LEASE_NAME
          value: "tetragon-consumer-leader"
        - name: LEADER_ELECTION_LEASE_NAMESPACE
          value: "kube-system"
        resources:
          requests:
            cpu: "500m"
            memory: "512Mi"
          limits:
            cpu: "2000m"
            memory: "2Gi"
```

#### 10.0.6 推荐选择（分布式 Deployment）

> **⭐ 强烈推荐：方案 1 - Kafka Compacted Topic + 消息 Key**

| 场景 | 推荐方案 | 理由 |
|------|---------|------|
| **⭐ 生产环境（首选）** | **方案 1：Kafka Compacted Topic** | **利用 Kafka 自身机制、不依赖外部、自动去重、真正分布式、最简单** |
| **需要 Exactly-Once** | 方案 2：Kafka 事务性 Producer | Kafka 保证 exactly-once 语义 |
| **无法使用 Compacted Topic** | 方案 3：K8s Leader Election | 标准 K8s 机制、无重复 |
| **无法使用 Compacted Topic，非 K8s** | 方案 4：Redis/etcd 分布式锁 | 通用方案，不依赖 K8s |
| **下游已有去重能力** | 方案 5：下游去重 | 零切换中断、真正分布式 |

> **💡 决策建议**：
> 
> 1. **首选**：**方案 1 - Kafka Compacted Topic + 消息 Key**
>    - ✅ 最简单：只需配置 Topic 为 Compacted，使用消息 Key
>    - ✅ 最可靠：利用 Kafka 自身机制，不依赖任何外部服务
>    - ✅ 最高可用：所有 Pod 都在工作，无单点故障
>    - ✅ 自动去重：Kafka 自动处理，无需额外逻辑
> 
> 2. **备选**：只有在以下情况才考虑其他方案
>    - Kafka 版本不支持 Compacted Topic（Kafka 0.8.1+ 支持）
>    - 业务要求实时去重（Compacted Topic 有延迟）
>    - 需要 exactly-once 语义（使用方案 2）

### 10.1 部署模式选择（快速参考）

- 推荐以 **Deployment** 运行 consumer（可水平扩展）
- 如果你要多副本：
  - 需要明确每个副本是否都订阅同一个 Tetragon（会重复）
  - 常见做法：每个 node 一个 consumer（DaemonSet），或在 consumer 内支持“仅订阅本节点 tetragon”
- 最简单稳定：**consumer 与 tetragon 同节点（DaemonSet）**，写 Kafka。

---

## 11. 下一步（如果你要我继续给“成品工程”）
我可以在这份设计基础上继续补齐：
1. **完整可编译的 repo（包含 protobuf 生成脚本、Dockerfile、Helm values）**
2. **稳定 JSON schema v1 的完整字段抽取（process/network/file/lsm）**
3. **Kafka topic admin 自动创建实现（带幂等）**
4. **Prometheus metrics + pprof 性能剖析开关**

你只需要告诉我：
- 你们 Kafka 是否启用 TLS/SASL？
- 你最想先支持哪些事件：exec / connect / file / lsm？
- 你们使用哪个 Kafka 客户端库：Sarama 还是 Confluent？

---

## 附录 A：完整配置参考

### A.1 完整 YAML 配置示例

```yaml
# Tetragon gRPC 连接配置
tetragon:
  grpc_addr: "tetragon.kube-system.svc:54321"
  tls:
    enabled: false
    ca_cert: "/etc/tetragon/ca.crt"
    client_cert: "/etc/tetragon/client.crt"
    client_key: "/etc/tetragon/client.key"
  stream:
    max_queue: 50000
    drop_if_queue_full: true
    sample_ratio: 1.0
    reconnect:
      initial_backoff_seconds: 1
      max_backoff_seconds: 30
      jitter: true

# Kafka 配置
kafka:
  brokers: ["kafka-0.kafka:9092","kafka-1.kafka:9092"]
  client_id: "tetragon-consumer"
  acks: "all"  # all / 1 / 0
  compression: "snappy"  # none / gzip / snappy / lz4 / zstd
  max_message_bytes: 1048576  # 1MB
  batch:
    max_messages: 3000
    max_bytes: 1048576
    flush_interval_ms: 100
  writer_workers: 12
  tls:
    enabled: false
    ca_cert: "/etc/kafka/ca.crt"
    client_cert: "/etc/kafka/client.crt"
    client_key: "/etc/kafka/client.key"
  sasl:
    enabled: false
    mechanism: "PLAIN"  # PLAIN / SCRAM-SHA-256 / SCRAM-SHA-512
    username: "tetragon-consumer"
    password_file: "/etc/kafka/password"
  topic_admin:
    auto_create: true
    partitions: 24
    replication_factor: 3
    retention_ms: 604800000  # 7 days

# 路由配置
routing:
  topics:
    process_exec: "tetragon.process.exec"
    process_exit: "tetragon.process.exit"
    process_lsm: "tetragon.security.lsm"
    process_kprobe: "tetragon.syscall.kprobe"
    process_tracepoint: "tetragon.kernel.tracepoint"
    process_connect: "tetragon.network.connect"
    process_dns: "tetragon.network.dns"
    unknown: "tetragon.unknown"
    dlq: "tetragon.dlq"
  partition_key:
    mode: "fields_concat"  # fields_concat / hash / random
    fields: ["k8s.namespace","k8s.pod","process.binary"]
    separator: "|"

# Schema 配置
schema:
  version: 1
  mode: "stable_json"  # stable_json / raw_string_fallback
  include_raw: false  # 是否在 JSON 中包含原始 protobuf bytes

# 日志配置
logger:
  level: "info"  # debug / info / warn / error
  format: "json"  # json / text
  output: "stdout"  # stdout / file
  file:
    path: "/var/log/consumer.log"
    max_size_mb: 100
    max_backups: 5
    max_age_days: 7

# 监控配置
monitoring:
  enabled: true
  health_port: 8080
  metrics_port: 9090
  pprof_enabled: false
  pprof_port: 6060
```

### A.2 环境变量映射表

| 配置路径 | 环境变量 | 示例值 |
|---------|---------|--------|
| `tetragon.grpc_addr` | `TETRAGON_GRPC_ADDR` | `tetragon.kube-system.svc:54321` |
| `tetragon.tls.enabled` | `TETRAGON_TLS_ENABLED` | `true` |
| `kafka.brokers` | `KAFKA_BROKERS` | `kafka-0:9092,kafka-1:9092` |
| `kafka.client_id` | `KAFKA_CLIENT_ID` | `tetragon-consumer` |
| `kafka.acks` | `KAFKA_ACKS` | `all` |
| `kafka.compression` | `KAFKA_COMPRESSION` | `snappy` |
| `kafka.writer_workers` | `KAFKA_WRITER_WORKERS` | `12` |
| `stream.max_queue` | `STREAM_MAX_QUEUE` | `50000` |
| `stream.drop_if_queue_full` | `STREAM_DROP_IF_QUEUE_FULL` | `true` |
| `logger.level` | `LOG_LEVEL` | `info` |

---

## 附录 B：性能基准参考

### B.1 典型性能指标（参考值）

| 场景 | QPS | 延迟 (P99) | CPU | 内存 |
|------|-----|-----------|-----|------|
| 低负载 | 1k/s | < 50ms | 200m | 256Mi |
| 中负载 | 10k/s | < 100ms | 500m | 512Mi |
| 高负载 | 50k/s | < 200ms | 1000m | 1Gi |
| 极高负载 | 100k/s+ | < 500ms | 2000m | 2Gi+ |

**注意**：实际性能取决于事件类型、Kafka 配置、网络条件等因素。

### B.2 调优建议

- **CPU 瓶颈**：增加 `writer_workers`、启用压缩、优化 JSON 序列化
- **内存瓶颈**：降低 `max_queue`、启用采样、限制消息大小
- **网络瓶颈**：增加 Kafka 分区数、优化 batch 大小、使用压缩
- **Kafka 瓶颈**：增加分区数、调整 ACK 策略、优化 broker 配置

---

## 附录 C：版本兼容性

### C.1 Tetragon 版本支持
- **最低版本**：Tetragon v1.0+
- **推荐版本**：Tetragon v1.9+（支持最新事件类型）
- **API 兼容性**：基于 `github.com/cilium/tetragon/api` protobuf 定义

### C.2 Go 版本要求
- **最低版本**：Go 1.21
- **推荐版本**：Go 1.22+

### C.3 Kafka 版本支持
- **Sarama 客户端**：Kafka 0.8.2+
- **Confluent 客户端**：Kafka 0.9.0+
- **推荐**：Kafka 2.8+（支持更好的性能特性）

---

## 附录 D：常见问题 FAQ

**Q: 为什么选择 gRPC 而不是直接读取文件？**
A: gRPC 流式订阅实时性更好，不依赖文件系统，适合容器化部署。

**Q: 多副本部署会导致事件重复吗？**
A: 如果所有副本订阅同一个 Tetragon，会重复。建议使用 DaemonSet 模式，每个节点一个 consumer。

**Q: 如何保证事件不丢失？**
A: 使用 `acks=all`、启用 DLQ、监控 drop 指标。但高频 syscall 事件建议采样，避免存储成本过高。

**Q: 支持配置热重载吗？**
A: 当前设计不支持，需要重启。如需热重载，可扩展支持 SIGHUP 信号或 HTTP API。

**Q: 如何调试性能问题？**
A: 启用 pprof、查看 Prometheus 指标、分析队列水位和 Kafka 写入延迟。

---

## 附录 E：相关资源

- [Tetragon 官方文档](https://github.com/cilium/tetragon)
- [Tetragon API 参考](https://github.com/cilium/tetragon/tree/main/api)
- [Sarama Kafka 客户端](https://github.com/IBM/sarama)
- [Confluent Kafka Go 客户端](https://github.com/confluentinc/confluent-kafka-go)
- [Prometheus 指标最佳实践](https://prometheus.io/docs/practices/naming/)
