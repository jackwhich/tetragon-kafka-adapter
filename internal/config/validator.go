package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Validate 验证配置
func Validate(cfg *Config) error {
	// 验证 Tetragon 配置
	if cfg.Tetragon.GRPCAddr == "" {
		return fmt.Errorf("tetragon.grpc_addr is required")
	}

	// 验证 Kafka 配置
	if len(cfg.Kafka.Brokers) == 0 {
		return fmt.Errorf("kafka.brokers is required")
	}
	for _, broker := range cfg.Kafka.Brokers {
		if broker == "" {
			return fmt.Errorf("kafka.brokers contains empty broker")
		}
		// 验证 broker 地址格式 (host:port)
		host, port, err := net.SplitHostPort(broker)
		if err != nil {
			return fmt.Errorf("invalid kafka broker address format '%s': %w (expected host:port)", broker, err)
		}
		if host == "" {
			return fmt.Errorf("kafka broker address '%s' has empty host", broker)
		}
		if port == "" {
			return fmt.Errorf("kafka broker address '%s' has empty port", broker)
		}
		portNum, err := strconv.Atoi(port)
		if err != nil {
			return fmt.Errorf("invalid port in kafka broker address '%s': %w", broker, err)
		}
		if portNum <= 0 || portNum > 65535 {
			return fmt.Errorf("invalid port number %d in kafka broker address '%s' (must be 1-65535)", portNum, broker)
		}
	}

	if cfg.Kafka.ClientID == "" {
		return fmt.Errorf("kafka.client_id is required")
	}

	// 验证 ACKS
	validAcks := map[string]bool{"all": true, "1": true, "0": true}
	if !validAcks[strings.ToLower(cfg.Kafka.Acks)] {
		return fmt.Errorf("kafka.acks must be one of: all, 1, 0")
	}

	// 验证压缩
	validCompression := map[string]bool{"none": true, "gzip": true, "snappy": true, "lz4": true, "zstd": true}
	if !validCompression[strings.ToLower(cfg.Kafka.Compression)] {
		return fmt.Errorf("kafka.compression must be one of: none, gzip, snappy, lz4, zstd")
	}

	// 验证队列大小
	if cfg.Tetragon.Stream.MaxQueue <= 0 {
		return fmt.Errorf("tetragon.stream.max_queue must be > 0")
	}
	if cfg.Tetragon.Stream.MaxQueue > 1000000 {
		return fmt.Errorf("tetragon.stream.max_queue must be <= 1000000 (too large)")
	}

	// 验证采样比例
	if cfg.Tetragon.Stream.SampleRatio < 0 || cfg.Tetragon.Stream.SampleRatio > 1 {
		return fmt.Errorf("tetragon.stream.sample_ratio must be between 0 and 1")
	}

	// 验证 Writer Workers
	if cfg.Kafka.WriterWorkers <= 0 {
		return fmt.Errorf("kafka.writer_workers must be > 0")
	}
	if cfg.Kafka.WriterWorkers > 1000 {
		return fmt.Errorf("kafka.writer_workers must be <= 1000 (too large)")
	}

	// 验证批处理配置
	if cfg.Kafka.Batch.MaxMessages <= 0 {
		return fmt.Errorf("kafka.batch.max_messages must be > 0")
	}
	if cfg.Kafka.Batch.MaxMessages > 100000 {
		return fmt.Errorf("kafka.batch.max_messages must be <= 100000 (too large)")
	}
	if cfg.Kafka.Batch.FlushIntervalMs <= 0 {
		return fmt.Errorf("kafka.batch.flush_interval_ms must be > 0")
	}
	if cfg.Kafka.Batch.FlushIntervalMs > 60000 {
		return fmt.Errorf("kafka.batch.flush_interval_ms must be <= 60000 (60 seconds)")
	}
	if cfg.Kafka.Batch.MaxBytes <= 0 {
		return fmt.Errorf("kafka.batch.max_bytes must be > 0")
	}
	if cfg.Kafka.Batch.MaxBytes > 100*1024*1024 {
		return fmt.Errorf("kafka.batch.max_bytes must be <= 100MB (too large)")
	}

	// 验证路由配置
	if len(cfg.Routing.Topics) == 0 {
		return fmt.Errorf("routing.topics is required")
	}

	// 验证 Partition Key 模式
	validModes := map[string]bool{"deduplication": true, "fields_concat": true, "hash": true, "random": true}
	if !validModes[cfg.Routing.PartitionKey.Mode] {
		return fmt.Errorf("routing.partition_key.mode must be one of: deduplication, fields_concat, hash, random")
	}

	// 验证 Schema 模式
	validSchemaModes := map[string]bool{"stable_json": true, "raw_string_fallback": true}
	if !validSchemaModes[cfg.Schema.Mode] {
		return fmt.Errorf("schema.mode must be one of: stable_json, raw_string_fallback")
	}
	
	// P1 修复：验证 Schema 格式
	validSchemaFormats := map[string]bool{"json": true, "protobuf": true}
	if cfg.Schema.Format != "" && !validSchemaFormats[cfg.Schema.Format] {
		return fmt.Errorf("schema.format must be one of: json, protobuf")
	}

	// 验证重连配置
	if cfg.Tetragon.Stream.Reconnect.InitialBackoffSeconds <= 0 {
		return fmt.Errorf("tetragon.stream.reconnect.initial_backoff_seconds must be > 0")
	}
	if cfg.Tetragon.Stream.Reconnect.InitialBackoffSeconds > 3600 {
		return fmt.Errorf("tetragon.stream.reconnect.initial_backoff_seconds must be <= 3600 (1 hour)")
	}
	if cfg.Tetragon.Stream.Reconnect.MaxBackoffSeconds <= 0 {
		return fmt.Errorf("tetragon.stream.reconnect.max_backoff_seconds must be > 0")
	}
	if cfg.Tetragon.Stream.Reconnect.MaxBackoffSeconds > 3600 {
		return fmt.Errorf("tetragon.stream.reconnect.max_backoff_seconds must be <= 3600 (1 hour)")
	}
	if cfg.Tetragon.Stream.Reconnect.MaxBackoffSeconds < cfg.Tetragon.Stream.Reconnect.InitialBackoffSeconds {
		return fmt.Errorf("tetragon.stream.reconnect.max_backoff_seconds must be >= initial_backoff_seconds")
	}

	// 验证监控配置
	if cfg.Monitoring.Enabled {
		if cfg.Monitoring.HealthPort <= 0 || cfg.Monitoring.HealthPort > 65535 {
			return fmt.Errorf("monitoring.health_port must be between 1 and 65535")
		}
		if cfg.Monitoring.MetricsPort <= 0 || cfg.Monitoring.MetricsPort > 65535 {
			return fmt.Errorf("monitoring.metrics_port must be between 1 and 65535")
		}
		if cfg.Monitoring.HealthPort == cfg.Monitoring.MetricsPort {
			return fmt.Errorf("monitoring.health_port and metrics_port must be different")
		}
		if cfg.Monitoring.PprofEnabled {
			if cfg.Monitoring.PprofPort <= 0 || cfg.Monitoring.PprofPort > 65535 {
				return fmt.Errorf("monitoring.pprof_port must be between 1 and 65535")
			}
			if cfg.Monitoring.PprofPort == cfg.Monitoring.HealthPort || cfg.Monitoring.PprofPort == cfg.Monitoring.MetricsPort {
				return fmt.Errorf("monitoring.pprof_port must be different from health_port and metrics_port")
			}
		}
	}

	// 验证 gRPC 地址格式
	if cfg.Tetragon.GRPCAddr != "" {
		host, port, err := net.SplitHostPort(cfg.Tetragon.GRPCAddr)
		if err != nil {
			return fmt.Errorf("invalid tetragon.grpc_addr format '%s': %w (expected host:port)", cfg.Tetragon.GRPCAddr, err)
		}
		if host == "" {
			return fmt.Errorf("tetragon.grpc_addr '%s' has empty host", cfg.Tetragon.GRPCAddr)
		}
		if port == "" {
			return fmt.Errorf("tetragon.grpc_addr '%s' has empty port", cfg.Tetragon.GRPCAddr)
		}
		portNum, err := strconv.Atoi(port)
		if err != nil {
			return fmt.Errorf("invalid port in tetragon.grpc_addr '%s': %w", cfg.Tetragon.GRPCAddr, err)
		}
		if portNum <= 0 || portNum > 65535 {
			return fmt.Errorf("invalid port number %d in tetragon.grpc_addr '%s' (must be 1-65535)", portNum, cfg.Tetragon.GRPCAddr)
		}
	}

	return nil
}
