package v1alpha1_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
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

var counter int

func fullSpec() v1alpha1.OnDemandPodSpec {
	ttl := metav1.Duration{Duration: time.Hour}
	return v1alpha1.OnDemandPodSpec{
		Image:   "busybox:1",
		Command: []string{"sh"},
		Args:    []string{"-c", "sleep 1"},
		Env:     []corev1.EnvVar{{Name: "A", Value: "1"}},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		},
		TTL: &ttl,
	}
}

func create(t *testing.T, spec v1alpha1.OnDemandPodSpec) *v1alpha1.OnDemandPod {
	t.Helper()
	counter++
	odp := &v1alpha1.OnDemandPod{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("odp-%d", counter), Namespace: "default"},
		Spec:       spec,
	}
	if err := k8s.Create(context.Background(), odp); err != nil {
		t.Fatalf("create: %v", err)
	}
	return odp
}

// immutableField describes how to change, add and remove one immutable field.
// change starts from fullSpec; add starts from a spec with only an image;
// remove starts from fullSpec.
type immutableField struct {
	name   string
	change func(*v1alpha1.OnDemandPodSpec)
	add    func(*v1alpha1.OnDemandPodSpec)
	remove func(*v1alpha1.OnDemandPodSpec)
}

var cpu = func(q string) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(q)}
}

var immutableFields = []immutableField{
	{
		name:   "image",
		change: func(s *v1alpha1.OnDemandPodSpec) { s.Image = "busybox:2" },
	},
	{
		name:   "command",
		change: func(s *v1alpha1.OnDemandPodSpec) { s.Command = []string{"bash"} },
		add:    func(s *v1alpha1.OnDemandPodSpec) { s.Command = []string{"sh"} },
		remove: func(s *v1alpha1.OnDemandPodSpec) { s.Command = nil },
	},
	{
		name:   "args",
		change: func(s *v1alpha1.OnDemandPodSpec) { s.Args = []string{"x"} },
		add:    func(s *v1alpha1.OnDemandPodSpec) { s.Args = []string{"x"} },
		remove: func(s *v1alpha1.OnDemandPodSpec) { s.Args = nil },
	},
	{
		name:   "env",
		change: func(s *v1alpha1.OnDemandPodSpec) { s.Env = []corev1.EnvVar{{Name: "B", Value: "2"}} },
		add:    func(s *v1alpha1.OnDemandPodSpec) { s.Env = []corev1.EnvVar{{Name: "A", Value: "1"}} },
		remove: func(s *v1alpha1.OnDemandPodSpec) { s.Env = nil },
	},
	{
		name:   "resources",
		change: func(s *v1alpha1.OnDemandPodSpec) { s.Resources.Limits = cpu("200m") },
		add:    func(s *v1alpha1.OnDemandPodSpec) { s.Resources.Limits = cpu("1") },
		remove: func(s *v1alpha1.OnDemandPodSpec) { s.Resources = corev1.ResourceRequirements{} },
	},
}

func rejectsUpdate(t *testing.T, initial v1alpha1.OnDemandPodSpec, mutate func(*v1alpha1.OnDemandPodSpec), what string) {
	t.Helper()
	odp := create(t, initial)
	mutate(&odp.Spec)
	if err := k8s.Update(context.Background(), odp); err == nil {
		t.Fatalf("%s was accepted", what)
	}
}

func TestImmutableFieldsRejectChange(t *testing.T) {
	for _, f := range immutableFields {
		t.Run(f.name, func(t *testing.T) { rejectsUpdate(t, fullSpec(), f.change, "changing "+f.name) })
	}
}

// Transition rules only run when a field exists in both the old and the new
// object, so a field-level rule alone lets a user add an omitted field later.
func TestImmutableFieldsRejectAddition(t *testing.T) {
	for _, f := range immutableFields {
		if f.add == nil {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			rejectsUpdate(t, v1alpha1.OnDemandPodSpec{Image: "busybox:1"}, f.add, "adding "+f.name)
		})
	}
}

func TestImmutableFieldsRejectRemoval(t *testing.T) {
	for _, f := range immutableFields {
		if f.remove == nil {
			continue
		}
		t.Run(f.name, func(t *testing.T) { rejectsUpdate(t, fullSpec(), f.remove, "removing "+f.name) })
	}
}

func TestTTLIsMutable(t *testing.T) {
	odp := create(t, fullSpec())
	longer := metav1.Duration{Duration: 3 * time.Hour}
	odp.Spec.TTL = &longer
	if err := k8s.Update(context.Background(), odp); err != nil {
		t.Fatalf("extending ttl: %v", err)
	}
	odp.Spec.TTL = nil
	if err := k8s.Update(context.Background(), odp); err != nil {
		t.Fatalf("clearing ttl: %v", err)
	}
}

func TestImageMustNotBeEmpty(t *testing.T) {
	counter++
	odp := &v1alpha1.OnDemandPod{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("odp-%d", counter), Namespace: "default"},
	}
	if err := k8s.Create(context.Background(), odp); err == nil {
		t.Fatal("empty image accepted")
	}
}
