package config

import (
	"time"
)

// Config 应用配置
type Config struct {
	Tetragon TetragonConfig `mapstructure:"tetragon"`
	Kafka    KafkaConfig    `mapstructure:"kafka"`
	Routing  RoutingConfig  `mapstructure:"routing"`
	Schema   SchemaConfig   `mapstructure:"schema"`
	Logger   LoggerConfig   `mapstructure:"logger"`
	Monitoring MonitoringConfig `mapstructure:"monitoring"`
}

// TetragonConfig Tetragon gRPC 配置
type TetragonConfig struct {
	GRPCAddr string         `mapstructure:"grpc_addr"`
	TLS      TLSConfig      `mapstructure:"tls"`
	Stream   StreamConfig   `mapstructure:"stream"`
}

// TLSConfig TLS 配置
type TLSConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	CACert     string `mapstructure:"ca_cert"`
	ClientCert string `mapstructure:"client_cert"`
	ClientKey  string `mapstructure:"client_key"`
}

// StreamConfig 流配置
type StreamConfig struct {
	MaxQueue          int           `mapstructure:"max_queue"`
	DropIfQueueFull   bool          `mapstructure:"drop_if_queue_full"`
	SampleRatio       float64       `mapstructure:"sample_ratio"`
	Reconnect         ReconnectConfig `mapstructure:"reconnect"`
}

// ReconnectConfig 重连配置
type ReconnectConfig struct {
	InitialBackoffSeconds int  `mapstructure:"initial_backoff_seconds"`
	MaxBackoffSeconds     int  `mapstructure:"max_backoff_seconds"`
	MaxAttempts           int  `mapstructure:"max_attempts"` // 最大重连次数，<=0 表示无限制
	Jitter                bool `mapstructure:"jitter"`
}

// KafkaConfig Kafka 配置
type KafkaConfig struct {
	Brokers        []string        `mapstructure:"brokers"`
	ClientID       string          `mapstructure:"client_id"`
	Acks           string          `mapstructure:"acks"`
	Compression    string          `mapstructure:"compression"`
	MaxMessageBytes int            `mapstructure:"max_message_bytes"`
	Batch          BatchConfig     `mapstructure:"batch"`
	WriterWorkers  int             `mapstructure:"writer_workers"`
	TLS            TLSConfig       `mapstructure:"tls"`
	SASL           SASLConfig      `mapstructure:"sasl"`
	TopicAdmin     TopicAdminConfig `mapstructure:"topic_admin"`
	Producer       ProducerConfig   `mapstructure:"producer"`
}

// BatchConfig 批处理配置
type BatchConfig struct {
	MaxMessages    int `mapstructure:"max_messages"`
	MaxBytes       int `mapstructure:"max_bytes"`
	FlushIntervalMs int `mapstructure:"flush_interval_ms"`
}

// SASLConfig SASL 配置
type SASLConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	Mechanism   string `mapstructure:"mechanism"`
	Username    string `mapstructure:"username"`
	PasswordFile string `mapstructure:"password_file"`
}

// TopicAdminConfig Topic 管理配置
type TopicAdminConfig struct {
	AutoCreate         bool   `mapstructure:"auto_create"`
	Partitions         int    `mapstructure:"partitions"`
	ReplicationFactor  *int16 `mapstructure:"replication_factor"` // 可选，nil 或未设置时使用 broker 默认值
	CleanupPolicy      string `mapstructure:"cleanup_policy"`
	MinCleanableDirtyRatio string `mapstructure:"min_cleanable_dirty_ratio"`
	RetentionMs        int64  `mapstructure:"retention_ms"`
}

// ProducerConfig Producer 配置
type ProducerConfig struct {
	EnableIdempotence bool `mapstructure:"enable_idempotence"`
}

// RoutingConfig 路由配置
type RoutingConfig struct {
	Topics      map[string]string `mapstructure:"topics"`
	PartitionKey PartitionKeyConfig `mapstructure:"partition_key"`
}

// PartitionKeyConfig Partition Key 配置
type PartitionKeyConfig struct {
	Mode      string   `mapstructure:"mode"`
	Fields    []string `mapstructure:"fields"`
	Separator string   `mapstructure:"separator"`
}

// SchemaConfig Schema 配置
type SchemaConfig struct {
	Version    int    `mapstructure:"version"`
	Mode       string `mapstructure:"mode"`
	Format     string `mapstructure:"format"` // P1 修复：序列化格式 "json" | "protobuf"
	IncludeRaw bool   `mapstructure:"include_raw"`
}

// LoggerConfig 日志配置
type LoggerConfig struct {
	Level   string         `mapstructure:"level"`
	Format  string         `mapstructure:"format"`
	Output  []string       `mapstructure:"output"` // 支持多个输出：file, kafka
	Console ConsoleLogConfig `mapstructure:"console"` // Console 输出配置（用于 K8s 调试）
	File    FileLogConfig  `mapstructure:"file"`
	Kafka   KafkaLogConfig `mapstructure:"kafka"`
}

