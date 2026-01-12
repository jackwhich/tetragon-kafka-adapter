package logger

import (
	"strings"

	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/kafka"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var globalLogger *zap.Logger

// Init 初始化日志（支持多输出：文件、Kafka，不输出到 stdout）
func Init(cfg *config.LoggerConfig, kafkaProducer *kafka.Producer) error {
	var zapLevel zapcore.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		zapLevel = zapcore.DebugLevel
	case "info":
		zapLevel = zapcore.InfoLevel
	case "warn":
		zapLevel = zapcore.WarnLevel
	case "error":
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	// 创建编码器配置
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// 创建编码器
	var encoder zapcore.Encoder
	if strings.ToLower(cfg.Format) == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// 收集所有 cores
	var cores []zapcore.Core
	enabler := zap.LevelEnablerFunc(func(level zapcore.Level) bool {
		return level >= zapLevel
	})

	// 检查输出配置
	outputs := cfg.Output
	if len(outputs) == 0 {
		// 如果没有配置，默认使用文件输出
		outputs = []string{"file"}
	}

	// 文件输出
	if contains(outputs, "file") {
		fileWriter := &lumberjack.Logger{
			Filename:   cfg.File.Path,
			MaxSize:    cfg.File.MaxSizeMB, // MB
			MaxBackups: cfg.File.MaxBackups,
			MaxAge:     cfg.File.MaxAgeDays, // days
			Compress:   true,
		}
		fileCore := zapcore.NewCore(encoder, zapcore.AddSync(fileWriter), enabler)
		cores = append(cores, fileCore)
	}

	// Kafka 输出
	if contains(outputs, "kafka") && cfg.Kafka.Enabled && kafkaProducer != nil {
		kafkaCore := NewKafkaCore(encoder, kafkaProducer, cfg.Kafka.Topic, enabler)
		cores = append(cores, kafkaCore)
	}

	// 如果没有配置任何输出，至少输出到文件（避免没有日志）
	if len(cores) == 0 {
		fileWriter := &lumberjack.Logger{
			Filename:   cfg.File.Path,
			MaxSize:    100, // MB
			MaxBackups: 5,
			MaxAge:     7, // days
			Compress:   true,
		}
		fileCore := zapcore.NewCore(encoder, zapcore.AddSync(fileWriter), enabler)
		cores = append(cores, fileCore)
	}

	// 使用 MultiCore 组合所有输出
	core := zapcore.NewTee(cores...)

	// 创建 logger（不输出到 stdout）
	globalLogger = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	return nil
}

// InitSimple 简单初始化（向后兼容，不推荐使用）
func InitSimple(level, format string) error {
	cfg := &config.LoggerConfig{
		Level:  level,
		Format: format,
		Output: []string{"file"},
		File: config.FileLogConfig{
			Path:       "/var/log/tetragon-kafka-adapter/app.log",
			MaxSizeMB:  100,
			MaxBackups: 5,
			MaxAgeDays: 7,
		},
	}
	return Init(cfg, nil)
}

// GetLogger 获取全局 logger
func GetLogger() *zap.Logger {
	if globalLogger == nil {
		// 如果没有初始化，创建一个默认的 logger（输出到文件）
		cfg := &config.LoggerConfig{
			Level:  "info",
			Format: "json",
			Output: []string{"file"},
			File: config.FileLogConfig{
				Path:       "/var/log/tetragon-kafka-adapter/app.log",
				MaxSizeMB:  100,
				MaxBackups: 5,
				MaxAgeDays: 7,
			},
		}
		Init(cfg, nil)
	}
	return globalLogger
}

// Sync 同步日志
func Sync() error {
	if globalLogger != nil {
		return globalLogger.Sync()
	}
	return nil
}

// contains 检查字符串切片是否包含指定字符串
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, item) {
			return true
		}
	}
	return false
}
