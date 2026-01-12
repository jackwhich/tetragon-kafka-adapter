package normalize

import (
	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
)

// normalizeProcessExec 规范化 process_exec 事件
func normalizeProcessExec(event *tetragon.GetEventsResponse, schema *v1.EventSchema) (*v1.EventSchema, error) {
	processExec := event.GetProcessExec()
	if processExec == nil {
		return schema, nil
	}

	proc := processExec.Process
	if proc == nil {
		return schema, nil
	}

	// 设置进程信息
	schema.Process = &v1.ProcessInfo{
		Binary: proc.GetBinary(),
		CWD:    proc.GetCwd(),
	}
	
	// 安全处理可能为 nil 的字段
	if proc.GetPid() != nil {
		schema.Process.PID = proc.GetPid().GetValue()
	}
	if proc.GetUid() != nil {
		schema.Process.UID = proc.GetUid().GetValue()
	}
	// Arguments 是 string 类型，需要转换为 []string
	argsStr := proc.GetArguments()
	if argsStr != "" {
		// 简单分割，实际可能需要更复杂的解析
		schema.Process.Args = []string{argsStr}
	}
	
	// 从 ProcessExec 获取 Parent
	if parent := processExec.GetParent(); parent != nil {
		if parent.GetPid() != nil {
			schema.Process.PPID = parent.GetPid().GetValue()
		}
	}
	
	// GID 可能在 ProcessCredentials 中，暂时跳过
	// if proc.GetCredentials() != nil && proc.GetCredentials().GetGid() != nil {
	// 	schema.Process.GID = proc.GetCredentials().GetGid().GetValue()
	// }

	// 设置 K8s 信息
	if pod := proc.GetPod(); pod != nil {
		schema.K8s = &v1.K8sInfo{
			Namespace: pod.GetNamespace(),
			Pod:       pod.GetName(),
		}
		// Container 是 *Container 类型，需要获取其名称
		if container := pod.GetContainer(); container != nil {
			schema.K8s.Container = container.GetName()
		}
	}

	// 设置标签
	if schema.Labels == nil {
		schema.Labels = make(map[string]string)
	}
	schema.Labels["source"] = "tetragon"

	return schema, nil
}
