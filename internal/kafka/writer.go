package kafka

import (
	"context"
	"sync"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"github.com/yourorg/tetragon-kafka-adapter/internal/router"
	"go.uber.org/zap"
)

// Message 待写入的消息
type Message struct {
	Event   *tetragon.GetEventsResponse
	Topic   string
	Key     string
	Value   []byte
	TraceID string // 追踪 ID，用于日志追踪
}

// Writer Kafka 批量写入 Worker
type Writer struct {
	producer   *Producer
	router     *router.Router
	config     *config.KafkaConfig
	logger     *zap.Logger
	workers    int
	messageCh  chan *Message
	wg         sync.WaitGroup
}

// NewWriter 创建新的 Writer
func NewWriter(producer *Producer, r *router.Router, cfg *config.KafkaConfig, logger *zap.Logger) *Writer {
	return &Writer{
		producer:  producer,
		router:    r,
		config:    cfg,
		logger:    logger,
		workers:   cfg.WriterWorkers,
		messageCh: make(chan *Message, cfg.Batch.MaxMessages*cfg.WriterWorkers),
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
				batch = batch[:0]
			}
			
		case <-ticker.C:
			// 定时刷新
			if len(batch) > 0 {
				w.flushBatch(ctx, batch)
				batch = batch[:0]
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
	
	successCount := 0
	errorCount := 0
	topic := batch[0].Topic
	
	// 收集失败的 trace ID（最多记录前 5 个，避免日志过长）
	failedTraceIDs := make([]string, 0, 5)
	
	for _, msg := range batch {
		if err := w.producer.SendMessage(flushCtx, msg.Topic, msg.Key, msg.Value); err != nil {
			errorCount++
			metrics.KafkaErrorsTotal.WithLabelValues("send_message", msg.Topic).Inc()
			
			// 收集失败的 trace ID（最多 5 个）
			if len(failedTraceIDs) < 5 {
				failedTraceIDs = append(failedTraceIDs, msg.TraceID)
			}
			
			w.logger.Error("发送消息失败",
				zap.String("主题", msg.Topic),
				zap.String("消息键", msg.Key),
				zap.String("trace_id", msg.TraceID),
				zap.Int("批次大小", len(batch)),
				zap.Error(err))
			
			// 写入 DLQ
			w.writeToDLQ(flushCtx, msg, err.Error())
		} else {
			successCount++
		}
	}

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

// writeToDLQ 写入死信队列
func (w *Writer) writeToDLQ(ctx context.Context, msg *Message, reason string) {
	dlqTopic := w.router.GetDLQTopic()
	
	// 使用原始 Key
	if err := w.producer.SendMessage(ctx, dlqTopic, msg.Key, msg.Value); err != nil {
		w.logger.Error("写入死信队列失败",
			zap.String("死信队列主题", dlqTopic),
			zap.String("原始主题", msg.Topic),
			zap.String("消息键", msg.Key),
			zap.String("trace_id", msg.TraceID),
			zap.String("失败原因", reason),
			zap.Int("消息大小", len(msg.Value)),
			zap.Error(err))
	} else {
		metrics.DLQEventsTotal.WithLabelValues(reason).Inc()
		metrics.DLQEventsBytesTotal.Add(float64(len(msg.Value)))
		// 优化：DLQ 写入成功使用 Info 级别，因为这是重要的操作
		w.logger.Info("消息已写入死信队列",
			zap.String("死信队列主题", dlqTopic),
			zap.String("原始主题", msg.Topic),
			zap.String("trace_id", msg.TraceID),
			zap.String("失败原因", reason))
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
	// 安全关闭：先关闭 channel，workers 会检测到 channel 关闭并退出
	// 但更好的做法是在调用 Close() 前先调用 Wait()
	close(w.messageCh)
}

var ErrQueueFull = &writerError{msg: "writer queue is full"}

type writerError struct {
	msg string
}

func (e *writerError) Error() string {
	return e.msg
}
