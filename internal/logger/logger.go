package logger

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/kafka"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// getK8sMetadata 获取 Kubernetes 元数据（Pod 名称、IP、Namespace 等）
func getK8sMetadata() []zap.Field {
	var fields []zap.Field
	
	// Pod 名称（优先使用 POD_NAME，否则使用 HOSTNAME）
	if podName := os.Getenv("POD_NAME"); podName != "" {
		fields = append(fields, zap.String("pod_name", podName))
	} else if hostname := os.Getenv("HOSTNAME"); hostname != "" {
		fields = append(fields, zap.String("pod_name", hostname))
	}
	
	// Pod IP
	if podIP := os.Getenv("POD_IP"); podIP != "" {
		fields = append(fields, zap.String("pod_ip", podIP))
	}
	
	// Namespace
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		fields = append(fields, zap.String("namespace", namespace))
	}
	
	// Node Name
	if nodeName := os.Getenv("NODE_NAME"); nodeName != "" {
		fields = append(fields, zap.String("node_name", nodeName))
	}
	
	// Node IP（节点 IP 地址）
	if nodeIP := os.Getenv("NODE_IP"); nodeIP != "" {
		fields = append(fields, zap.String("node_ip", nodeIP))
	} else if hostIP := os.Getenv("HOST_IP"); hostIP != "" {
		// 备用：使用 HOST_IP（某些环境可能使用这个）
		fields = append(fields, zap.String("node_ip", hostIP))
	}
	
	// Server Name / Hostname（作为备用）
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		fields = append(fields, zap.String("server_name", hostname))
	}
	
	return fields
}

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
	
	// 如果开启了 Kafka 日志输出，且配置了 kafka 输出，则自动禁用文件输出
	// 这样可以避免日志重复，只输出到 Kafka
	shouldOutputToKafka := contains(outputs, "kafka") && cfg.Kafka.Enabled && kafkaProducer != nil
	shouldOutputToFile := contains(outputs, "file") && !shouldOutputToKafka

	// 文件输出（仅在未开启 Kafka 时输出）
	if shouldOutputToFile {
		// 确保日志文件目录存在（lumberjack 会在写入时创建，但提前创建可以避免权限问题）
		logDir := filepath.Dir(cfg.File.Path)
		if logDir != "" && logDir != "." {
			// 尝试创建目录，如果失败不影响初始化（lumberjack 会在写入时再次尝试）
			// 但提前创建可以避免第一次写入时的延迟和潜在错误
			_ = os.MkdirAll(logDir, 0755) // 忽略错误，lumberjack 会在第一次写入时再次尝试
		}
		
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
	if shouldOutputToKafka {
		kafkaCore := NewKafkaCore(encoder, kafkaProducer, cfg.Kafka.Topic, enabler)
		cores = append(cores, kafkaCore)
	}

	// Console 输出（用于 K8s 调试，输出到 stdout）
	if cfg.Console.Enabled {
		// 使用 WriteSyncer 包装 stdout，确保日志立即刷新（不使用缓冲）
		consoleCore := zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), enabler)
		cores = append(cores, consoleCore)
	}

	// 如果没有配置任何输出，且没有 Kafka Producer，使用 no-op core（避免创建文件）
	// 但是如果启用了 Console 输出，应该已经有 core 了，所以这里只处理完全没有输出的情况
	if len(cores) == 0 {
		// 如果启用了 Console 输出，应该已经有 core 了
		// 这里只处理完全没有输出的情况（既没有 file/kafka，也没有 console）
		// 使用 no-op core，不输出任何日志（避免文件权限问题）
		// 当 Kafka Producer 创建后，会重新初始化 logger 并启用 Kafka 输出
		noOpCore := zapcore.NewNopCore()
		cores = append(cores, noOpCore)
	}

	// 使用 MultiCore 组合所有输出
	core := zapcore.NewTee(cores...)

	// 创建 logger（不输出到 stdout）
	// 添加 Kubernetes 元数据作为全局字段
	k8sFields := getK8sMetadata()
	globalLogger = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	
	// 如果有 Kubernetes 元数据，添加到 logger
	if len(k8sFields) > 0 {
		globalLogger = globalLogger.With(k8sFields...)
	}

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
