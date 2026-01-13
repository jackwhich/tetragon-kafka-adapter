package kafka

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"github.com/yourorg/tetragon-kafka-adapter/internal/router"
	"go.uber.org/zap"
)

// Message 待写入的消息
type Message struct {
	Event       *tetragon.GetEventsResponse
	EventType   string // 事件类型（P1 修复：用于 Headers）
	ContentType string // P1 修复：内容类型（application/json 或 application/protobuf）
	Topic       string
	Key         string
	Value       []byte
	TraceID     string // 追踪 ID，用于日志追踪
}

// Writer Kafka 批量写入 Worker
type Writer struct {
	producer   *Producer
	asyncProducer sarama.AsyncProducer // P0 修复：直接访问 AsyncProducer 以优化批量发送
	router     *router.Router
	config     *config.KafkaConfig
	logger     *zap.Logger
	workers    int
	messageCh  chan *Message
	wg         sync.WaitGroup
	closeOnce  sync.Once // 确保 Close() 只执行一次
}

// NewWriter 创建新的 Writer
func NewWriter(producer *Producer, r *router.Router, cfg *config.KafkaConfig, logger *zap.Logger) *Writer {
	return &Writer{
		producer:      producer,
		asyncProducer: producer.GetAsyncProducer(), // P0 修复：直接访问 AsyncProducer 以优化批量发送
		router:        r,
		config:        cfg,
		logger:        logger,
		workers:       cfg.WriterWorkers,
		messageCh:     make(chan *Message, cfg.Batch.MaxMessages*cfg.WriterWorkers),
	}
}

// Start 启动 Writer workers
func (w *Writer) Start(ctx context.Context) {
	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go w.worker(ctx, i)
	}
}

// Write 写入消息
func (w *Writer) Write(msg *Message) error {
	select {
	case w.messageCh <- msg:
		return nil
	default:
		metrics.DropsTotal.WithLabelValues("writer_queue_full").Inc()
		return ErrQueueFull
	}
	// 注意：如果 channel 已关闭，select 会立即返回 default case
	// 这由调用者确保在 Close() 后不再调用 Write()
}

