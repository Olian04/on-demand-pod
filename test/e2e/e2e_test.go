//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
	"github.com/Olian04/on-demand-pod/test/utils"
)

// The whole point of the operator: a pod that runs, then disappears with its
// CR at expiresAt, with owner-reference garbage collection removing the Pod.
func TestLeaseRunsThenExpires(t *testing.T) {
	const name = "e2e-lease"
	create(t, name, nonRootImage, "45s")

	eventually(t, 2*time.Minute, "Ready", func() (bool, string) {
		var odp v1alpha1.OnDemandPod
		if err := k8s.Get(context.Background(), key(name), &odp); err != nil {
			return false, err.Error()
		}
		return meta.IsStatusConditionTrue(odp.Status.Conditions, "Ready"), fmt.Sprintf("%+v", odp.Status)
	})

	var odp v1alpha1.OnDemandPod
	if err := k8s.Get(context.Background(), key(name), &odp); err != nil {
		t.Fatal(err)
	}
	if odp.Status.Phase != "Running" {
		t.Errorf("phase = %q, want Running", odp.Status.Phase)
	}
	var pod corev1.Pod
	if err := k8s.Get(context.Background(), key(name), &pod); err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(&pod, &odp) {
		t.Error("pod is not owned by the CR")
	}

	eventually(t, 90*time.Second, "CR deleted at expiry", func() (bool, string) {
		return isGone(&v1alpha1.OnDemandPod{}, name), "CR still present"
	})
	eventually(t, 90*time.Second, "pod garbage-collected", func() (bool, string) {
		return isGone(&corev1.Pod{}, name), "pod still present"
	})
}

// Fact 2 of the design: the restricted security context must make a root image
// fail loudly and readably, and the operator must not loosen it to compensate.
func TestRootImageFailsWithReadableCondition(t *testing.T) {
	const name = "e2e-root"
	create(t, name, rootImage, "5m")

	const want = "CreateContainerConfigError"
	eventually(t, 3*time.Minute, "Ready reason "+want, func() (bool, string) {
		var odp v1alpha1.OnDemandPod
		if err := k8s.Get(context.Background(), key(name), &odp); err != nil {
			return false, err.Error()
		}
		c := meta.FindStatusCondition(odp.Status.Conditions, "Ready")
		if c == nil {
			return false, "no Ready condition yet"
		}
		return c.Status == metav1.ConditionFalse && c.Reason == want, fmt.Sprintf("%+v", *c)
	})

	var odp v1alpha1.OnDemandPod
	if err := k8s.Get(context.Background(), key(name), &odp); err != nil {
		t.Fatal(err)
	}
	if odp.Status.Phase != "Pending" {
		t.Errorf("phase = %q, want Pending", odp.Status.Phase)
	}
	c := meta.FindStatusCondition(odp.Status.Conditions, "Ready")
	if !strings.Contains(strings.ToLower(c.Message), "non-root") {
		t.Errorf("message %q does not explain the non-root failure", c.Message)
	}
	var pod corev1.Pod
	if err := k8s.Get(context.Background(), key(name), &pod); err != nil {
		t.Fatal(err)
	}
	if rn := pod.Spec.SecurityContext.RunAsNonRoot; rn == nil || !*rn {
		t.Error("security context was loosened")
	}
}

// The plugin must do through the CLI exactly what the CR operations do.
func TestPluginCommands(t *testing.T) {
	const name = "e2e-plugin"
	const bin = "bin/kubectl-ondemand"
	run := func(args ...string) string {
		t.Helper()
		out, err := utils.Run(exec.Command(bin, args...))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	run("run", nonRootImage, "--name", name, "--ttl", "10m")
	t.Cleanup(func() { _, _ = utils.Run(exec.Command(bin, "rm", name)) })

	if out := run("ls"); !strings.Contains(out, name) {
		t.Errorf("ls does not list %s:\n%s", name, out)
	}

	run("extend", name, "30m")
	var odp v1alpha1.OnDemandPod
	if err := k8s.Get(context.Background(), key(name), &odp); err != nil {
		t.Fatal(err)
	}
	if odp.Spec.TTL == nil || odp.Spec.TTL.Duration != 30*time.Minute {
		t.Errorf("spec.ttl = %v, want 30m", odp.Spec.TTL)
	}

	run("rm", name)
	eventually(t, time.Minute, "CR removed", func() (bool, string) {
		return isGone(&v1alpha1.OnDemandPod{}, name), "CR still present"
	})
}
