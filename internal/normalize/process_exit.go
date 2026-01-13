package normalize

import (
	"github.com/cilium/tetragon/api/v1/tetragon"
	"github.com/yourorg/tetragon-kafka-adapter/internal/schema/v1"
)

// normalizeProcessExit 规范化 process_exit 事件
func normalizeProcessExit(event *tetragon.GetEventsResponse, schema *v1.EventSchema) (*v1.EventSchema, error) {
	processExit := event.GetProcessExit()
	if processExit == nil {
		return schema, nil
	}

	proc := processExit.Process
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
	
	// 保留原始 arguments 字符串（不要盲目拆分，避免语义混淆）
	argsStr := proc.GetArguments()
	if argsStr != "" {
		if schema.Extra == nil {
			schema.Extra = make(map[string]interface{})
		}
		schema.Extra["arguments_raw"] = argsStr
	}
	
	// 从 ProcessExit 获取 Parent
	if parent := processExit.GetParent(); parent != nil {
		if parent.GetPid() != nil {
			schema.Process.PPID = parent.GetPid().GetValue()
		}
		if parent.GetExecId() != "" {
			if schema.Extra == nil {
				schema.Extra = make(map[string]interface{})
			}
			schema.Extra["parent_exec_id"] = parent.GetExecId()
		}
	}
	
	// 抽取 exec_id、docker id 等便于索引（存到 Extra，不覆盖原始 Raw）
	if proc.GetExecId() != "" {
		if schema.Extra == nil {
			schema.Extra = make(map[string]interface{})
		}
		schema.Extra["exec_id"] = proc.GetExecId()
	}
	// docker 字段是字符串类型，不是对象
	if dockerID := proc.GetDocker(); dockerID != "" {
		if schema.Extra == nil {
			schema.Extra = make(map[string]interface{})
		}
		schema.Extra["docker_id"] = dockerID
	}

	// 设置 K8s 信息
	if pod := proc.GetPod(); pod != nil {
		schema.K8s = &v1.K8sInfo{
			Namespace: pod.GetNamespace(),
			Pod:       pod.GetName(),
		}
		if container := pod.GetContainer(); container != nil {
			schema.K8s.Container = container.GetName()
		}
	}

	// 设置退出码（如果有）
	if processExit.Signal != "" {
		if schema.Extra == nil {
			schema.Extra = make(map[string]interface{})
		}
		schema.Extra["signal"] = processExit.Signal
	}

	// 设置标签
	if schema.Labels == nil {
		schema.Labels = make(map[string]string)
	}
	schema.Labels["source"] = "tetragon"

	return schema, nil
}