// worker 批量写入 worker
func (w *Writer) worker(ctx context.Context, id int) {
	defer w.wg.Done()
	
	w.logger.Info("Kafka Writer Worker 已启动",
		zap.Int("Worker ID", id))

	ticker := time.NewTicker(time.Duration(w.config.Batch.FlushIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	
	// 优化：使用单独的 ticker 定期更新指标，减少锁竞争
	metricsTicker := time.NewTicker(100 * time.Millisecond)
	defer metricsTicker.Stop()

	// 优化：使用预分配的 batch slice，减少内存分配
	// 注意：batch 会在每次 flush 后重置为 batch[:0]，底层数组会保留
	// 这样可以减少内存分配，但需要注意内存使用
	batch := make([]*Message, 0, w.config.Batch.MaxMessages)
	
	for {
		select {
		case <-ctx.Done():
			// 关闭前刷新剩余消息
			if len(batch) > 0 {
				w.logger.Info("Worker 关闭前刷新剩余批次",
					zap.Int("Worker ID", id),
					zap.Int("批次大小", len(batch)))
				w.flushBatch(ctx, batch)
			}
			w.logger.Info("Kafka Writer Worker 已停止",
				zap.Int("Worker ID", id))
			metrics.KafkaWriterQueueDepth.Set(0)
			return
			
		case msg, ok := <-w.messageCh:
			// 不再每次收到消息都更新指标，改为定期更新
			
			// 检查 channel 是否已关闭
			if !ok {
				// Channel 已关闭，刷新剩余批次并退出
				if len(batch) > 0 {
					w.logger.Info("Channel 已关闭，刷新剩余批次",
						zap.Int("Worker ID", id),
						zap.Int("批次大小", len(batch)))
					w.flushBatch(ctx, batch)
				}
				w.logger.Info("Kafka Writer Worker 已停止（channel 关闭）",
					zap.Int("Worker ID", id))
				metrics.KafkaWriterQueueDepth.Set(0)
				return
			}
			
			batch = append(batch, msg)
			
			// 达到批处理大小，立即刷新
			if len(batch) >= w.config.Batch.MaxMessages {
				w.flushBatch(ctx, batch)
				// 优化：重置 slice 但保留底层数组容量，减少内存分配
				batch = batch[:0]
			}
			
		case <-ticker.C:
			// 定时刷新
			if len(batch) > 0 {
				w.flushBatch(ctx, batch)
				// 优化：重置 slice 但保留底层数组容量，减少内存分配
				// 如果 batch 增长过大（超过 MaxMessages 的 2 倍），重新分配以释放内存
				if cap(batch) > w.config.Batch.MaxMessages*2 {
					batch = make([]*Message, 0, w.config.Batch.MaxMessages)
				} else {
					batch = batch[:0]
				}
			}
			
		case <-metricsTicker.C:
			// 定期更新队列深度指标（优化：减少锁竞争）
			metrics.KafkaWriterQueueDepth.Set(float64(len(w.messageCh)))
		}
	}
}

// flushBatch 刷新批次
func (w *Writer) flushBatch(ctx context.Context, batch []*Message) {
	if len(batch) == 0 {
		return
	}

	start := time.Now()
	
	// 性能优化：使用传入的 context 创建超时 context，支持优雅关闭
	flushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	
	topic := batch[0].Topic
	
	// P0 修复：批量发送优化 - 将所有消息发送到 AsyncProducer 的 Input channel
	// AsyncProducer 会自动批量处理这些消息，提高吞吐量
	sentMessages := make(map[string]*Message, len(batch)) // 用于跟踪已发送的消息
	
	for _, msg := range batch {
		// 创建 ProducerMessage
		var msgTimestamp time.Time
		if msg.Event != nil && msg.Event.GetTime() != nil {
			msgTimestamp = msg.Event.GetTime().AsTime()
		} else {
			msgTimestamp = time.Now()
		}
		
		// P1 修复：使用 Message 中的 ContentType
		contentType := msg.ContentType
		if contentType == "" {
			contentType = "application/json" // 默认值
		}
		
		headers := []sarama.RecordHeader{
			{Key: []byte("content-type"), Value: []byte(contentType)},
			{Key: []byte("schema-version"), Value: []byte("1")},
		}
		if msg.EventType != "" {
			headers = append(headers, sarama.RecordHeader{
				Key:   []byte("event-type"),
				Value: []byte(msg.EventType),
			})
		}
		
		producerMsg := &sarama.ProducerMessage{
			Topic:     msg.Topic,
			Key:       sarama.StringEncoder(msg.Key),
			Value:     sarama.ByteEncoder(msg.Value),
			Timestamp: msgTimestamp,
			Headers:   headers,
		}
		
		// 使用 traceID 作为唯一标识，用于后续错误处理
		msgKey := msg.TraceID
		if msgKey == "" {
			msgKey = fmt.Sprintf("%s:%s", msg.Topic, msg.Key)
		}
		sentMessages[msgKey] = msg
		
		// P0 修复：阻塞发送到 Input channel（等待缓冲区有空间，避免消息丢失）
		// 如果 channel 满了，会阻塞等待，直到有空间或 context 取消
		select {
		case w.asyncProducer.Input() <- producerMsg:
			// 消息已发送到缓冲区，AsyncProducer 会自动批量处理
		case <-flushCtx.Done():
			// Context 已取消，记录未发送的消息
			w.logger.Warn("批次发送被取消",
				zap.String("主题", topic),
				zap.Int("已发送", len(sentMessages)),
				zap.Int("批次大小", len(batch)))
			// 将未发送的消息写入 DLQ
			for _, remainingMsg := range batch[len(sentMessages):] {
				w.writeToDLQ(flushCtx, remainingMsg, "context cancelled")
			}
			return
		}
	}
	
	// P0 修复：由于 AsyncProducer 是异步批量处理的，我们无法在这里直接获取每个消息的结果
	// 错误和成功会通过 Producer 的 handleErrors 和 handleSuccesses 异步处理
	// 这里我们假设所有成功发送到 Input channel 的消息都会被处理
	// 实际的错误统计通过 metrics 和异步错误处理来完成
	successCount := len(sentMessages) // 成功发送到缓冲区的消息数
	errorCount := 0 // 错误通过异步处理统计
	failedTraceIDs := make([]string, 0, 5)

	latency := time.Since(start).Milliseconds()
	// 安全处理：确保批次不为空
	if len(batch) > 0 {
		metrics.KafkaWriteLatencyMs.WithLabelValues(topic).Observe(float64(latency))
		metrics.KafkaBatchSize.WithLabelValues(topic).Observe(float64(len(batch)))
		
		// 计算批次错误率
		errorRate := float64(errorCount) / float64(len(batch))
		metrics.KafkaBatchErrorRate.WithLabelValues(topic).Set(errorRate)
		
		// 记录批次级别的错误信息（如果批次中有部分失败）
		if errorCount > 0 {
			logFields := []zap.Field{
				zap.String("主题", topic),
				zap.Int("批次大小", len(batch)),
				zap.Int("成功数", successCount),
				zap.Int("失败数", errorCount),
				zap.Float64("错误率", errorRate),
			}
			// 如果有失败的 trace ID，添加到日志中
			if len(failedTraceIDs) > 0 {
				logFields = append(logFields, zap.Strings("失败事件trace_id", failedTraceIDs))
			}
			w.logger.Warn("批次写入部分失败", logFields...)
		}
	}
}

// writeToDLQ 写入死信队列（P0 修复：添加重试机制）
func (w *Writer) writeToDLQ(ctx context.Context, msg *Message, reason string) {
	dlqTopic := w.router.GetDLQTopic()
	
	// P0 修复：添加重试机制，最多重试 3 次
	maxRetries := 3
	backoff := 100 * time.Millisecond
	
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：100ms, 200ms, 400ms
			select {
			case <-ctx.Done():
				w.logger.Warn("DLQ 重试被取消",
					zap.String("trace_id", msg.TraceID),
					zap.Int("尝试次数", attempt))
				return
			case <-time.After(backoff):
				backoff *= 2
			}
		}
		
		if err := w.producer.SendMessage(ctx, dlqTopic, msg.Key, msg.Value, msg.Event, msg.EventType, msg.ContentType); err != nil {
			if attempt == maxRetries-1 {
				// 最后一次重试失败，记录错误
				w.logger.Error("写入死信队列失败（已重试）",
					zap.String("死信队列主题", dlqTopic),
					zap.String("原始主题", msg.Topic),
					zap.String("消息键", msg.Key),
					zap.String("trace_id", msg.TraceID),
					zap.String("失败原因", reason),
					zap.Int("消息大小", len(msg.Value)),
					zap.Int("重试次数", maxRetries),
					zap.Error(err))
				metrics.DLQWriteFailuresTotal.Inc()
				// TODO: 可以考虑写入本地文件作为最后备份
			} else {
				w.logger.Warn("写入死信队列失败，将重试",
					zap.String("trace_id", msg.TraceID),
					zap.Int("尝试次数", attempt+1),
					zap.Int("最大重试次数", maxRetries),
					zap.Error(err))
			}
			continue
		}
		
		// 成功写入
		metrics.DLQEventsTotal.WithLabelValues(reason).Inc()
		metrics.DLQEventsBytesTotal.Add(float64(len(msg.Value)))
		w.logger.Info("消息已写入死信队列",
			zap.String("死信队列主题", dlqTopic),
			zap.String("原始主题", msg.Topic),
			zap.String("trace_id", msg.TraceID),
			zap.String("失败原因", reason),
			zap.Int("重试次数", attempt))
		return
	}
}

// Wait 等待所有 workers 完成
func (w *Writer) Wait() {
	w.wg.Wait()
}

// Close 关闭 Writer
// 注意：关闭 channel 前应该先等待所有 workers 完成，否则可能导致 panic
// 建议在调用 Close() 前先调用 Wait()
func (w *Writer) Close() {
	// 使用 sync.Once 确保 channel 只关闭一次，避免 panic
	w.closeOnce.Do(func() {
		close(w.messageCh)
	})
}

var ErrQueueFull = &writerError{msg: "writer queue is full"}

type writerError struct {
	msg string
}

func (e *writerError) Error() string {
	return e.msg
}
