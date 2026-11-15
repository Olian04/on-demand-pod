package controller_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
	"github.com/Olian04/on-demand-pod/internal/controller"
)

func ttl(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

func createWithTTL(t *testing.T, d *metav1.Duration) *v1alpha1.OnDemandPod {
	t.Helper()
	odp := newCR(t, func(o *v1alpha1.OnDemandPod) { o.Spec.TTL = d })
	if err := k8s.Create(context.Background(), odp); err != nil {
		t.Fatal(err)
	}
	return awaitStatus(t, odp, "expiresAt", func(o *v1alpha1.OnDemandPod) bool { return o.Status.ExpiresAt != nil })
}

func wantExpiry(t *testing.T, odp *v1alpha1.OnDemandPod, d time.Duration) {
	t.Helper()
	want := odp.CreationTimestamp.Add(d)
	if got := odp.Status.ExpiresAt.Time; !got.Equal(want) {
		t.Errorf("expiresAt = %s, want %s (creation + %s)", got, want, d)
	}
}

// reconcileNow runs one reconcile directly, so the result can be inspected.
func reconcileNow(t *testing.T, odp *v1alpha1.OnDemandPod) ctrl.Result {
	t.Helper()
	res, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(odp)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func exists(odp *v1alpha1.OnDemandPod) bool {
	err := k8s.Get(context.Background(), client.ObjectKeyFromObject(odp), &v1alpha1.OnDemandPod{})
	return !apierrors.IsNotFound(err)
}

// poke triggers a reconcile after the fake clock moves, because requeues run
// on real timers.
func poke(t *testing.T, odp *v1alpha1.OnDemandPod) {
	t.Helper()
	patch := []byte(`{"metadata":{"annotations":{"poke":"` + time.Now().Format(time.RFC3339Nano) + `"}}}`)
	if err := k8s.Patch(context.Background(), odp, client.RawPatch("application/merge-patch+json", patch)); err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
}

func TestDefaultTTLApplies(t *testing.T) {
	odp := createWithTTL(t, nil)
	wantExpiry(t, odp, testConfig.DefaultTTL)
	if c := meta.FindStatusCondition(odp.Status.Conditions, "TTLClamped"); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("TTLClamped = %+v, want False", c)
	}
}

func TestTTLAboveMaxIsClampedNotRejected(t *testing.T) {
	odp := createWithTTL(t, ttl(100*time.Hour))
	wantExpiry(t, odp, testConfig.MaxTTL)
	if !meta.IsStatusConditionTrue(odp.Status.Conditions, "TTLClamped") {
		t.Error("TTLClamped is not True")
	}
	if !exists(odp) {
		t.Error("clamped CR was deleted")
	}
}

func TestExtendingTTLMovesExpiryAndClearsClamp(t *testing.T) {
	odp := createWithTTL(t, ttl(100*time.Hour))
	if !meta.IsStatusConditionTrue(odp.Status.Conditions, "TTLClamped") {
		t.Fatal("precondition: expected clamped")
	}
	odp.Spec.TTL = ttl(3 * time.Hour)
	if err := k8s.Update(context.Background(), odp); err != nil {
		t.Fatal(err)
	}
	cur := awaitStatus(t, odp, "new expiry", func(o *v1alpha1.OnDemandPod) bool {
		return o.Status.ExpiresAt != nil && o.Status.ExpiresAt.Time.Equal(o.CreationTimestamp.Add(3*time.Hour))
	})
	if meta.IsStatusConditionTrue(cur.Status.Conditions, "TTLClamped") {
		t.Error("TTLClamped still True after lowering ttl below max")
	}
}

func TestRequeuesAtExpiry(t *testing.T) {
	odp := createWithTTL(t, ttl(time.Hour))
	fakeClock.Step(10 * time.Minute)
	got := reconcileNow(t, odp).RequeueAfter
	want := odp.Status.ExpiresAt.Sub(fakeClock.Now())
	if got != want {
		t.Errorf("RequeueAfter = %s, want time until expiresAt = %s", got, want)
	}
}

func TestNothingIsDeletedBeforeExpiry(t *testing.T) {
	odp := createWithTTL(t, ttl(time.Hour))
	pod := awaitPod(t, odp)
	// A finished Pod must outlive its work so logs stay readable.
	pod.Status.Phase = corev1.PodSucceeded
	if err := k8s.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, odp, "Succeeded", func(o *v1alpha1.OnDemandPod) bool { return o.Status.Phase == "Succeeded" })

	fakeClock.SetTime(odp.Status.ExpiresAt.Add(-time.Second))
	reconcileNow(t, odp)
	if !exists(odp) {
		t.Fatal("CR deleted one second before expiresAt")
	}
	if _, ok := getPod(odp); !ok {
		t.Fatal("Pod deleted before expiresAt")
	}
}

