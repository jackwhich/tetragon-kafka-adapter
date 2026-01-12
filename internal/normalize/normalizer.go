// Package normalize 提供 Tetragon 事件的规范化功能
// 将 Tetragon gRPC 事件转换为统一的 JSON Schema 格式，并保留完整的原始事件数据
package normalize

import (
	"encoding/json"
	
	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"go.uber.org/zap"
)

// Normalizer 规范化接口
type Normalizer interface {
	Normalize(event *tetragon.GetEventsResponse) (*v1.EventSchema, error)
}

// EventNormalizer 事件规范化器
type EventNormalizer struct {
	logger *zap.Logger
}

// NewEventNormalizer 创建新的事件规范化器
func NewEventNormalizer(logger *zap.Logger) *EventNormalizer {
	return &EventNormalizer{
		logger: logger,
	}
}

// Normalize 规范化事件
func (n *EventNormalizer) Normalize(event *tetragon.GetEventsResponse) (*v1.EventSchema, error) {
	eventType := detectEventType(event)
	
	schema := v1.NewEventSchema(eventType)
	// 处理时间戳：从 *timestamppb.Timestamp 转换为 int64 (Unix 纳秒)
	if event.GetTime() != nil {
		timestamp := event.GetTime().AsTime().UnixNano()
		schema.SetTimestamp(timestamp)
	}
	schema.Node = getEventNode(event)
	
	// 将完整的原始事件序列化为 JSON 并放入 Raw 字段
	// 这样 Logstash 可以提取任何需要的信息，包括所有网络连接细节、Pod 信息等
	rawJSON, err := protojson.MarshalOptions{
		UseProtoNames: true, // 使用 snake_case 字段名，保持与 Tetragon 官方格式一致
		EmitUnpopulated: true, // 包含所有字段，即使为空
	}.Marshal(event)
	if err == nil {
		var rawMap map[string]interface{}
		if err := json.Unmarshal(rawJSON, &rawMap); err == nil {
			schema.Raw = rawMap
		} else {
			// 如果解析失败，直接使用 JSON 字符串
			schema.Raw = string(rawJSON)
		}
	}
	
	// 根据事件类型调用不同的规范化函数（提取常用字段，方便查询）
	switch eventType {
	case "process_exec":
		return normalizeProcessExec(event, schema)
	case "process_exit":
		return normalizeProcessExit(event, schema)
	case "process_kprobe":
		return normalizeProcessKprobe(event, schema)
	case "process_tracepoint":
		return normalizeProcessTracepoint(event, schema)
	default:
		return normalizeUnknown(event, schema)
	}
}

func detectEventType(event *tetragon.GetEventsResponse) string {
	switch {
	case event.GetProcessExec() != nil:
		return "process_exec"
	case event.GetProcessExit() != nil:
		return "process_exit"
	case event.GetProcessKprobe() != nil:
		return "process_kprobe"
	case event.GetProcessTracepoint() != nil:
		return "process_tracepoint"
	default:
		return "unknown"
	}
}

func getEventNode(event *tetragon.GetEventsResponse) string {
	nodeName := event.GetNodeName()
	if nodeName != "" {
		return nodeName
	}
	return "unknown"
}
