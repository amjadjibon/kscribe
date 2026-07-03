package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/amjadjibon/kscribe/internal/enricher"
)

const (
	maxTailLines  int64 = 200
	defaultTail   int64 = 100
	maxLogBytes   int64 = 8 * 1024 // 8 KB
	maxEventCount       = 30
)

// KubeToolExecutor implements agent.ToolExecutor using live Kubernetes clients.
// SEC-001: all outputs are run through enricher.Redact before returning to the model.
type KubeToolExecutor struct {
	Client client.Client
	Kube   kubernetes.Interface
}

// Execute dispatches to the named tool handler.
func (e *KubeToolExecutor) Execute(ctx context.Context, name, argsJSON string) (string, error) {
	switch name {
	case "get_pod_logs":
		return e.getPodLogs(ctx, argsJSON)
	case "get_events":
		return e.getEvents(ctx, argsJSON)
	case "get_node":
		return e.getNode(ctx, argsJSON)
	default:
		return "", fmt.Errorf("unknown tool: %q", name)
	}
}

func (e *KubeToolExecutor) getPodLogs(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
		Container string `json:"container"`
		Tail      int64  `json:"tail"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	tail := args.Tail
	if tail <= 0 {
		tail = defaultTail
	}
	if tail > maxTailLines {
		tail = maxTailLines
	}
	req := e.Kube.CoreV1().Pods(args.Namespace).GetLogs(args.Pod, &corev1.PodLogOptions{
		Container: args.Container,
		TailLines: &tail,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("stream logs: %w", err)
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, maxLogBytes))
	if err != nil {
		return "", fmt.Errorf("read logs: %w", err)
	}
	// SEC-001: redact before handing to model.
	return enricher.Redact(string(data)), nil
}

func (e *KubeToolExecutor) getEvents(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Namespace  string `json:"namespace"`
		ObjectName string `json:"object_name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	var list corev1.EventList
	if err := e.Client.List(ctx, &list, client.InNamespace(args.Namespace)); err != nil {
		return "", fmt.Errorf("list events: %w", err)
	}
	var sb strings.Builder
	count := 0
	for _, ev := range list.Items {
		if args.ObjectName != "" && ev.InvolvedObject.Name != args.ObjectName {
			continue
		}
		if count >= maxEventCount {
			break
		}
		// SEC-001: redact event message.
		fmt.Fprintf(&sb, "[%s] %s (x%d): %s\n",
			ev.Reason, ev.InvolvedObject.Name, ev.Count, enricher.Redact(ev.Message))
		count++
	}
	return sb.String(), nil
}

func (e *KubeToolExecutor) getNode(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		NodeName string `json:"node_name"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	var node corev1.Node
	if err := e.Client.Get(ctx, client.ObjectKey{Name: args.NodeName}, &node); err != nil {
		return "", fmt.Errorf("get node: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("Conditions:\n")
	for _, c := range node.Status.Conditions {
		// SEC-001: redact condition message.
		fmt.Fprintf(&sb, "  %s=%s: %s\n", c.Type, c.Status, enricher.Redact(c.Message))
	}
	sb.WriteString("Capacity:\n")
	for r, q := range node.Status.Capacity {
		fmt.Fprintf(&sb, "  %s: %s\n", r, q.String())
	}
	return sb.String(), nil
}
