package router

import (
	"github.com/cilium/tetragon/api/v1/tetragon"
)

// DetectEventType 检测事件类型
func DetectEventType(event *tetragon.GetEventsResponse) string {
	switch {
	case event.GetProcessExec() != nil:
		return "process_exec"
	case event.GetProcessExit() != nil:
		return "process_exit"
	case event.GetProcessKprobe() != nil:
		return "process_kprobe"
	case event.GetProcessTracepoint() != nil:
		return "process_tracepoint"
	// DNS 和 Connect 事件可能使用不同的方法名，暂时跳过
	// case event.GetProcessDns() != nil:
	// 	return "process_dns"
	// case event.GetProcessConnect() != nil:
	// 	return "process_connect"
	case event.GetTest() != nil:
		return "test"
	default:
		return "unknown"
	}
}

// GetEventNode 获取事件节点名称
func GetEventNode(event *tetragon.GetEventsResponse) string {
	nodeName := event.GetNodeName()
	if nodeName != "" {
		return nodeName
	}
	return "unknown"
}
