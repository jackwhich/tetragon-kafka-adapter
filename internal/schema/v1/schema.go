package v1

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// EventSchema 稳定 JSON Schema v1
type EventSchema struct {
	SchemaVersion int                    `json:"schema_version"`
	Type          string                 `json:"type"`
	Timestamp     string                 `json:"ts"`
	TraceID       string                 `json:"trace_id,omitempty"` // 追踪 ID，用于分布式追踪
	Node          string                 `json:"node"`
	K8s           *K8sInfo               `json:"k8s,omitempty"`
	Process       *ProcessInfo           `json:"process,omitempty"`
	Network       *NetworkInfo           `json:"network,omitempty"` // 网络连接信息
	Labels        map[string]string      `json:"labels,omitempty"`
	Extra         map[string]interface{} `json:"extra,omitempty"`
}

// K8sInfo Kubernetes 信息
type K8sInfo struct {
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
}

// NetworkInfo 网络连接信息
type NetworkInfo struct {
	SourceIP      string `json:"source_ip,omitempty"`      // 源 IP 地址
	SourcePort    uint32 `json:"source_port,omitempty"`    // 源端口
	DestinationIP string `json:"destination_ip,omitempty"` // 目标 IP 地址
	DestinationPort uint32 `json:"destination_port,omitempty"` // 目标端口
	Protocol      string `json:"protocol,omitempty"`       // 协议 (TCP/UDP)
	Family        string `json:"family,omitempty"`         // 地址族 (IPv4/IPv6)
}

// ProcessInfo 进程信息
type ProcessInfo struct {
	PID     uint32   `json:"pid,omitempty"`
	PPID    uint32   `json:"ppid,omitempty"`
	UID     uint32   `json:"uid,omitempty"`
	GID     uint32   `json:"gid,omitempty"`
	Binary  string   `json:"binary,omitempty"`
	Args    []string `json:"args,omitempty"`
	CWD     string   `json:"cwd,omitempty"`
}

// NewEventSchema 创建新的事件 Schema
func NewEventSchema(eventType string) *EventSchema {
	return &EventSchema{
		SchemaVersion: 1,
		Type:          eventType,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		TraceID:       generateTraceID(),
		Labels:        make(map[string]string),
		Extra:         make(map[string]interface{}),
	}
}

// traceIDPool 用于复用字节数组，减少内存分配
var traceIDPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 16)
	},
}

// generateTraceID 生成 trace ID（16字节，32字符hex字符串）
// 性能优化：使用 sync.Pool 复用字节数组
func generateTraceID() string {
	b := traceIDPool.Get().([]byte)
	defer traceIDPool.Put(b)
	
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ToJSON 转换为 JSON 字节
func (e *EventSchema) ToJSON() ([]byte, error) {
	return json.Marshal(e)
}

// SetTimestamp 设置时间戳（从 protobuf timestamp）
func (e *EventSchema) SetTimestamp(ts int64) {
	if ts > 0 {
		t := time.Unix(0, ts)
		e.Timestamp = t.UTC().Format(time.RFC3339Nano)
	}
}
