# Logstash 配置文件说明

## 配置文件列表

### 1. 应用日志配置
- **logstash-tetragon-kafka-adapter.conf** - Tetragon Kafka Adapter 应用日志
  - Topic: `tetragon-kafka-adapter-logs`
  - Consumer Group: `logstash-k8s-tetragon-kafka-adapter-logs`
  - Index: `tetragon-kafka-adapter-logs-YYYY.MM.dd`

### 2. Tetragon 事件配置（每个事件类型独立配置）

- **logstash-tetragon-process-exec.conf** - 进程执行事件
  - Topic: `tetragon.process.exec`
  - Consumer Group: `logstash-k8s-tetragon-process-exec`
  - Index: `tetragon-process-exec-YYYY.MM.dd`

- **logstash-tetragon-process-exit.conf** - 进程退出事件
  - Topic: `tetragon.process.exit`
  - Consumer Group: `logstash-k8s-tetragon-process-exit`
  - Index: `tetragon-process-exit-YYYY.MM.dd`

- **logstash-tetragon-security-lsm.conf** - 安全 LSM 事件
  - Topic: `tetragon.security.lsm`
  - Consumer Group: `logstash-k8s-tetragon-security-lsm`
  - Index: `tetragon-security-lsm-YYYY.MM.dd`

- **logstash-tetragon-syscall-kprobe.conf** - 系统调用 kprobe 事件
  - Topic: `tetragon.syscall.kprobe`
  - Consumer Group: `logstash-k8s-tetragon-syscall-kprobe`
  - Index: `tetragon-syscall-kprobe-YYYY.MM.dd`

- **logstash-tetragon-kernel-tracepoint.conf** - 内核 tracepoint 事件
  - Topic: `tetragon.kernel.tracepoint`
  - Consumer Group: `logstash-k8s-tetragon-kernel-tracepoint`
  - Index: `tetragon-kernel-tracepoint-YYYY.MM.dd`

- **logstash-tetragon-unknown.conf** - 未知类型事件
  - Topic: `tetragon.unknown`
  - Consumer Group: `logstash-k8s-tetragon-unknown`
  - Index: `tetragon-unknown-YYYY.MM.dd`

- **logstash-tetragon-dlq.conf** - 死信队列事件
  - Topic: `tetragon.dlq`
  - Consumer Group: `logstash-k8s-tetragon-dlq`
  - Index: `tetragon-dlq-YYYY.MM.dd`

## 部署方式

### 方式一：每个配置文件独立运行（推荐）

每个配置文件使用独立的 Logstash 进程，便于管理和监控：

```bash
# 启动应用日志收集
logstash -f logstash-tetragon-kafka-adapter.conf

# 启动各个事件类型收集（可以并行运行）
logstash -f logstash-tetragon-process-exec.conf &
logstash -f logstash-tetragon-process-exit.conf &
logstash -f logstash-tetragon-security-lsm.conf &
logstash -f logstash-tetragon-syscall-kprobe.conf &
logstash -f logstash-tetragon-kernel-tracepoint.conf &
logstash -f logstash-tetragon-unknown.conf &
logstash -f logstash-tetragon-dlq.conf &
```

### 方式二：使用 Logstash 管道配置

在 `logstash.yml` 中配置多个管道：

```yaml
pipelines:
  - pipeline.id: tetragon-kafka-adapter-logs
    path.config: "/etc/logstash/conf.d/logstash-tetragon-kafka-adapter.conf"
  
  - pipeline.id: tetragon-process-exec
    path.config: "/etc/logstash/conf.d/logstash-tetragon-process-exec.conf"
  
  - pipeline.id: tetragon-process-exit
    path.config: "/etc/logstash/conf.d/logstash-tetragon-process-exit.conf"
  
  - pipeline.id: tetragon-security-lsm
    path.config: "/etc/logstash/conf.d/logstash-tetragon-security-lsm.conf"
  
  - pipeline.id: tetragon-syscall-kprobe
    path.config: "/etc/logstash/conf.d/logstash-tetragon-syscall-kprobe.conf"
  
  - pipeline.id: tetragon-kernel-tracepoint
    path.config: "/etc/logstash/conf.d/logstash-tetragon-kernel-tracepoint.conf"
  
  - pipeline.id: tetragon-unknown
    path.config: "/etc/logstash/conf.d/logstash-tetragon-unknown.conf"
  
  - pipeline.id: tetragon-dlq
    path.config: "/etc/logstash/conf.d/logstash-tetragon-dlq.conf"
```

## 配置说明

### 共同特点

1. **独立消费者组**：每个配置文件使用独立的 Kafka 消费者组，避免消费冲突
2. **JSON 格式**：所有日志和事件都是 JSON 格式，使用 `codec => json` 直接解析
3. **时间戳处理**：支持 `timestamp` 和 `ts` 字段，自动转换为 `@timestamp`
4. **索引日期**：自动生成 `index_date` 字段（格式：YYYY.MM.dd，UTC+8）
5. **Elasticsearch 输出**：统一输出到 Elasticsearch 集群

### 配置参数

- **bootstrap_servers**: Kafka broker 地址
- **auto_offset_reset**: `latest` - 从最新消息开始消费
- **consumer_threads**: 可根据实际负载调整（默认注释，使用默认值）

### 索引命名规则

- 应用日志：`tetragon-kafka-adapter-logs-YYYY.MM.dd`
- 事件类型：`tetragon-{event-type}-YYYY.MM.dd`

## 监控建议

1. **消费者组监控**：监控每个消费者组的 lag，确保消费正常
2. **索引大小监控**：监控各个索引的大小和文档数量
3. **错误日志监控**：监控 Logstash 的错误日志，及时发现解析问题

## 注意事项

1. 确保 Kafka topics 已创建
2. 确保 Elasticsearch 集群可访问
3. 根据实际数据量调整 `consumer_threads` 参数
4. 定期清理旧索引，避免磁盘空间不足
