package enricher

import (
	"context"
	"fmt"
	"io"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultTailLines int64 = 100

// maxPodsPerWorkload caps how many pods we fetch logs for when the involved
// object is a Deployment or ReplicaSet.
// ponytail: hard-coded to 3; make it a BuildSnapshot param if operators need tuning
const maxPodsPerWorkload = 3

// ObjectRef is the involved object from a Kubernetes event, carried into BuildSnapshot.
type ObjectRef struct {
	Kind      string
	Namespace string
	Name      string
	EventUID  string
	Reason    string
	Message   string
}

// BuildSnapshot collects Kubernetes context for the diagnosed event.
// Collection failures are recorded in Snapshot.Partial and do not abort the build (REQ-004).
// c is used for all object reads; kcs is used only for pod log streaming
// (controller-runtime client.Client cannot fetch logs).
func BuildSnapshot(ctx context.Context, c client.Client, kcs kubernetes.Interface, ref ObjectRef, tailLines int64) (*Snapshot, error) {
	if tailLines <= 0 {
		tailLines = defaultTailLines
	}
	s := &Snapshot{
		EventUID:   ref.EventUID,
		Reason:     ref.Reason,
		Message:    ref.Message,
		Namespace:  ref.Namespace,
		ObjectKind: ref.Kind,
		ObjectName: ref.Name,
	}

	collectRelatedEvents(ctx, c, s, ref)

	switch {
	case strings.EqualFold(ref.Kind, "Pod"):
		collectSinglePod(ctx, c, kcs, s, ref.Namespace, ref.Name, tailLines)
	case strings.EqualFold(ref.Kind, "Deployment"):
		collectDeployment(ctx, c, kcs, s, ref.Namespace, ref.Name, tailLines)
	case strings.EqualFold(ref.Kind, "ReplicaSet"):
		collectReplicaSet(ctx, c, kcs, s, ref.Namespace, ref.Name, tailLines)
	default:
		s.Partial = append(s.Partial, fmt.Sprintf("pod context: unsupported object kind %q", ref.Kind))
	}

	return s, nil
}

// collectRelatedEvents lists events in the namespace and keeps those that
// reference the same involved object.
func collectRelatedEvents(ctx context.Context, c client.Client, s *Snapshot, ref ObjectRef) {
	var list corev1.EventList
	if err := c.List(ctx, &list, client.InNamespace(ref.Namespace)); err != nil {
		s.Partial = append(s.Partial, fmt.Sprintf("related events: %v", err))
		return
	}
	for _, ev := range list.Items {
		if ev.InvolvedObject.Name != ref.Name {
			continue
		}
		s.RelatedEvents = append(s.RelatedEvents, EventSummary{
			Name:    ev.Name,
			Reason:  ev.Reason,
			Message: ev.Message,
			Count:   ev.Count,
		})
	}
}

func collectSinglePod(ctx context.Context, c client.Client, kcs kubernetes.Interface, s *Snapshot, ns, name string, tail int64) {
	var pod corev1.Pod
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pod); err != nil {
		s.Partial = append(s.Partial, fmt.Sprintf("pod %s/%s: %v", ns, name, err))
		return
	}
	pc := podContextFrom(&pod)
	fetchLogsIntoPodContext(ctx, kcs, s, &pc, ns, pod.Name, pod.Spec.Containers, tail)
	s.PodContexts = append(s.PodContexts, pc)
	if pod.Spec.NodeName != "" {
		collectNodeConditions(ctx, c, s, pod.Spec.NodeName)
	}
}

func collectDeployment(ctx context.Context, c client.Client, kcs kubernetes.Interface, s *Snapshot, ns, name string, tail int64) {
	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &dep); err != nil {
		s.Partial = append(s.Partial, fmt.Sprintf("deployment %s/%s: %v", ns, name, err))
	} else {
		ds := &DeploymentStatus{
			Name:              dep.Name,
			Replicas:          dep.Status.Replicas,
			ReadyReplicas:     dep.Status.ReadyReplicas,
			AvailableReplicas: dep.Status.AvailableReplicas,
		}
		for _, cond := range dep.Status.Conditions {
			ds.Conditions = append(ds.Conditions,
				fmt.Sprintf("%s=%s: %s", cond.Type, cond.Status, cond.Message))
		}
		s.DeploymentStatus = ds
		if dep.Spec.Selector != nil {
			collectPodsForSelector(ctx, c, kcs, s, ns, dep.Spec.Selector, tail)
		}
	}
}

