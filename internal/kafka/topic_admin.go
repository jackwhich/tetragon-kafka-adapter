package kafka

import (
	"context"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"go.uber.org/zap"
)

// TopicAdmin Topic 管理
type TopicAdmin struct {
	admin    sarama.ClusterAdmin
	config   *config.TopicAdminConfig
	logger   *zap.Logger
}

// NewTopicAdmin 创建新的 TopicAdmin
func NewTopicAdmin(brokers []string, cfg *config.TopicAdminConfig, logger *zap.Logger) (*TopicAdmin, error) {
	admin, err := sarama.NewClusterAdmin(brokers, sarama.NewConfig())
	if err != nil {
		logger.Error("创建 Kafka 集群管理员失败",
			zap.Strings("代理列表", brokers),
			zap.Error(err))
		return nil, err
	}

	logger.Info("Kafka 集群管理员创建成功",
		zap.Strings("代理列表", brokers))
	
	return &TopicAdmin{
		admin:  admin,
		config: cfg,
		logger: logger,
	}, nil
}

// CreateCompactedTopic 创建 Compacted Topic（方案 1）
func (ta *TopicAdmin) CreateCompactedTopic(ctx context.Context, topic string) error {
	start := time.Now()
	defer func() {
		metrics.KafkaTopicCreateDurationSeconds.Observe(time.Since(start).Seconds())
	}()

	// 检查 topic 是否已存在
	topics, err := ta.admin.ListTopics()
	if err != nil {
		return err
	}

	if _, exists := topics[topic]; exists {
		ta.logger.Info("Topic 已存在，跳过创建", zap.String("主题", topic))
		metrics.KafkaTopicCreateTotal.WithLabelValues("skipped").Inc()
		return nil
	}

	// 创建 Compacted Topic
	topicDetail := &sarama.TopicDetail{
		NumPartitions:     int32(ta.config.Partitions),
		ReplicationFactor: ta.config.ReplicationFactor,
		ConfigEntries: map[string]*string{
			"cleanup.policy":              stringPtr(ta.config.CleanupPolicy),
			"min.cleanable.dirty.ratio":   stringPtr(ta.config.MinCleanableDirtyRatio),
			"segment.ms":                  stringPtr("3600000"), // 1 小时
			"retention.ms":                int64Ptr(ta.config.RetentionMs),
		},
	}

	err = ta.admin.CreateTopic(topic, topicDetail, false)
	if err != nil {
		metrics.KafkaTopicCreateTotal.WithLabelValues("failed").Inc()
		return err
	}

	ta.logger.Info("已创建 Compacted Topic", 
		zap.String("主题", topic),
		zap.Int32("分区数", topicDetail.NumPartitions),
		zap.Int16("副本因子", topicDetail.ReplicationFactor))
	
	metrics.KafkaTopicCreateTotal.WithLabelValues("success").Inc()
	return nil
}

// EnsureTopics 确保所有配置的 topics 存在
func (ta *TopicAdmin) EnsureTopics(ctx context.Context, topics []string) error {
	if !ta.config.AutoCreate {
		ta.logger.Info("Topic 自动创建已禁用，跳过",
			zap.Strings("主题列表", topics))
		return nil
	}

	ta.logger.Info("开始确保所有 Topics 存在",
		zap.Strings("主题列表", topics),
		zap.Int("主题数量", len(topics)))

	successCount := 0
	for _, topic := range topics {
		if err := ta.CreateCompactedTopic(ctx, topic); err != nil {
			ta.logger.Error("创建 Topic 失败", 
				zap.String("主题", topic),
				zap.Error(err))
			// 继续创建其他 topics
		} else {
			successCount++
		}
	}

	ta.logger.Info("Topic 确保完成",
		zap.Int("成功数量", successCount),
		zap.Int("总数", len(topics)))

	return nil
}

// Close 关闭 TopicAdmin
func (ta *TopicAdmin) Close() error {
	return ta.admin.Close()
}

func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *string {
	s := fmt.Sprintf("%d", i)
	return &s
}
