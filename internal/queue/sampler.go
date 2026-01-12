package queue

import (
	"math/rand"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"github.com/yourorg/tetragon-kafka-adapter/internal/router"
)

// Sampler 采样器
// 注意：rand.Rand 不是并发安全的，Sampler 应该在单 goroutine 中使用
// 如果需要多 goroutine 访问，应该使用 sync.Mutex 保护或每个 goroutine 使用独立的实例
type Sampler struct {
	config *config.StreamConfig
	rand   *rand.Rand
}

// NewSampler 创建新的采样器
func NewSampler(cfg *config.StreamConfig) *Sampler {
	return &Sampler{
		config: cfg,
		rand:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// ShouldSample 判断是否应该采样
func (s *Sampler) ShouldSample(event *tetragon.GetEventsResponse) bool {
	if s.config.SampleRatio >= 1.0 {
		return true
	}

	eventType := router.DetectEventType(event)
	
	// 随机采样
	if s.rand.Float64() < s.config.SampleRatio {
		return true
	}

	metrics.SampledTotal.WithLabelValues(eventType).Inc()
	return false
}
