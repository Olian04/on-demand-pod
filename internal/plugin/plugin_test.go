package plugin_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
	"github.com/Olian04/on-demand-pod/internal/plugin"
	"github.com/Olian04/on-demand-pod/internal/testenv"
)

const (
	ns          = "default"
	poll        = 20 * time.Millisecond
	testImage   = "busybox:1"
	readyType   = "Ready"
	running     = "Running"
	clampedType = "TTLClamped"
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
	if err := v1alpha1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme.Scheme}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	code = m.Run()
}

var seq atomic.Int64

func uniq() string { return fmt.Sprintf("p-%d", seq.Add(1)) }

func get(t *testing.T, name string) *v1alpha1.OnDemandPod {
	t.Helper()
	var odp v1alpha1.OnDemandPod
	if err := k8s.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &odp); err != nil {
		t.Fatal(err)
	}
	return &odp
}

// setStatus plays the operator, which these tests do not run.
func setStatus(t *testing.T, name string, mutate func(*v1alpha1.OnDemandPodStatus)) {
	t.Helper()
	odp := get(t, name)
	mutate(&odp.Status)
	if err := k8s.Status().Update(context.Background(), odp); err != nil {
		t.Fatal(err)
	}
}

func TestCreateMapsOptionsToSpec(t *testing.T) {
	ttl := 90 * time.Minute
	name := uniq()
	odp, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{
		Image: testImage, Name: name, TTL: &ttl, Command: []string{"sh", "-c", "sleep 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := get(t, odp.Name).Spec
	if got.Image != testImage || got.TTL == nil || got.TTL.Duration != ttl {
		t.Errorf("spec = %+v", got)
	}
	if len(got.Command) != 1 || got.Command[0] != "sh" || strings.Join(got.Args, " ") != "-c sleep 1" {
		t.Errorf("command=%v args=%v", got.Command, got.Args)
	}
}

func TestCreateLeavesTTLToOperatorAndGeneratesName(t *testing.T) {
	odp, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(odp.Name, "odp-") || len(odp.Name) != len("odp-")+5 {
		t.Errorf("generated name %q", odp.Name)
	}
	if get(t, odp.Name).Spec.TTL != nil {
		t.Error("ttl was set although none was requested")
	}
}

func TestCreateRequiresImage(t *testing.T) {
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{}); err == nil {
		t.Error("empty image accepted")
	}
}

func TestCreateFailsOnDuplicateName(t *testing.T) {
	name := uniq()
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); !apierrors.IsAlreadyExists(err) {
		t.Errorf("err = %v, want AlreadyExists", err)
	}
}

func TestWaitReadyReturnsOnceReadyAndReportsReasons(t *testing.T) {
	name := uniq()
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); err != nil {
		t.Fatal(err)
	}
	setStatus(t, name, func(s *v1alpha1.OnDemandPodStatus) {
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{Type: readyType, Status: metav1.ConditionFalse, Reason: "ImagePullBackOff"})
	})
	go func() {
		time.Sleep(200 * time.Millisecond)
		setStatus(t, name, func(s *v1alpha1.OnDemandPodStatus) {
			s.Phase = running
			meta.SetStatusCondition(&s.Conditions, metav1.Condition{Type: readyType, Status: metav1.ConditionTrue, Reason: "PodReady"})
		})
	}()
	var reasons []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := plugin.WaitReady(ctx, k8s, types.NamespacedName{Namespace: ns, Name: name}, poll, func(r, _ string) { reasons = append(reasons, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(reasons) == 0 || reasons[0] != "ImagePullBackOff" {
		t.Errorf("progress reasons = %v", reasons)
	}
}

func TestWaitReadyFailsWhenPodFails(t *testing.T) {
	name := uniq()
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); err != nil {
		t.Fatal(err)
	}
	setStatus(t, name, func(s *v1alpha1.OnDemandPodStatus) {
		s.Phase = "Failed"
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{Type: readyType, Status: metav1.ConditionFalse, Reason: "Error"})
	})
	_, err := plugin.WaitReady(context.Background(), k8s, types.NamespacedName{Namespace: ns, Name: name}, poll, nil)
	if err == nil || !strings.Contains(err.Error(), "Failed") || !strings.Contains(err.Error(), "Error") {
		t.Errorf("err = %v", err)
	}
}

func TestWaitReadyStopsWhenContextEnds(t *testing.T) {
	name := uniq()
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := plugin.WaitReady(ctx, k8s, types.NamespacedName{Namespace: ns, Name: name}, poll, nil); err == nil {
		t.Error("returned without error although never ready")
	}
}