func TestDeletesCRAtExpiry(t *testing.T) {
	odp := createWithTTL(t, ttl(time.Hour))
	awaitPod(t, odp)

	fakeClock.SetTime(odp.Status.ExpiresAt.Time)
	poke(t, odp)
	eventually(t, "CR deletion", func() bool { return !exists(odp) })
}

func TestPodCarriesMaxTTLDeadlineBackstop(t *testing.T) {
	odp := createWithTTL(t, ttl(time.Hour))
	pod := awaitPod(t, odp)
	want := int64(testConfig.MaxTTL.Seconds())
	if got := pod.Spec.ActiveDeadlineSeconds; got == nil || *got != want {
		t.Errorf("activeDeadlineSeconds = %v, want %d", got, want)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := map[string]controller.Config{
		"zero default":     {DefaultTTL: 0, MaxTTL: time.Hour},
		"negative default": {DefaultTTL: -time.Hour, MaxTTL: time.Hour},
		"zero max":         {DefaultTTL: time.Hour, MaxTTL: 0},
		"default over max": {DefaultTTL: 2 * time.Hour, MaxTTL: time.Hour},
	}
	for name, cfg := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := controller.NewOnDemandPodReconciler(k8s, nil, cfg); err == nil {
				t.Error("invalid config accepted")
			}
		})
	}
}

// The design sets activeDeadlineSeconds once to the max TTL and enforces the
// precise lease in the operator, on the belief that a live Pod's deadline can
// be lowered but not raised. This pins that belief to the Kubernetes version
// under test, so a change in behaviour fails loudly instead of silently
// invalidating the design.
func TestActiveDeadlineSecondsCanBeLoweredNotRaised(t *testing.T) {
	ctx := context.Background()
	odp := newCR(t, nil)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "deadline-" + odp.Name, Namespace: "default"},
		Spec: corev1.PodSpec{
			ActiveDeadlineSeconds: new(int64(100)),
			Containers:            []corev1.Container{{Name: "x", Image: testImage}},
		},
	}
	if err := k8s.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Spec.ActiveDeadlineSeconds = new(int64(50))
	if err := k8s.Update(ctx, pod); err != nil {
		t.Errorf("lowering the deadline was rejected: %v", err)
	}
	pod.Spec.ActiveDeadlineSeconds = new(int64(500))
	if err := k8s.Update(ctx, pod); err == nil {
		t.Error("raising the deadline was accepted: the design could be simplified")
	}
}

// statusCounter counts status writes, which the API server hides: it drops
// no-op updates without bumping resourceVersion.
type statusCounter struct {
	client.Client
	writes atomic.Int64
}

func (c *statusCounter) Status() client.SubResourceWriter {
	return &countingWriter{SubResourceWriter: c.Client.Status(), n: &c.writes}
}

type countingWriter struct {
	client.SubResourceWriter
	n *atomic.Int64
}

func (w *countingWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	w.n.Add(1)
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

// A settled CR must cost no API writes. Without truncating expiresAt to the
// second precision metav1.Time serializes at, the stored value never equals the
// computed one and every reconcile rewrites status.
func TestSettledStatusIsNotRewritten(t *testing.T) {
	odp := createWithTTL(t, ttl(time.Hour+500*time.Millisecond))
	awaitPod(t, odp)
	awaitStatus(t, odp, "settled", func(o *v1alpha1.OnDemandPod) bool { return o.Status.Phase == phasePending })

	counter := &statusCounter{Client: k8s}
	r, err := controller.NewOnDemandPodReconciler(counter, k8s.Scheme(), testConfig, controller.WithClock(fakeClock))
	if err != nil {
		t.Fatal(err)
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(odp)}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	counter.writes.Store(0)
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if n := counter.writes.Load(); n != 0 {
		t.Errorf("an idle reconcile wrote status %d times", n)
	}
}
