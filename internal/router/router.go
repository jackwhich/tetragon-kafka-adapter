package router

import (
	"github.com/cilium/tetragon/api/v1/tetragon"
	"go.uber.org/zap"
)

// Router 路由决策器
type Router struct {
	topics map[string]string
	logger *zap.Logger
}

// NewRouter 创建新的 Router
func NewRouter(topics map[string]string, logger *zap.Logger) *Router {
	return &Router{
		topics: topics,
		logger: logger,
	}
}

// Route 路由事件到 Topic
func (r *Router) Route(event *tetragon.GetEventsResponse) string {
	eventType := DetectEventType(event)
	
	topic, ok := r.topics[eventType]
	if !ok {
		// 使用 unknown topic 作为兜底
		topic, ok = r.topics["unknown"]
		if !ok {
			r.logger.Warn("未找到事件类型对应的主题映射，且未配置 unknown 主题",
				zap.String("事件类型", eventType))
			return "tetragon.unknown"
		}
		r.logger.Debug("使用 unknown 主题作为兜底",
			zap.String("事件类型", eventType),
			zap.String("主题", topic))
	}
	
	return topic
}

// GetDLQTopic 获取 DLQ topic
func (r *Router) GetDLQTopic() string {
	if topic, ok := r.topics["dlq"]; ok {
		return topic
	}
	return "tetragon.dlq"
}
