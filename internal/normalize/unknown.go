package normalize

import (
	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
)

// normalizeUnknown 规范化未知事件（兜底处理）
func normalizeUnknown(_ *tetragon.GetEventsResponse, schema *v1.EventSchema) (*v1.EventSchema, error) {
	// 设置基本标签
	if schema.Labels == nil {
		schema.Labels = make(map[string]string)
	}
	schema.Labels["source"] = "tetragon"
	schema.Labels["event_subtype"] = "unknown"

	return schema, nil
}
