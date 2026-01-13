// Package normalize 提供 Tetragon 事件的规范化功能
// 将 Tetragon gRPC 事件转换为统一的 JSON Schema 格式
package normalize

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/cilium/tetragon/api/v1/tetragon"
	v1 "github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
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
// 简化版本：只做 Protobuf 转 JSON，不提取任何字段，保留完整原始数据
func (n *EventNormalizer) Normalize(event *tetragon.GetEventsResponse) (*v1.EventSchema, error) {
	eventType := detectEventType(event)

	schema := v1.NewEventSchema(eventType)
	// 处理时间戳：从 *timestamppb.Timestamp 转换为 int64 (Unix 纳秒)
	if event.GetTime() != nil {
		timestamp := event.GetTime().AsTime().UnixNano()
		schema.SetTimestamp(timestamp)
	}
	schema.Node = getEventNode(event)

	// 直接将整个 Protobuf 事件转换为 JSON（原始数据保全）
	rawJSON, err := protojson.MarshalOptions{
		UseProtoNames: true,
	}.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal raw event: %w", err)
	}

	schema.Raw = json.RawMessage(rawJSON)
	schema.RawMeta = &v1.RawMeta{
		Format:    "json_map",
		SizeBytes: len(rawJSON),
		Redacted:  false,
	}

	// 不再提取任何字段，所有数据都在 Raw 字段中
	// Process, Network, K8s 等字段保持为 nil，由 Elasticsearch 从 Raw 中解析
	return schema, nil
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
