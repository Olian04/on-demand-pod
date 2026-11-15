package controller

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

const (
	// ManagedByLabel marks every Pod this operator creates.
	ManagedByLabel = "app.kubernetes.io/managed-by"
	// ManagedByValue is the value of ManagedByLabel.
	ManagedByValue = "on-demand-pod"
	// OnDemandPodLabel points from a Pod back at the CR that owns it.
	OnDemandPodLabel = "ondemand.olian04.github.io/name"

	containerName = "main"
)

// newPod builds the Pod for a CR. The Pod name equals the CR name, so no
// status field is needed to find it again.
//
// The security context is the Pod Security "restricted" profile. The cost is
// that images which run as root fail to start; that is intended and surfaces as
// a condition on the CR, never as a loosened context.
func newPod(odp *v1alpha1.OnDemandPod) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      odp.Name,
			Namespace: odp.Namespace,
			Labels: map[string]string{
				ManagedByLabel:   ManagedByValue,
				OnDemandPodLabel: odp.Name,
			},
		},
		Spec: corev1.PodSpec{
			// Never restart, so a crashed Pod stays visible as Failed.
			RestartPolicy: corev1.RestartPolicyNever,
			// The default ServiceAccount carries the image pull secrets, but the
			// workload itself gets no API credentials.
			AutomountServiceAccountToken: new(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   new(true),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:      containerName,
				Image:     odp.Spec.Image,
				Command:   odp.Spec.Command,
				Args:      odp.Spec.Args,
				Env:       odp.Spec.Env,
				Resources: odp.Spec.Resources,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: new(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
}