func collectReplicaSet(ctx context.Context, c client.Client, kcs kubernetes.Interface, s *Snapshot, ns, name string, tail int64) {
	var rs appsv1.ReplicaSet
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &rs); err != nil {
		s.Partial = append(s.Partial, fmt.Sprintf("replicaset %s/%s: %v", ns, name, err))
	} else {
		rss := &ReplicaSetStatus{
			Name:          rs.Name,
			Replicas:      rs.Status.Replicas,
			ReadyReplicas: rs.Status.ReadyReplicas,
		}
		for _, cond := range rs.Status.Conditions {
			rss.Conditions = append(rss.Conditions,
				fmt.Sprintf("%s=%s: %s", cond.Type, cond.Status, cond.Message))
		}
		s.ReplicaSetStatus = rss
		if rs.Spec.Selector != nil {
			collectPodsForSelector(ctx, c, kcs, s, ns, rs.Spec.Selector, tail)
		}
	}
}

func collectPodsForSelector(ctx context.Context, c client.Client, kcs kubernetes.Interface, s *Snapshot, ns string, sel *metav1.LabelSelector, tail int64) {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		s.Partial = append(s.Partial, fmt.Sprintf("label selector: %v", err))
		return
	}
	var podList corev1.PodList
	if err := c.List(ctx, &podList, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		s.Partial = append(s.Partial, fmt.Sprintf("pods for selector: %v", err))
		return
	}
	for i := range podList.Items {
		if i >= maxPodsPerWorkload {
			break
		}
		pod := &podList.Items[i]
		pc := podContextFrom(pod)
		fetchLogsIntoPodContext(ctx, kcs, s, &pc, ns, pod.Name, pod.Spec.Containers, tail)
		s.PodContexts = append(s.PodContexts, pc)
		if pod.Spec.NodeName != "" {
			collectNodeConditions(ctx, c, s, pod.Spec.NodeName)
		}
	}
}

func collectNodeConditions(ctx context.Context, c client.Client, s *Snapshot, nodeName string) {
	var node corev1.Node
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		s.Partial = append(s.Partial, fmt.Sprintf("node %s: %v", nodeName, err))
		return
	}
	for _, cond := range node.Status.Conditions {
		s.NodeConditions = append(s.NodeConditions, NodeCondition{
			NodeName: nodeName,
			Type:     string(cond.Type),
			Status:   string(cond.Status),
			Message:  cond.Message,
		})
	}
}

// podContextFrom extracts static pod context from the pod spec/status.
// EnvVars with ValueFrom are recorded as RedactedPlaceholder since the value
// is not available without resolving secrets/configmaps.
func podContextFrom(pod *corev1.Pod) PodContext {
	pc := PodContext{
		PodName:     pod.Name,
		NodeName:    pod.Spec.NodeName,
		Phase:       string(pod.Status.Phase),
		Annotations: pod.Annotations,
	}
	for _, c := range pod.Spec.Containers {
		for _, e := range c.Env {
			val := e.Value
			if e.ValueFrom != nil {
				val = RedactedPlaceholder + " (from valueFrom)"
			}
			pc.EnvVars = append(pc.EnvVars, EnvVar{Name: e.Name, Value: val})
		}
	}
	return pc
}

// fetchLogsIntoPodContext fetches tail-N log lines for each container.
// Failures are recorded in s.Partial and do not abort the snapshot build.
func fetchLogsIntoPodContext(ctx context.Context, kcs kubernetes.Interface, s *Snapshot, pc *PodContext, ns, podName string, containers []corev1.Container, tail int64) {
	for _, c := range containers {
		data, err := streamPodLogs(ctx, kcs, ns, podName, c.Name, tail)
		if err != nil {
			s.Partial = append(s.Partial, fmt.Sprintf("logs %s/%s[%s]: %v", ns, podName, c.Name, err))
			continue
		}
		pc.Logs = append(pc.Logs, PodLog{
			ContainerName: c.Name,
			Lines:         string(data),
		})
	}
}

// streamPodLogs wraps the REST log stream call with a panic recovery so that
// fake or misbehaving clients do not crash the snapshot build.
func streamPodLogs(ctx context.Context, kcs kubernetes.Interface, ns, podName, container string, tail int64) (data []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("log stream panic: %v", r)
		}
	}()
	req := kcs.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Container: container,
		TailLines: &tail,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return io.ReadAll(stream)
}
