// Package grpc 提供 gRPC 流管理功能
// 用于从 Tetragon 服务读取事件流
package grpc

import (
	"context"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"go.uber.org/zap"
)

// Stream gRPC 流管理器
type Stream struct {
	client         *Client
	config         *config.Config
	logger         *zap.Logger
	lastDropLogTime time.Time // 用于限流日志，避免日志洪水
}

// NewStream 创建新的 Stream
func NewStream(client *Client, cfg *config.Config, logger *zap.Logger) *Stream {
	return &Stream{
		client: client,
		config: cfg,
		logger: logger,
	}
}

// ReadEvents 读取事件流
func (s *Stream) ReadEvents(ctx context.Context, eventCh chan<- *tetragon.GetEventsResponse) error {
	stream, err := s.client.GetEventsClient(ctx)
	if err != nil {
		metrics.GrpcErrorsTotal.WithLabelValues("get_events_client").Inc()
		s.logger.Error("获取 gRPC 事件客户端失败", zap.Error(err))
		return err
	}

	// 确保 stream 在函数退出时被正确关闭，避免资源泄漏
	defer func() {
		if stream != nil {
			if closeErr := stream.CloseSend(); closeErr != nil {
				s.logger.Debug("关闭 gRPC stream 时出错", zap.Error(closeErr))
			}
		}
	}()

	s.logger.Info("gRPC 事件流已建立")
	metrics.GrpcStreamUptimeSeconds.Set(float64(time.Now().Unix()))
	metrics.GrpcConnectionStatus.Set(1) // 连接已建立
	streamStartTime := time.Now()

	for {
		// 性能优化：先检查 context 是否已取消，避免不必要的 Recv 调用
		select {
		case <-ctx.Done():
			s.logger.Info("gRPC 事件流上下文已取消", zap.Error(ctx.Err()))
			return ctx.Err()
		default:
		}

		event, err := stream.Recv()
		if err != nil {
			metrics.GrpcErrorsTotal.WithLabelValues("recv").Inc()
			metrics.GrpcConnectionStatus.Set(0) // 连接断开
			s.logger.Error("从流接收事件失败",
				zap.Duration("流运行时长", time.Since(streamStartTime)),
				zap.Error(err))
			return err
		}

		metrics.GrpcEventsReceivedTotal.Inc()
		metrics.GrpcStreamUptimeSeconds.Set(time.Since(streamStartTime).Seconds())

		// 性能优化：使用非阻塞发送，如果 channel 满则记录警告
		// 优化：限制日志频率，避免日志洪水
		select {
		case eventCh <- event:
		case <-ctx.Done():
			s.logger.Info("gRPC 事件流上下文已取消", zap.Error(ctx.Err()))
			return ctx.Err()
		default:
			// Channel 已满，记录警告和指标但继续处理（由队列层处理背压）
			metrics.DropsTotal.WithLabelValues("stream_channel_full").Inc()
			// 优化：使用限流日志，避免日志洪水（每 10 秒最多记录一次）
			// 注意：这里使用简单的限流，如果需要更精确的限流可以使用更复杂的机制
			now := time.Now()
			if s.lastDropLogTime.IsZero() || now.Sub(s.lastDropLogTime) > 10*time.Second {
				s.logger.Warn("事件通道已满，可能影响性能",
					zap.Int("通道容量", cap(eventCh)),
					zap.Int("通道当前长度", len(eventCh)))
				s.lastDropLogTime = now
			}
		}
	}
}
