package kafka

import (
	"context"
	"time"

	"github.com/IBM/sarama"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"go.uber.org/zap"
)

// Producer Kafka Producer 封装
type Producer struct {
	producer sarama.AsyncProducer
	config   *config.KafkaConfig
	logger   *zap.Logger
}

// NewProducer 创建新的 Producer
func NewProducer(cfg *config.KafkaConfig, logger *zap.Logger) (*Producer, error) {
	saramaConfig := sarama.NewConfig()
	
	// 基本配置
	saramaConfig.Producer.Return.Successes = true
	saramaConfig.Producer.Return.Errors = true
	saramaConfig.Producer.RequiredAcks = parseAcks(cfg.Acks)
	saramaConfig.Producer.Compression = parseCompression(cfg.Compression)
	saramaConfig.Producer.MaxMessageBytes = cfg.MaxMessageBytes
	
	// 批处理配置
	saramaConfig.Producer.Flush.Messages = cfg.Batch.MaxMessages
	saramaConfig.Producer.Flush.Bytes = cfg.Batch.MaxBytes
	saramaConfig.Producer.Flush.Frequency = time.Duration(cfg.Batch.FlushIntervalMs) * time.Millisecond
	
	// 幂等性（方案 1：配合 Compacted Topic 使用）
	if cfg.Producer.EnableIdempotence {
		saramaConfig.Producer.Idempotent = true
		saramaConfig.Net.MaxOpenRequests = 1
	}

	// 创建异步 Producer
	producer, err := sarama.NewAsyncProducer(cfg.Brokers, saramaConfig)
	if err != nil {
		return nil, err
	}

	p := &Producer{
		producer: producer,
		config:   cfg,
		logger:   logger,
	}

	// 启动错误处理 goroutine
	go p.handleErrors()
	go p.handleSuccesses()

	metrics.KafkaProducerConnected.Set(1)

	return p, nil
}

// SendMessage 发送消息（使用去重 Key）
func (p *Producer) SendMessage(ctx context.Context, topic string, key string, value []byte) error {
	msg := &sarama.ProducerMessage{
		Topic: topic,
		Key:   sarama.StringEncoder(key),
		Value: sarama.ByteEncoder(value),
		Timestamp: time.Now(),
	}

	select {
	case p.producer.Input() <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleErrors 处理发送错误
func (p *Producer) handleErrors() {
	for err := range p.producer.Errors() {
		if err != nil {
			metrics.KafkaErrorsTotal.WithLabelValues("write", err.Msg.Topic).Inc()
			// 增强日志：添加更多上下文信息
			var keyStr string
			if err.Msg.Key != nil {
				if keyEncoder, ok := err.Msg.Key.(sarama.StringEncoder); ok {
					keyStr = string(keyEncoder)
				}
			}
			p.logger.Error("发送消息到 Kafka 失败",
				zap.String("主题", err.Msg.Topic),
				zap.String("消息键", keyStr),
				zap.Int("分区", int(err.Msg.Partition)),
				zap.Int64("偏移量", err.Msg.Offset),
				zap.Error(err.Err))
		}
	}
}

// handleSuccesses 处理发送成功
func (p *Producer) handleSuccesses() {
	for msg := range p.producer.Successes() {
		metrics.KafkaWriteMessagesTotal.WithLabelValues(msg.Topic).Inc()
		// 性能优化：使用类型断言避免 panic，并检查 nil
		if msg.Value != nil {
			if byteEncoder, ok := msg.Value.(sarama.ByteEncoder); ok {
				metrics.KafkaWriteBytesTotal.WithLabelValues(msg.Topic).Add(float64(len(byteEncoder)))
			}
		}
	}
}

// Close 关闭 Producer
func (p *Producer) Close() error {
	metrics.KafkaProducerConnected.Set(0)
	return p.producer.Close()
}

// parseAcks 解析 ACKS 配置
func parseAcks(acks string) sarama.RequiredAcks {
	switch acks {
	case "all":
		return sarama.WaitForAll
	case "1":
		return sarama.WaitForLocal
	case "0":
		return sarama.NoResponse
	default:
		return sarama.WaitForAll
	}
}

// parseCompression 解析压缩配置
func parseCompression(compression string) sarama.CompressionCodec {
	switch compression {
	case "gzip":
		return sarama.CompressionGZIP
	case "snappy":
		return sarama.CompressionSnappy
	case "lz4":
		return sarama.CompressionLZ4
	case "zstd":
		return sarama.CompressionZSTD
	default:
		return sarama.CompressionNone
	}
}