// FileLogConfig 文件日志配置
type FileLogConfig struct {
	Path       string `mapstructure:"path"`
	MaxSizeMB  int    `mapstructure:"max_size_mb"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAgeDays int    `mapstructure:"max_age_days"`
}

// ConsoleLogConfig Console 日志配置（用于 K8s 调试）
type ConsoleLogConfig struct {
	Enabled bool `mapstructure:"enabled"` // 是否启用 console 输出（stdout）
}

// KafkaLogConfig Kafka 日志配置
type KafkaLogConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Topic   string `mapstructure:"topic"`
}

// MonitoringConfig 监控配置
type MonitoringConfig struct {
	Enabled    bool `mapstructure:"enabled"`
	HealthPort int  `mapstructure:"health_port"`
	MetricsPort int  `mapstructure:"metrics_port"`
	PprofEnabled bool `mapstructure:"pprof_enabled"`
	PprofPort   int  `mapstructure:"pprof_port"`
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Tetragon: TetragonConfig{
			GRPCAddr: "localhost:54321",
			TLS: TLSConfig{
				Enabled: false,
			},
			Stream: StreamConfig{
				MaxQueue:        50000,
				DropIfQueueFull: true,
				SampleRatio:     1.0,
				Reconnect: ReconnectConfig{
					InitialBackoffSeconds: 1,
					MaxBackoffSeconds:     30,
					MaxAttempts:           -1, // -1 表示无限制
					Jitter:                true,
				},
			},
		},
		Kafka: KafkaConfig{
			Brokers:         []string{"localhost:9092"},
			ClientID:        "tetragon-kafka-adapter",
			Acks:            "all",
			Compression:     "snappy",
			MaxMessageBytes: 1048576,
			Batch: BatchConfig{
				MaxMessages:     3000,
				MaxBytes:        1048576,
				FlushIntervalMs: 100,
			},
			WriterWorkers: 12,
			TopicAdmin: TopicAdminConfig{
				AutoCreate:         true,
				Partitions:         24,
				ReplicationFactor:  nil, // nil 表示使用 broker 的默认副本因子
				CleanupPolicy:      "compact",
				MinCleanableDirtyRatio: "0.5",
				RetentionMs:        604800000,
			},
			Producer: ProducerConfig{
				EnableIdempotence: true,
			},
		},
		Routing: RoutingConfig{
			Topics: map[string]string{
				"process_exec":     "tetragon.process.exec",
				"process_exit":      "tetragon.process.exit",
				"process_lsm":      "tetragon.security.lsm",
				"process_kprobe":   "tetragon.syscall.kprobe",
				"process_tracepoint": "tetragon.kernel.tracepoint",
				"unknown":          "tetragon.unknown",
				"dlq":              "tetragon.dlq",
			},
			PartitionKey: PartitionKeyConfig{
				Mode:      "deduplication",
				Fields:    []string{"node", "type", "process.pid", "timestamp"},
				Separator: ":",
			},
		},
		Schema: SchemaConfig{
			Version:    1,
			Mode:       "stable_json",
			Format:     "json", // P1 修复：默认使用 JSON
			IncludeRaw: false,
		},
		Logger: LoggerConfig{
			Level:  "info",
			Format: "json",
			Output: []string{"file"}, // 默认只输出到文件
			Console: ConsoleLogConfig{
				Enabled: false, // 默认不输出到 console（K8s 环境可通过此开关开启调试）
			},
			File: FileLogConfig{
				Path:       "/var/log/tetragon-kafka-adapter/app.log",
				MaxSizeMB:  100,
				MaxBackups: 5,
				MaxAgeDays: 7,
			},
			Kafka: KafkaLogConfig{
				Enabled: false,
				Topic:   "tetragon.logs",
			},
		},
		Monitoring: MonitoringConfig{
			Enabled:    true,
			HealthPort: 8080,
			MetricsPort: 9090,
			PprofEnabled: false,
			PprofPort:   6060,
		},
	}
}

// GetReconnectBackoff 获取重连退避时间
func (c *Config) GetReconnectBackoff(attempt int) time.Duration {
	backoff := time.Duration(c.Tetragon.Stream.Reconnect.InitialBackoffSeconds) * time.Second
	for i := 0; i < attempt; i++ {
		backoff *= 2
	}
	if backoff > time.Duration(c.Tetragon.Stream.Reconnect.MaxBackoffSeconds)*time.Second {
		backoff = time.Duration(c.Tetragon.Stream.Reconnect.MaxBackoffSeconds) * time.Second
	}
	return backoff
}
