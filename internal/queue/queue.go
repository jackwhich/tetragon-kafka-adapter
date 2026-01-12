package queue

import (
	"context"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"go.uber.org/zap"
)

// Queue 内存队列（带背压策略）
type Queue struct {
	ch          chan *tetragon.GetEventsResponse
	config      *config.StreamConfig
	logger      *zap.Logger
	updateTicker *time.Ticker
	stopCh      chan struct{}
	// 使用原子操作减少锁竞争
	lastUpdateTime int64
}

const (
	// 指标更新间隔（减少锁竞争）
	metricsUpdateInterval = 100 * time.Millisecond
)

// NewQueue 创建新的队列
func NewQueue(cfg *config.StreamConfig, logger *zap.Logger) *Queue {
	metrics.QueueCapacity.Set(float64(cfg.MaxQueue))
	
	q := &Queue{
		ch:          make(chan *tetragon.GetEventsResponse, cfg.MaxQueue),
		config:      cfg,
		logger:      logger,
		updateTicker: time.NewTicker(metricsUpdateInterval),
		stopCh:      make(chan struct{}),
	}
	
	// 启动后台指标更新 goroutine
	go q.updateMetricsLoop()
	
	return q
}

// updateMetricsLoop 定期更新指标（减少锁竞争）
func (q *Queue) updateMetricsLoop() {
	for {
		select {
		case <-q.updateTicker.C:
			metrics.QueueDepth.Set(float64(len(q.ch)))
		case <-q.stopCh:
			return
		}
	}
}

// Push 推送事件到队列
func (q *Queue) Push(ctx context.Context, event *tetragon.GetEventsResponse) error {
	if q.config.DropIfQueueFull {
		// Drop 模式：队列满时丢弃
		select {
		case q.ch <- event:
			return nil
		default:
			metrics.DropsTotal.WithLabelValues("queue_full").Inc()
			return ErrQueueFull
		}
	} else {
		// Block 模式：队列满时阻塞
		select {
		case q.ch <- event:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Pop 从队列弹出事件
func (q *Queue) Pop(ctx context.Context) (*tetragon.GetEventsResponse, error) {
	select {
	case event := <-q.ch:
		return event, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close 关闭队列
func (q *Queue) Close() {
	q.updateTicker.Stop()
	close(q.stopCh)
	// 注意：updateMetricsLoop goroutine 会在检测到 stopCh 关闭后自动退出
	// 这里不需要等待，因为它是轻量级的指标更新操作
}

// Size 获取队列当前大小
func (q *Queue) Size() int {
	return len(q.ch)
}

// Capacity 获取队列容量
func (q *Queue) Capacity() int {
	return cap(q.ch)
}

var ErrQueueFull = &queueError{msg: "queue is full"}

type queueError struct {
	msg string
}

func (e *queueError) Error() string {
	return e.msg
}
