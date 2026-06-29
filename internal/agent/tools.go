package agent

import "context"

// ToolExecutor executes a named tool call and returns the result as a string.
type ToolExecutor interface {
	Execute(ctx context.Context, name, argsJSON string) (string, error)
}

// KubeTools returns the minimal set of tool definitions the agent can call.
// ponytail: fixed 3 tools (pod logs, events, node); extend if operators need more context sources.
func KubeTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "get_pod_logs",
				Description: "Fetch recent log lines from a pod container in the cluster.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"namespace": map[string]any{"type": "string", "description": "Pod namespace"},
						"pod":       map[string]any{"type": "string", "description": "Pod name"},
						"container": map[string]any{"type": "string", "description": "Container name (optional)"},
						"tail":      map[string]any{"type": "integer", "description": "Number of tail lines (default 100)"},
					},
					"required": []string{"namespace", "pod"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "get_events",
				Description: "List recent Kubernetes events for an object in a namespace.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"namespace":   map[string]any{"type": "string", "description": "Namespace to list events from"},
						"object_name": map[string]any{"type": "string", "description": "Involved object name filter (optional)"},
					},
					"required": []string{"namespace"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "get_node",
				Description: "Get conditions and capacity for a specific cluster node.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"node_name": map[string]any{"type": "string", "description": "Node name"},
					},
					"required": []string{"node_name"},
				},
			},
		},
	}
}
