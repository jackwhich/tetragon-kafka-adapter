package normalize

import (
	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
)

// normalizeProcessKprobe 规范化 process_kprobe 事件
func normalizeProcessKprobe(event *tetragon.GetEventsResponse, schema *v1.EventSchema) (*v1.EventSchema, error) {
	processKprobe := event.GetProcessKprobe()
	if processKprobe == nil {
		return schema, nil
	}

	proc := processKprobe.Process
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
		
		// 从 ProcessKprobe 获取 Parent
		if parent := processKprobe.GetParent(); parent != nil {
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

	// 设置 kprobe 相关信息
	if schema.Extra == nil {
		schema.Extra = make(map[string]interface{})
	}
	if processKprobe.FunctionName != "" {
		schema.Extra["function_name"] = processKprobe.FunctionName
	}

	// 设置标签
	if schema.Labels == nil {
		schema.Labels = make(map[string]string)
	}
	schema.Labels["source"] = "tetragon"
	schema.Labels["event_subtype"] = "kprobe"

	return schema, nil
}
