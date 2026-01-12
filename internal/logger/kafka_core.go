package logger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yourorg/tetragon-kafka-adapter/internal/kafka"
	"go.uber.org/zap/zapcore"
)

// KafkaCore 将日志发送到 Kafka 的 Core
type KafkaCore struct {
	encoder zapcore.Encoder
	producer *kafka.Producer
	topic    string
	enabler  zapcore.LevelEnabler
}

// NewKafkaCore 创建新的 KafkaCore
func NewKafkaCore(encoder zapcore.Encoder, producer *kafka.Producer, topic string, enabler zapcore.LevelEnabler) *KafkaCore {
	return &KafkaCore{
		encoder:  encoder,
		producer: producer,
		topic:    topic,
		enabler:  enabler,
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

	// 创建日志消息的 JSON 结构
	logMsg := map[string]interface{}{
		"timestamp": entry.Time.Format(time.RFC3339),
		"level":     entry.Level.String(),
		"logger":    entry.LoggerName,
		"message":   entry.Message,
		"caller":    entry.Caller.String(),
	}

	// 解析 JSON 字段
	var logFields map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logFields); err == nil {
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

	// 生成日志 key（使用时间戳和 logger 名称）
	key := entry.Time.Format("20060102150405") + "-" + entry.LoggerName

	// 发送到 Kafka（异步，不阻塞）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 使用 goroutine 异步发送，避免阻塞日志写入
	go func() {
		if err := kc.producer.SendMessage(ctx, kc.topic, key, jsonData); err != nil {
			// 如果 Kafka 发送失败，这里无法再记录日志（避免循环）
			// 可以考虑使用标准错误输出
			_ = err
		}
	}()

	return nil
}

// Sync 同步
func (kc *KafkaCore) Sync() error {
	// Kafka producer 有自己的同步机制，这里不需要额外操作
	return nil
}

// clone 克隆
func (kc *KafkaCore) clone() *KafkaCore {
	return &KafkaCore{
		encoder:  kc.encoder,
		producer: kc.producer,
		topic:    kc.topic,
		enabler:  kc.enabler,
	}
}
