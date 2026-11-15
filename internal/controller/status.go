package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types and reasons set on an OnDemandPod.
const (
	// ConditionReady mirrors the Pod's Ready condition.
	ConditionReady = "Ready"

	// ReasonNameConflict marks a CR whose name is taken by a Pod it does not own.
	ReasonNameConflict = "NameConflict"
	// ReasonPodReady is the Ready reason while the Pod is ready.
	ReasonPodReady = "PodReady"
	// ReasonPodNotReady is the fallback Ready reason when the Pod gives none.
	ReasonPodNotReady = "PodNotReady"
)

// phaseOf maps a Pod phase to a CR phase. The CR enum has no Unknown, which the
// kubelet no longer reports; treating it as Pending avoids inventing a state.
func phaseOf(pod *corev1.Pod) string {
	switch pod.Status.Phase {
	case corev1.PodRunning:
		return string(corev1.PodRunning)
	case corev1.PodSucceeded:
		return string(corev1.PodSucceeded)
	case corev1.PodFailed:
		return string(corev1.PodFailed)
	default:
		return string(corev1.PodPending)
	}
}

// readyCondition mirrors Pod readiness. When the Pod is not ready the reason is
// the most specific cause the Pod exposes, because the Pod's own Ready reason
// is usually the unhelpful "ContainersNotReady". That specific reason is where
// image pull errors, scheduling failures and config errors such as
// CreateContainerConfigError become visible on the CR.
func readyCondition(pod *corev1.Pod) metav1.Condition {
	if cond := podCondition(pod, corev1.PodReady); cond != nil && cond.Status == corev1.ConditionTrue {
		return metav1.Condition{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonPodReady}
	}
	reason, message := notReadyCause(pod)
	return metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: message}
}

func notReadyCause(pod *corev1.Pod) (reason, message string) {
	// Eviction and deadline kills are reported on the Pod, not its containers.
	if pod.Status.Reason != "" {
		return pod.Status.Reason, pod.Status.Message
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			return w.Reason, w.Message
		}
		if t := cs.State.Terminated; t != nil && t.Reason != "" {
			return t.Reason, t.Message
		}
	}
	if cond := podCondition(pod, corev1.PodScheduled); cond != nil && cond.Status == corev1.ConditionFalse && cond.Reason != "" {
		return cond.Reason, cond.Message
	}
	if cond := podCondition(pod, corev1.PodReady); cond != nil && cond.Reason != "" {
		return cond.Reason, cond.Message
	}
	return ReasonPodNotReady, ""
}

func podCondition(pod *corev1.Pod, t corev1.PodConditionType) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == t {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

func conflictCondition() metav1.Condition {
	return metav1.Condition{
		Type:    ConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  ReasonNameConflict,
		Message: "a Pod with this name exists and is not owned by this OnDemandPod",
	}
}