func TestListShowsTimeRemaining(t *testing.T) {
	name := uniq()
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); err != nil {
		t.Fatal(err)
	}
	expires := metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))
	setStatus(t, name, func(s *v1alpha1.OnDemandPodStatus) {
		s.Phase = running
		s.ExpiresAt = &expires
		meta.SetStatusCondition(&s.Conditions, metav1.Condition{Type: readyType, Status: metav1.ConditionTrue, Reason: "PodReady"})
	})
	now := expires.Add(-45 * time.Minute)
	rows, err := plugin.List(context.Background(), k8s, ns, now)
	if err != nil {
		t.Fatal(err)
	}
	var row *plugin.Row
	for i := range rows {
		if rows[i].Name == name {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatal("row missing")
	}
	if row.Remaining != 45*time.Minute || row.Ready != "True" || row.Phase != running || row.Image != testImage {
		t.Errorf("row = %+v", *row)
	}
	if got := plugin.FormatRemaining(*row); got != "45m0s" {
		t.Errorf("FormatRemaining = %q", got)
	}
	if got := plugin.FormatRemaining(plugin.Row{HasExpiry: true, Remaining: -time.Second}); got != "expired" {
		t.Errorf("expired row = %q", got)
	}
	if got := plugin.FormatRemaining(plugin.Row{}); got != "<unknown>" {
		t.Errorf("no-expiry row = %q", got)
	}
}

// actAsOperator acknowledges every generation of name, clamping if asked.
func actAsOperator(t *testing.T, name string, clamp bool, stop <-chan struct{}) {
	t.Helper()
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			var odp v1alpha1.OnDemandPod
			if err := k8s.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &odp); err != nil {
				continue
			}
			if odp.Status.ObservedGeneration >= odp.Generation && odp.Status.ExpiresAt != nil {
				continue
			}
			exp := metav1.NewTime(odp.CreationTimestamp.Add(time.Hour))
			odp.Status.ObservedGeneration = odp.Generation
			odp.Status.ExpiresAt = &exp
			status := metav1.ConditionFalse
			if clamp {
				status = metav1.ConditionTrue
			}
			meta.SetStatusCondition(&odp.Status.Conditions, metav1.Condition{Type: clampedType, Status: status, Reason: "Test"})
			_ = k8s.Status().Update(context.Background(), &odp)
		}
	}()
}

func TestExtendPatchesTTLAndReportsClamp(t *testing.T) {
	for _, clamp := range []bool{false, true} {
		t.Run(fmt.Sprintf("clamped=%v", clamp), func(t *testing.T) {
			name := uniq()
			ttl := time.Hour
			if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name, TTL: &ttl}); err != nil {
				t.Fatal(err)
			}
			stop := make(chan struct{})
			defer close(stop)
			actAsOperator(t, name, clamp, stop)

			res, err := plugin.Extend(context.Background(), k8s, types.NamespacedName{Namespace: ns, Name: name}, 3*time.Hour, poll, 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if got := get(t, name).Spec.TTL.Duration; got != 3*time.Hour {
				t.Errorf("spec.ttl = %s, want 3h", got)
			}
			if !res.Observed || res.Clamped != clamp {
				t.Errorf("result = %+v, want observed with clamped=%v", res, clamp)
			}
		})
	}
}

func TestExtendReportsUnobservedWhenOperatorIsSilent(t *testing.T) {
	name := uniq()
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); err != nil {
		t.Fatal(err)
	}
	res, err := plugin.Extend(context.Background(), k8s, types.NamespacedName{Namespace: ns, Name: name}, 3*time.Hour, poll, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if res.Observed {
		t.Error("claimed the operator observed the change")
	}
}

func TestExtendMissingPodFails(t *testing.T) {
	if _, err := plugin.Extend(context.Background(), k8s, types.NamespacedName{Namespace: ns, Name: "nope"}, time.Hour, poll, time.Second); err == nil {
		t.Error("extending a missing CR succeeded")
	}
}

func TestRemoveDeletesCR(t *testing.T) {
	name := uniq()
	if _, err := plugin.Create(context.Background(), k8s, ns, plugin.RunOptions{Image: testImage, Name: name}); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: ns, Name: name}
	if err := plugin.Remove(context.Background(), k8s, key); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(context.Background(), key, &v1alpha1.OnDemandPod{}); !apierrors.IsNotFound(err) {
		t.Errorf("CR still present: %v", err)
	}
	if err := plugin.Remove(context.Background(), k8s, key); err == nil {
		t.Error("removing a missing CR succeeded")
	}
}

func TestWriteTable(t *testing.T) {
	var b strings.Builder
	rows := []plugin.Row{{Name: "a", Image: testImage, Phase: running, Ready: "True", HasExpiry: true, Remaining: time.Minute}}
	if err := plugin.WriteTable(&b, rows); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "NAME") || !strings.Contains(lines[1], "1m0s") {
		t.Errorf("table:\n%s", b.String())
	}
}
