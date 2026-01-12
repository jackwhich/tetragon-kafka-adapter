package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/yourorg/tetragon-kafka-adapter/internal/kafka"
	"go.uber.org/zap/zapcore"
)

// logTraceIDPool 用于复用字节数组，减少内存分配
var logTraceIDPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 16)
	},
}

// generateLogTraceID 生成日志 trace ID（16字节，32字符hex字符串）
func generateLogTraceID() string {
	b := logTraceIDPool.Get().([]byte)
	defer logTraceIDPool.Put(b)
	
	rand.Read(b)
	return hex.EncodeToString(b)
}

// KafkaCore 将日志发送到 Kafka 的 Core
type KafkaCore struct {
	encoder  zapcore.Encoder
	producer *kafka.Producer
	topic    string
	enabler  zapcore.LevelEnabler
	logCh    chan []byte
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	closeOnce sync.Once // 确保 Close() 只执行一次
}

// NewKafkaCore 创建新的 KafkaCore
func NewKafkaCore(encoder zapcore.Encoder, producer *kafka.Producer, topic string, enabler zapcore.LevelEnabler) *KafkaCore {
	ctx, cancel := context.WithCancel(context.Background())
	
	kc := &KafkaCore{
		encoder:  encoder,
		producer: producer,
		topic:    topic,
		enabler:  enabler,
		logCh:    make(chan []byte, 1000), // 缓冲 1000 条日志，避免阻塞
		ctx:      ctx,
		cancel:   cancel,
	}
	
	// 启动后台 worker 处理日志发送
	kc.wg.Add(1)
	go kc.logWorker()
	
	return kc
}

// logWorker 后台 worker，批量处理日志发送
func (kc *KafkaCore) logWorker() {
	defer kc.wg.Done()
	
	ticker := time.NewTicker(100 * time.Millisecond) // 每 100ms 批量发送一次
	defer ticker.Stop()
	
	batch := make([][]byte, 0, 100) // 批量发送，最多 100 条
	
	for {
		select {
		case <-kc.ctx.Done():
			// 关闭前发送剩余日志
			if len(batch) > 0 {
				kc.flushBatch(batch)
			}
			return
			
		case logData, ok := <-kc.logCh:
			if !ok {
				// Channel 已关闭，发送剩余日志并退出
				if len(batch) > 0 {
					kc.flushBatch(batch)
				}
				return
			}
			
			batch = append(batch, logData)
			
			// 达到批量大小，立即发送
			if len(batch) >= 100 {
				kc.flushBatch(batch)
				batch = batch[:0]
			}
			
		case <-ticker.C:
			// 定时发送
			if len(batch) > 0 {
				kc.flushBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

// flushBatch 批量发送日志
func (kc *KafkaCore) flushBatch(batch [][]byte) {
	ctx, cancel := context.WithTimeout(kc.ctx, 5*time.Second)
	defer cancel()
	
	// 生成日志 key（使用时间戳，批量使用同一个时间戳）
	key := time.Now().Format("20060102150405")
	
	for _, logData := range batch {
		// 发送到 Kafka（非阻塞，如果失败会被 producer 的错误处理机制捕获）
		_ = kc.producer.SendMessage(ctx, kc.topic, key, logData)
	}
}

// Enabled 检查是否启用
func (kc *KafkaCore) Enabled(level zapcore.Level) bool {
	return kc.enabler.Enabled(level)
}

// With 添加字段
func (kc *KafkaCore) With(fields []zapcore.Field) zapcore.Core {
	clone := kc.clone()
	clone.encoder = clone.encoder.Clone()
	for _, field := range fields {
		field.AddTo(clone.encoder)
	}
	return clone
}

// Check 检查并添加字段
func (kc *KafkaCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if kc.Enabled(entry.Level) {
		return checked.AddCore(entry, kc)
	}
	return checked
}

// Write 写入日志到 Kafka
func (kc *KafkaCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	// 编码日志条目
	buf, err := kc.encoder.EncodeEntry(entry, fields)
	if err != nil {
		return err
	}
	defer buf.Free()

	// 生成 trace ID（每条日志一个独立的 trace ID）
	traceID := generateLogTraceID()
	
	// 创建日志消息的 JSON 结构
	logMsg := map[string]interface{}{
		"timestamp": entry.Time.Format(time.RFC3339),
		"level":     entry.Level.String(),
		"logger":    entry.LoggerName,
		"message":   entry.Message,
		"caller":    entry.Caller.String(),
		"trace_id":  traceID, // 添加 trace_id 字段
	}

	// 解析 JSON 字段
	var logFields map[string]interface{}
	if unmarshalErr := json.Unmarshal(buf.Bytes(), &logFields); unmarshalErr == nil {
		// 合并字段
		for k, v := range logFields {
			if k != "ts" && k != "level" && k != "msg" && k != "logger" && k != "caller" {
				logMsg[k] = v
			}
		}
	}

	// 序列化为 JSON
	jsonData, err := json.Marshal(logMsg)
	if err != nil {
		return err
	}

	// 发送到缓冲 channel（非阻塞，如果 channel 满则丢弃，避免阻塞日志写入）
	select {
	case kc.logCh <- jsonData:
		// 成功发送到 channel
	case <-kc.ctx.Done():
		// Context 已取消，不再发送
		return kc.ctx.Err()
	default:
		// Channel 已满，丢弃日志（避免阻塞）
		// 在高负载情况下，丢弃部分日志比阻塞整个应用更好
	}

	return nil
}

// Sync 同步
func (kc *KafkaCore) Sync() error {
	// 等待所有日志发送完成（最多等待 5 秒）
	done := make(chan struct{})
	go func() {
		kc.wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		// 超时，但继续执行
		return nil
	}
}

// clone 克隆
func (kc *KafkaCore) clone() *KafkaCore {
	// 注意：clone 时共享同一个 logCh 和 worker，避免创建多个 worker
	// 不复制 WaitGroup，因为它是共享的
	return &KafkaCore{
		encoder:  kc.encoder,
		producer: kc.producer,
		topic:    kc.topic,
		enabler:  kc.enabler,
		logCh:    kc.logCh, // 共享同一个 channel
		ctx:      kc.ctx,
		cancel:   kc.cancel,
		// 不复制 wg，因为多个 clone 共享同一个 worker
	}
}

// Close 关闭 KafkaCore，清理资源
func (kc *KafkaCore) Close() error {
	var err error
	kc.closeOnce.Do(func() {
		kc.cancel()  // 取消 context
		close(kc.logCh) // 关闭 channel
		kc.wg.Wait() // 等待 worker 退出
	})
	return err
}
