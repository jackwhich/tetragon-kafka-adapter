package metrics

import (
	"runtime"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// 吞吐量指标
	EventsInTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "events_in_total",
			Help: "Total events received by type",
		},
		[]string{"type"},
	)

	EventsOutTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "events_out_total",
			Help: "Total events written to Kafka by topic and status",
		},
		[]string{"topic", "status"},
	)

	GrpcEventsReceivedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "grpc_events_received_total",
			Help: "Total events received from gRPC stream",
		},
	)

	// 队列指标
	QueueDepth = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "queue_depth",
			Help: "Current queue depth",
		},
	)

	QueueCapacity = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "queue_capacity",
			Help: "Queue capacity",
		},
	)

	// 丢弃与采样指标
	DropsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "drops_total",
			Help: "Total dropped events by reason",
		},
		[]string{"reason"},
	)

	SampledTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sampled_total",
			Help: "Total sampled events by type",
		},
		[]string{"type"},
	)

	// 延迟指标
	KafkaWriteLatencyMs = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kafka_write_latency_ms",
			Help:    "Kafka write latency in milliseconds",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
		},
		[]string{"topic"},
	)

	NormalizeLatencyMs = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "normalize_latency_ms",
			Help:    "Normalization latency in milliseconds",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 25, 50},
		},
		[]string{"type"},
	)

	// Kafka 写入指标
	KafkaWriteBytesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kafka_write_bytes_total",
			Help: "Total bytes written to Kafka by topic",
		},
		[]string{"topic"},
	)

	KafkaWriteMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kafka_write_messages_total",
			Help: "Total messages written to Kafka by topic",
		},
		[]string{"topic"},
	)

	KafkaBatchSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kafka_batch_size",
			Help:    "Kafka batch size distribution",
			Buckets: []float64{1, 10, 50, 100, 500, 1000, 5000},
		},
		[]string{"topic"},
	)

	// DLQ 指标
	DLQEventsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dlq_events_total",
			Help: "Total events written to DLQ by reason",
		},
		[]string{"reason"},
	)

	DLQEventsBytesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "dlq_events_bytes_total",
			Help: "Total bytes written to DLQ",
		},
	)

	// 错误指标
	KafkaErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kafka_errors_total",
			Help: "Total Kafka errors by error type and topic",
		},
		[]string{"error_type", "topic"},
	)

	NormalizeErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "normalize_errors_total",
			Help: "Total normalization errors by event type",
		},
		[]string{"event_type"},
	)

	GrpcErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "grpc_errors_total",
			Help: "Total gRPC errors by error type",
		},
		[]string{"error_type"},
	)

	// 连接状态指标
	GrpcReconnectTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "grpc_reconnect_total",
			Help: "Total gRPC reconnection attempts",
		},
	)

	GrpcStreamUptimeSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "grpc_stream_uptime_seconds",
			Help: "gRPC stream uptime in seconds",
		},
	)

	KafkaProducerConnected = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "kafka_producer_connected",
			Help: "Kafka producer connection status (1=connected, 0=disconnected)",
		},
	)

	// Topic 管理指标
	KafkaTopicCreateTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kafka_topic_create_total",
			Help: "Total topic creation attempts by status",
		},
		[]string{"status"},
	)

	KafkaTopicCreateDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "kafka_topic_create_duration_seconds",
			Help:    "Topic creation duration in seconds",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10},
		},
	)

	// 新增：队列处理延迟指标
	QueueProcessingLatencyMs = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "queue_processing_latency_ms",
			Help:    "Time events spend in queue before processing (milliseconds)",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
		},
	)

	// 新增：gRPC 连接状态指标
	GrpcConnectionStatus = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "grpc_connection_status",
			Help: "gRPC connection status (1=connected, 0=disconnected)",
		},
	)

	// 新增：Kafka Writer 队列深度指标（所有 worker 共享同一个队列，使用 Gauge 而非 GaugeVec）
	KafkaWriterQueueDepth = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "kafka_writer_queue_depth",
			Help: "Current depth of Kafka writer message queue",
		},
	)

	// 新增：事件处理各阶段延迟指标
	EventProcessingLatencyMs = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "event_processing_latency_ms",
			Help:    "Total event processing latency from receive to write (milliseconds)",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000},
		},
		[]string{"stage"}, // stage: normalize, serialize, route, total
	)

	// 新增：重连频率指标（最近 N 次重连的平均间隔）
	GrpcReconnectIntervalSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "grpc_reconnect_interval_seconds",
			Help:    "Time between gRPC reconnection attempts (seconds)",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
		},
	)

	// 新增：批次错误率指标
	KafkaBatchErrorRate = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "kafka_batch_error_rate",
			Help: "Error rate in batch processing (0.0-1.0)",
		},
		[]string{"topic"},
	)

	// 内存监控指标
	MemoryAllocBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "memory_alloc_bytes",
			Help: "Number of bytes allocated and still in use",
		},
	)

	MemoryTotalAllocBytes = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "memory_total_alloc_bytes_total",
			Help: "Total number of bytes allocated (even if freed)",
		},
	)

	MemorySysBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "memory_sys_bytes",
			Help: "Number of bytes obtained from system",
		},
	)

	MemoryNumGC = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "memory_num_gc_total",
			Help: "Total number of GC cycles",
		},
	)

	MemoryHeapAllocBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "memory_heap_alloc_bytes",
			Help: "Number of heap bytes allocated and still in use",
		},
	)

	MemoryHeapSysBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "memory_heap_sys_bytes",
			Help: "Number of heap bytes obtained from system",
		},
	)

	MemoryHeapInuseBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "memory_heap_inuse_bytes",
			Help: "Number of heap bytes in use",
		},
	)

	MemoryHeapIdleBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "memory_heap_idle_bytes",
			Help: "Number of heap bytes idle",
		},
	)

	NumGoroutines = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "num_goroutines",
			Help: "Number of goroutines that currently exist",
		},
	)
)

