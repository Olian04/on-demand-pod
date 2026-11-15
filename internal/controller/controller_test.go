package controller_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
	"github.com/Olian04/on-demand-pod/internal/controller"
	"github.com/Olian04/on-demand-pod/internal/testenv"
)

var k8s client.Client

func TestMain(m *testing.M) {
	env, cfg, err := testenv.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := 1
	defer func() { _ = env.Stop(); os.Exit(code) }()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if err := (&controller.OnDemandPodReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	code = m.Run()
}

var seq atomic.Int64

func newCR(t *testing.T, mutate func(*v1alpha1.OnDemandPod)) *v1alpha1.OnDemandPod {
	t.Helper()
	odp := &v1alpha1.OnDemandPod{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("t-%d", seq.Add(1)), Namespace: "default"},
		Spec:       v1alpha1.OnDemandPodSpec{Image: "busybox:1", Command: []string{"sleep"}, Args: []string{"1"}},
	}
	if mutate != nil {
		mutate(odp)
	}
	return odp
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func getPod(odp *v1alpha1.OnDemandPod) (*corev1.Pod, bool) {
	var pod corev1.Pod
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(odp), &pod); err != nil {
		return nil, false
	}
	return &pod, true
}

func awaitPod(t *testing.T, odp *v1alpha1.OnDemandPod) *corev1.Pod {
	t.Helper()
	var pod *corev1.Pod
	eventually(t, "pod creation", func() bool { var ok bool; pod, ok = getPod(odp); return ok })
	return pod
}

func awaitStatus(t *testing.T, odp *v1alpha1.OnDemandPod, what string, ok func(*v1alpha1.OnDemandPod) bool) *v1alpha1.OnDemandPod {
	t.Helper()
	var cur v1alpha1.OnDemandPod
	eventually(t, what, func() bool {
		if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(odp), &cur); err != nil {
			return false
		}
		return ok(&cur)
	})
	return &cur
}

func TestCreatesHardenedOwnedPod(t *testing.T) {
	ctx := context.Background()
	odp := newCR(t, func(o *v1alpha1.OnDemandPod) {
		o.Spec.Env = []corev1.EnvVar{{Name: "A", Value: "1"}}
	})
	if err := k8s.Create(ctx, odp); err != nil {
		t.Fatal(err)
	}
	pod := awaitPod(t, odp)

	if pod.Name != odp.Name {
		t.Errorf("pod name %q, want CR name", pod.Name)
	}
	if !metav1.IsControlledBy(pod, odp) {
		t.Error("pod is not controlled by the CR")
	}
	if pod.Labels[controller.ManagedByLabel] != controller.ManagedByValue || pod.Labels[controller.OnDemandPodLabel] != odp.Name {
		t.Errorf("labels = %v", pod.Labels)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q", pod.Spec.RestartPolicy)
	}
	if v := pod.Spec.AutomountServiceAccountToken; v == nil || *v {
		t.Error("service account token is automounted")
	}
	psc := pod.Spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("runAsNonRoot is not true")
	}
	if psc == nil || psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("seccomp profile is not RuntimeDefault")
	}
	c := pod.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("privilege escalation allowed")
	}
	if c.SecurityContext == nil || c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Error("capabilities are not dropped")
	}
	if c.Image != "busybox:1" || len(c.Command) != 1 || len(c.Args) != 1 || len(c.Env) != 1 {
		t.Errorf("container did not carry the spec: %+v", c)
	}
}

func TestMirrorsPodPhaseAndReadiness(t *testing.T) {
	ctx := context.Background()
	odp := newCR(t, nil)
	if err := k8s.Create(ctx, odp); err != nil {
		t.Fatal(err)
	}
	pod := awaitPod(t, odp)

	pending := awaitStatus(t, odp, "Pending status", func(o *v1alpha1.OnDemandPod) bool { return o.Status.Phase == "Pending" })
	if pending.Status.ObservedGeneration != pending.Generation {
		t.Errorf("observedGeneration = %d, want %d", pending.Status.ObservedGeneration, pending.Generation)
	}

	// envtest has no kubelet, so the test plays the kubelet.
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	running := awaitStatus(t, odp, "Running and Ready", func(o *v1alpha1.OnDemandPod) bool {
		return o.Status.Phase == "Running" && meta.IsStatusConditionTrue(o.Status.Conditions, "Ready")
	})
	_ = running

	pod.Status.Phase = corev1.PodFailed
	pod.Status.Conditions = nil
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "main",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}},
	}}
	if err := k8s.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	failed := awaitStatus(t, odp, "Failed", func(o *v1alpha1.OnDemandPod) bool { return o.Status.Phase == "Failed" })
	if c := meta.FindStatusCondition(failed.Status.Conditions, "Ready"); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "Error" {
		t.Errorf("Ready condition = %+v", c)
	}
}

func TestSurfacesWaitingReasonOnReadyCondition(t *testing.T) {
	ctx := context.Background()
	for _, reason := range []string{"ImagePullBackOff", "CreateContainerConfigError"} {
		t.Run(reason, func(t *testing.T) {
			odp := newCR(t, nil)
			if err := k8s.Create(ctx, odp); err != nil {
				t.Fatal(err)
			}
			pod := awaitPod(t, odp)
			pod.Status.Phase = corev1.PodPending
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:  "main",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "boom"}},
			}}
			if err := k8s.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			cur := awaitStatus(t, odp, reason, func(o *v1alpha1.OnDemandPod) bool {
				c := meta.FindStatusCondition(o.Status.Conditions, "Ready")
				return c != nil && c.Reason == reason
			})
			if cur.Status.Phase != "Pending" {
				t.Errorf("phase = %q, want Pending", cur.Status.Phase)
			}
		})
	}
}

func TestNameConflictLeavesForeignPodAlone(t *testing.T) {
	ctx := context.Background()
	odp := newCR(t, nil)
	foreign := foreignPod(odp)
	if err := k8s.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(ctx, odp); err != nil {
		t.Fatal(err)
	}
	cur := awaitStatus(t, odp, "NameConflict", func(o *v1alpha1.OnDemandPod) bool {
		c := meta.FindStatusCondition(o.Status.Conditions, "Ready")
		return c != nil && c.Reason == controller.ReasonNameConflict && c.Status == metav1.ConditionFalse
	})
	if cur.Status.Phase != "" {
		t.Errorf("phase = %q, want empty while conflicting", cur.Status.Phase)
	}
	pod, ok := getPod(odp)
	if !ok || pod.UID != foreign.UID || pod.Spec.Containers[0].Image != foreignImage || len(pod.OwnerReferences) != 0 {
		t.Errorf("foreign pod was modified: %+v", pod)
	}
}

func TestNameConflictResolvesWhenForeignPodIsDeleted(t *testing.T) {
	ctx := context.Background()
	odp := newCR(t, nil)
	foreign := foreignPod(odp)
	if err := k8s.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(ctx, odp); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, odp, "NameConflict", func(o *v1alpha1.OnDemandPod) bool {
		return meta.FindStatusCondition(o.Status.Conditions, "Ready") != nil
	})
	grace := int64(0)
	if err := k8s.Delete(ctx, foreign, &client.DeleteOptions{GracePeriodSeconds: &grace}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "operator-owned pod", func() bool {
		pod, ok := getPod(odp)
		return ok && metav1.IsControlledBy(pod, odp)
	})
}

const foreignImage = "other:1"

// foreignPod is a Pod that shares a CR's name but belongs to someone else.
func foreignPod(odp *v1alpha1.OnDemandPod) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: odp.Name, Namespace: odp.Namespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "x", Image: foreignImage}}},
	}
}
