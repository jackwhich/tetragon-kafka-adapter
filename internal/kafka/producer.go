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
	"go.uber.org/zap"
)

// Producer Kafka Producer 封装
type Producer struct {
	producer sarama.AsyncProducer
	config   *config.KafkaConfig
	logger   *zap.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewProducer 创建新的 Producer
func NewProducer(cfg *config.KafkaConfig, logger *zap.Logger) (*Producer, error) {
	saramaConfig := sarama.NewConfig()
	
	// 网络超时配置（防止连接时卡住）
	saramaConfig.Net.DialTimeout = 10 * time.Second
	saramaConfig.Net.ReadTimeout = 10 * time.Second
	saramaConfig.Net.WriteTimeout = 10 * time.Second
	saramaConfig.Net.KeepAlive = 30 * time.Second
	
	// 元数据配置（防止获取元数据时卡住）
	saramaConfig.Metadata.Retry.Max = 3
	saramaConfig.Metadata.Retry.Backoff = 250 * time.Millisecond
	saramaConfig.Metadata.Timeout = 10 * time.Second
	saramaConfig.Metadata.RefreshFrequency = 10 * time.Minute
	
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

	// 创建异步 Producer（带超时控制）
	// 注意：NewAsyncProducer 会尝试连接 broker 获取元数据，如果 broker 不可达会超时
	logger.Info("正在连接 Kafka broker 获取元数据...",
		zap.Strings("broker地址", cfg.Brokers),
		zap.Duration("超时时间", saramaConfig.Metadata.Timeout))
	producer, err := sarama.NewAsyncProducer(cfg.Brokers, saramaConfig)
	if err != nil {
		logger.Error("创建 Kafka Producer 失败",
			zap.Strings("broker地址", cfg.Brokers),
			zap.Error(err),
			zap.String("提示", "请检查：1) Kafka broker 地址是否正确 2) DNS 是否能解析 broker 主机名 3) 网络是否可达 4) 端口是否正确"))
		return nil, fmt.Errorf("创建 Kafka Producer 失败: %w", err)
	}
	
	logger.Info("Kafka Producer 创建成功",
		zap.Strings("broker地址", cfg.Brokers))

	ctx, cancel := context.WithCancel(context.Background())
	
	p := &Producer{
		producer: producer,
		config:   cfg,
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
	}

	// 启动错误处理 goroutine
	p.wg.Add(2)
	go p.handleErrors()
	go p.handleSuccesses()

	metrics.KafkaProducerConnected.Set(1)

	return p, nil
}

// SendMessage 发送消息（P1 修复：添加 Headers 和正确的时间戳）
func (p *Producer) SendMessage(ctx context.Context, topic string, key string, value []byte, event *tetragon.GetEventsResponse, eventType string) error {
	// P1 修复：使用事件的实际时间戳
	var msgTimestamp time.Time
	if event != nil && event.GetTime() != nil {
		msgTimestamp = event.GetTime().AsTime()
	} else {
		msgTimestamp = time.Now()
	}

	// P1 修复：添加消息 Headers
	headers := []sarama.RecordHeader{
		{Key: []byte("content-type"), Value: []byte("application/json")},
		{Key: []byte("schema-version"), Value: []byte("1")},
	}
	if eventType != "" {
		headers = append(headers, sarama.RecordHeader{
			Key:   []byte("event-type"),
			Value: []byte(eventType),
		})
	}

	msg := &sarama.ProducerMessage{
		Topic:     topic,
		Key:       sarama.StringEncoder(key),
		Value:     sarama.ByteEncoder(value),
		Timestamp: msgTimestamp,
		Headers:   headers,
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
	defer p.wg.Done()
	
	for {
		select {
		case <-p.ctx.Done():
			// Context 已取消，退出
			return
		case err, ok := <-p.producer.Errors():
			if !ok {
				// Channel 已关闭，退出
				return
			}
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
}

// handleSuccesses 处理发送成功
func (p *Producer) handleSuccesses() {
	defer p.wg.Done()
	
	for {
		select {
		case <-p.ctx.Done():
			// Context 已取消，退出
			return
		case msg, ok := <-p.producer.Successes():
			if !ok {
				// Channel 已关闭，退出
				return
			}
			metrics.KafkaWriteMessagesTotal.WithLabelValues(msg.Topic).Inc()
			// 性能优化：使用类型断言避免 panic，并检查 nil
			if msg.Value != nil {
				if byteEncoder, ok := msg.Value.(sarama.ByteEncoder); ok {
					metrics.KafkaWriteBytesTotal.WithLabelValues(msg.Topic).Add(float64(len(byteEncoder)))
				}
			}
		}
	}
}

// Close 关闭 Producer
func (p *Producer) Close() error {
	metrics.KafkaProducerConnected.Set(0)
	
	// 先取消 context，让 goroutine 退出
	p.cancel()
	
	// 关闭 producer（这会关闭 Errors 和 Successes channel）
	err := p.producer.Close()
	
	// 等待所有 goroutine 退出
	p.wg.Wait()
	
	return err
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