var (
	// 用于跟踪上次的内存统计值，以便计算增量
	lastMemStats     runtime.MemStats
	lastMemStatsOnce sync.Once
	lastMemStatsMu   sync.Mutex
)

// UpdateMemoryMetrics 更新内存指标（应该定期调用，例如每 10 秒）
// 注意：TotalAlloc 和 NumGC 是累计值，需要计算增量
func UpdateMemoryMetrics() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	MemoryAllocBytes.Set(float64(m.Alloc))
	MemorySysBytes.Set(float64(m.Sys))
	MemoryHeapAllocBytes.Set(float64(m.HeapAlloc))
	MemoryHeapSysBytes.Set(float64(m.HeapSys))
	MemoryHeapInuseBytes.Set(float64(m.HeapInuse))
	MemoryHeapIdleBytes.Set(float64(m.HeapIdle))
	NumGoroutines.Set(float64(runtime.NumGoroutine()))

	// 对于 Counter 类型的指标，需要计算增量
	lastMemStatsMu.Lock()
	defer lastMemStatsMu.Unlock()

	// 初始化 lastMemStats（第一次调用时）
	lastMemStatsOnce.Do(func() {
		lastMemStats = m
		// 第一次调用时，直接设置初始值（不增加，因为这是累计值）
		// 注意：Counter 从 0 开始，所以第一次不需要 Add
		return
	})

	// 计算增量并更新 Counter
	if m.TotalAlloc > lastMemStats.TotalAlloc {
		MemoryTotalAllocBytes.Add(float64(m.TotalAlloc - lastMemStats.TotalAlloc))
	}
	if m.NumGC > lastMemStats.NumGC {
		MemoryNumGC.Add(float64(m.NumGC - lastMemStats.NumGC))
	}

	// 更新上次的值
	lastMemStats = m
}
