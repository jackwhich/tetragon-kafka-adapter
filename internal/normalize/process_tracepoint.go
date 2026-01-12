package normalize

import (
	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
)

// normalizeProcessTracepoint 规范化 process_tracepoint 事件
func normalizeProcessTracepoint(event *tetragon.GetEventsResponse, schema *v1.EventSchema) (*v1.EventSchema, error) {
	processTracepoint := event.GetProcessTracepoint()
	if processTracepoint == nil {
		return schema, nil
	}

	proc := processTracepoint.Process
	if proc != nil {
		schema.Process = &v1.ProcessInfo{
			Binary: proc.GetBinary(),
		}
		
		// 安全处理可能为 nil 的字段
		if proc.GetPid() != nil {
			schema.Process.PID = proc.GetPid().GetValue()
		}
		if proc.GetUid() != nil {
			schema.Process.UID = proc.GetUid().GetValue()
		}
		argsStr := proc.GetArguments()
		if argsStr != "" {
			schema.Process.Args = []string{argsStr}
		}
		
		// 从 ProcessTracepoint 获取 Parent
		if parent := processTracepoint.GetParent(); parent != nil {
			if parent.GetPid() != nil {
				schema.Process.PPID = parent.GetPid().GetValue()
			}
		}

		if pod := proc.GetPod(); pod != nil {
			schema.K8s = &v1.K8sInfo{
				Namespace: pod.GetNamespace(),
				Pod:       pod.GetName(),
			}
			if container := pod.GetContainer(); container != nil {
				schema.K8s.Container = container.GetName()
			}
		}
	}

	// 设置 tracepoint 相关信息
	if schema.Extra == nil {
		schema.Extra = make(map[string]interface{})
	}
	if processTracepoint.Subsys != "" {
		schema.Extra["subsys"] = processTracepoint.Subsys
	}
	if processTracepoint.Event != "" {
		schema.Extra["event"] = processTracepoint.Event
	}

	// 设置标签
	if schema.Labels == nil {
		schema.Labels = make(map[string]string)
	}
	schema.Labels["source"] = "tetragon"
	schema.Labels["event_subtype"] = "tracepoint"

	return schema, nil
}
