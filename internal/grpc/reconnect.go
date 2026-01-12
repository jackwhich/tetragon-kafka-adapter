// Package grpc 提供 gRPC 重连管理功能
// 实现自动重连机制，确保与 Tetragon 服务的连接稳定性
package grpc

import (
	"context"
	"math/rand"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/metrics"
	"go.uber.org/zap"
)

// ReconnectManager 重连管理器
type ReconnectManager struct {
	client *Client
	config *config.Config
	logger *zap.Logger
}

// NewReconnectManager 创建新的重连管理器
func NewReconnectManager(client *Client, cfg *config.Config, logger *zap.Logger) *ReconnectManager {
	return &ReconnectManager{
		client: client,
		config: cfg,
		logger: logger,
	}
}

// RunWithReconnect 带重连的运行
func (rm *ReconnectManager) RunWithReconnect(ctx context.Context, eventCh chan<- *tetragon.GetEventsResponse) {
	attempt := 0
	maxAttempts := rm.config.Tetragon.Stream.Reconnect.MaxAttempts
	lastReconnectTime := time.Now()

	for {
		// 检查是否应该退出
		select {
		case <-ctx.Done():
			rm.logger.Info("重连管理器收到退出信号，停止重连")
			return
		default:
		}

		// 优化：检查客户端是否有效，避免使用 nil 客户端
		if rm.client == nil {
			rm.logger.Error("客户端未初始化，无法创建流")
			// 继续重连循环，等待下次重连
		} else {
			stream := NewStream(rm.client, rm.config, rm.logger)
			err := stream.ReadEvents(ctx, eventCh)
			if err != nil {
				metrics.GrpcConnectionStatus.Set(0) // 连接断开
				rm.logger.Error("流错误，将重连",
					zap.Error(err),
					zap.Int("尝试次数", attempt))
			} else {
				// 如果 ReadEvents 正常返回（非错误），说明是正常关闭
				metrics.GrpcConnectionStatus.Set(0) // 正常关闭
				rm.logger.Info("gRPC 事件流正常关闭")
				return
			}
		}

		// 再次检查是否应该退出
		select {
		case <-ctx.Done():
			rm.logger.Info("重连管理器收到退出信号，停止重连")
			return
		default:
		}

		// 检查重连次数限制
		if maxAttempts > 0 && attempt >= maxAttempts {
			rm.logger.Error("达到最大重连次数，停止重连",
				zap.Int("最大次数", maxAttempts),
				zap.Int("当前次数", attempt))
			return
		}

		// 指数退避重连
		backoff := rm.config.GetReconnectBackoff(attempt)
		if rm.config.Tetragon.Stream.Reconnect.Jitter {
			// 添加 jitter（使用独立的随机数生成器以保持一致）
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			jitter := time.Duration(rng.Intn(1000)) * time.Millisecond
			backoff += jitter
		}

		rm.logger.Info("正在重连...",
			zap.Int("尝试次数", attempt),
			zap.Duration("退避时间", backoff))

		metrics.GrpcReconnectTotal.Inc()
		metrics.GrpcStreamUptimeSeconds.Set(0)
		
		// 记录重连间隔
		if attempt > 0 {
			reconnectInterval := time.Since(lastReconnectTime).Seconds()
			metrics.GrpcReconnectIntervalSeconds.Observe(reconnectInterval)
		}
		lastReconnectTime = time.Now()

		// 性能优化：先关闭旧连接，再创建新连接，避免资源泄漏
		if rm.client != nil {
			if closeErr := rm.client.Close(); closeErr != nil {
				rm.logger.Warn("关闭旧客户端连接时出错", zap.Error(closeErr))
			}
		}

		newClient, newErr := NewClient(&rm.config.Tetragon)
		if newErr != nil {
			rm.logger.Error("重新创建客户端失败",
				zap.String("gRPC地址", rm.config.Tetragon.GRPCAddr),
				zap.Error(newErr))
			// 优化：如果创建失败，保持旧客户端为 nil，下次重连时会再次尝试
			// 注意：旧客户端已经关闭，所以这里不需要额外处理
			rm.client = nil
		} else {
			rm.client = newClient
			rm.logger.Info("客户端重新创建成功",
				zap.String("gRPC地址", rm.config.Tetragon.GRPCAddr))
		}

		// 使用 context 超时控制 sleep，支持优雅关闭
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			// 确保 timer 停止，避免泄漏
			if !timer.Stop() {
				// Timer 已经触发，需要排空 channel
				<-timer.C
			}
			rm.logger.Info("重连管理器收到退出信号，停止重连")
			return
		case <-timer.C:
			// 继续重连
		}
		attempt++
	}
}
