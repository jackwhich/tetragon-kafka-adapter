package kafka

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/config"
	"github.com/yourorg/tetragon-kafka-adapter/internal/router"
)

// GenerateDedupKey 生成用于去重的消息 Key（方案 1：Kafka Compacted Topic）
// 这个 Key 会用于 Kafka Compacted Topic，相同 Key 的消息会自动去重
// 性能优化：使用 strings.Builder 减少内存分配
func GenerateDedupKey(event *tetragon.GetEventsResponse, cfg *config.PartitionKeyConfig) string {
	var builder strings.Builder
	// 预分配容量（估算）
	builder.Grow(128)
	
	first := true
	// 根据配置的字段生成 Key
	for _, field := range cfg.Fields {
		value := extractFieldValue(event, field)
		if value != "" {
			if !first {
				builder.WriteString(cfg.Separator)
			}
			builder.WriteString(value)
			first = false
		}
	}

	key := builder.String()

	// 如果 Key 太长，使用 Hash
	if len(key) > 100 {
		h := sha256.New()
		h.Write([]byte(key))
		return hex.EncodeToString(h.Sum(nil))[:32]
	}

	return key
}

// extractFieldValue 从事件中提取字段值
func extractFieldValue(event *tetragon.GetEventsResponse, field string) string {
	parts := strings.Split(field, ".")
	
	switch parts[0] {
	case "node":
		return router.GetEventNode(event)
	case "type":
		return router.DetectEventType(event)
	case "process":
		if len(parts) > 1 {
			return extractProcessField(event, parts[1])
		}
	case "k8s":
		if len(parts) > 1 {
			return extractK8sField(event, parts[1])
		}
	case "timestamp":
		if event.GetTime() != nil {
			return fmt.Sprintf("%d", event.GetTime().AsTime().UnixNano())
		}
		return ""
	}
	
	return ""
}

// extractProcessField 提取进程字段
func extractProcessField(event *tetragon.GetEventsResponse, field string) string {
	var proc *tetragon.Process
	var parent *tetragon.Process
	
	switch {
	case event.GetProcessExec() != nil && event.GetProcessExec().Process != nil:
		proc = event.GetProcessExec().Process
		parent = event.GetProcessExec().GetParent()
	case event.GetProcessExit() != nil && event.GetProcessExit().Process != nil:
		proc = event.GetProcessExit().Process
		parent = event.GetProcessExit().GetParent()
	case event.GetProcessKprobe() != nil && event.GetProcessKprobe().Process != nil:
		proc = event.GetProcessKprobe().Process
		parent = event.GetProcessKprobe().GetParent()
	case event.GetProcessTracepoint() != nil && event.GetProcessTracepoint().Process != nil:
		proc = event.GetProcessTracepoint().Process
		parent = event.GetProcessTracepoint().GetParent()
	default:
		return ""
	}

	if proc == nil {
		return ""
	}

	switch field {
	case "pid":
		if proc.GetPid() != nil {
			return fmt.Sprintf("%d", proc.GetPid().GetValue())
		}
		return ""
	case "binary":
		return proc.GetBinary()
	case "ppid":
		if parent != nil && parent.GetPid() != nil {
			return fmt.Sprintf("%d", parent.GetPid().GetValue())
		}
		return ""
	default:
		return ""
	}
}

// extractK8sField 提取 K8s 字段
func extractK8sField(event *tetragon.GetEventsResponse, field string) string {
	var pod *tetragon.Pod
	
	switch {
	case event.GetProcessExec() != nil && event.GetProcessExec().Process != nil:
		pod = event.GetProcessExec().Process.GetPod()
	case event.GetProcessExit() != nil && event.GetProcessExit().Process != nil:
		pod = event.GetProcessExit().Process.GetPod()
	case event.GetProcessKprobe() != nil && event.GetProcessKprobe().Process != nil:
		pod = event.GetProcessKprobe().Process.GetPod()
	case event.GetProcessTracepoint() != nil && event.GetProcessTracepoint().Process != nil:
		pod = event.GetProcessTracepoint().Process.GetPod()
	default:
		return ""
	}

	if pod == nil {
		return ""
	}

	switch field {
	case "namespace":
		return pod.GetNamespace()
	case "pod":
		return pod.GetName()
	case "container":
		if container := pod.GetContainer(); container != nil {
			return container.GetName()
		}
		return ""
	default:
		return ""
	}
}
