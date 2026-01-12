package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

// Load 加载配置（支持 YAML 文件和环境变量）
func Load(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	// 设置 Viper
	viper.SetConfigType("yaml")
	if configPath != "" {
		viper.SetConfigFile(configPath)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("./configs")
		viper.AddConfigPath(".")
	}

	// 读取配置文件（如果存在）
	if err := viper.ReadInConfig(); err != nil {
		// 配置文件不存在时使用默认配置
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// 设置环境变量前缀
	viper.SetEnvPrefix("")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// 绑定环境变量
	bindEnvVars()

	// 解析配置
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return cfg, nil
}

// bindEnvVars 绑定环境变量到配置
func bindEnvVars() {
	// Tetragon 配置
	viper.BindEnv("tetragon.grpc_addr", "TETRAGON_GRPC_ADDR")
	viper.BindEnv("tetragon.tls.enabled", "TETRAGON_TLS_ENABLED")
	viper.BindEnv("tetragon.tls.ca_cert", "TETRAGON_TLS_CA_CERT")
	viper.BindEnv("tetragon.tls.client_cert", "TETRAGON_TLS_CLIENT_CERT")
	viper.BindEnv("tetragon.tls.client_key", "TETRAGON_TLS_CLIENT_KEY")
	viper.BindEnv("tetragon.stream.max_queue", "STREAM_MAX_QUEUE")
	viper.BindEnv("tetragon.stream.drop_if_queue_full", "STREAM_DROP_IF_QUEUE_FULL")
	viper.BindEnv("tetragon.stream.sample_ratio", "STREAM_SAMPLE_RATIO")

	// Kafka 配置
	viper.BindEnv("kafka.brokers", "KAFKA_BROKERS")
	viper.BindEnv("kafka.client_id", "KAFKA_CLIENT_ID")
	viper.BindEnv("kafka.acks", "KAFKA_ACKS")
	viper.BindEnv("kafka.compression", "KAFKA_COMPRESSION")
	viper.BindEnv("kafka.writer_workers", "KAFKA_WRITER_WORKERS")
	viper.BindEnv("kafka.batch.max_messages", "KAFKA_BATCH_MAX_MESSAGES")
	viper.BindEnv("kafka.batch.flush_interval_ms", "KAFKA_BATCH_FLUSH_INTERVAL_MS")
	viper.BindEnv("kafka.topic_admin.auto_create", "KAFKA_TOPIC_AUTO_CREATE")
	viper.BindEnv("kafka.topic_admin.cleanup_policy", "KAFKA_TOPIC_CLEANUP_POLICY")

	// 日志配置
	viper.BindEnv("logger.level", "LOG_LEVEL")

	// 处理 KAFKA_BROKERS（逗号分隔的字符串）
	if brokers := os.Getenv("KAFKA_BROKERS"); brokers != "" {
		brokerList := strings.Split(brokers, ",")
		for i, broker := range brokerList {
			brokerList[i] = strings.TrimSpace(broker)
		}
		viper.Set("kafka.brokers", brokerList)
	}
}
