package normalize

import (
	"encoding/base64"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
	"google.golang.org/protobuf/proto"
)

// normalizeUnknown 规范化未知事件（兜底处理）
func normalizeUnknown(event *tetragon.GetEventsResponse, schema *v1.EventSchema) (*v1.EventSchema, error) {
	// 尝试序列化原始 protobuf 作为 raw 字段
	if data, err := proto.Marshal(event); err == nil {
		// 使用 base64 编码
		schema.Raw = base64.StdEncoding.EncodeToString(data)
	}

	// 设置基本标签
	if schema.Labels == nil {
		schema.Labels = make(map[string]string)
	}
	schema.Labels["source"] = "tetragon"
	schema.Labels["event_subtype"] = "unknown"

	return schema, nil
}
