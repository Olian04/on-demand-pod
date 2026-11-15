package metrics_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
	"github.com/Olian04/on-demand-pod/internal/metrics"
)

func cr(name, phase string) *v1alpha1.OnDemandPod {
	return &v1alpha1.OnDemandPod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status:     v1alpha1.OnDemandPodStatus{Phase: phase},
	}
}

func TestActivePodsByPhaseAndExpiredCounter(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		cr("a", "Running"), cr("b", "Running"), cr("c", "Failed"), cr("d", ""),
	).Build()

	reg := prometheus.NewRegistry()
	m, err := metrics.New(reg, reader)
	if err != nil {
		t.Fatal(err)
	}
	m.PodExpired()
	m.PodExpired()

	want := `
# HELP ondemandpod_active_pods OnDemandPods that currently exist, by phase. A CR without a phase has no Pod yet and is not counted.
# TYPE ondemandpod_active_pods gauge
ondemandpod_active_pods{phase="Failed"} 1
ondemandpod_active_pods{phase="Pending"} 0
ondemandpod_active_pods{phase="Running"} 2
ondemandpod_active_pods{phase="Succeeded"} 0
# HELP ondemandpod_expired_pods_total OnDemandPods deleted because their lease expired.
# TYPE ondemandpod_expired_pods_total counter
ondemandpod_expired_pods_total 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestRegisteringTwiceFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	reader := fake.NewClientBuilder().Build()
	if _, err := metrics.New(reg, reader); err != nil {
		t.Fatal(err)
	}
	if _, err := metrics.New(reg, reader); err == nil {
		t.Error("duplicate registration was accepted")
	}
}
