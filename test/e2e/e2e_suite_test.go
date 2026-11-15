//go:build e2e

// Package e2e runs the operator against a real cluster, normally kind. It
// covers what envtest cannot: a kubelet, owner-reference garbage collection and
// the container runtime's reaction to the hardened security context.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
	"github.com/Olian04/on-demand-pod/test/utils"
)

const (
	managerImage = "example.com/on-demand-pod:e2e"
	operatorNS   = "on-demand-pod-system"
	namespace    = "default"

	// nonRootImage runs as a numeric non-root user, which the restricted
	// security context requires. busybox runs as root and must not start.
	nonRootImage = "registry.k8s.io/pause:3.10"
	rootImage    = "busybox:1.37"

	pollEvery = time.Second
)

var k8s client.Client

func TestMain(m *testing.M) {
	code := 1
	defer func() { os.Exit(code) }()
	if err := setup(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		return
	}
	code = m.Run()
	teardown()
}

// setup builds the operator image, loads it into kind, deploys it with the
// shipped kustomize config and builds the plugin binary the tests drive.
func setup() error {
	if _, err := utils.Run(exec.Command("make", "docker-build", "IMG="+managerImage)); err != nil {
		return err
	}
	if err := utils.LoadImageToKindClusterWithName(managerImage); err != nil {
		return err
	}
	steps := [][]string{
		{"make", "deploy", "IMG=" + managerImage},
		{"kubectl", "rollout", "status", "deployment/on-demand-pod-controller-manager", "-n", operatorNS, "--timeout=120s"},
		{"make", "build"},
	}
	for _, s := range steps {
		if _, err := utils.Run(exec.Command(s[0], s[1:]...)); err != nil {
			return err
		}
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme.Scheme); err != nil {
		return err
	}
	k8s, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	return err
}

func teardown() {
	_, _ = utils.Run(exec.Command("make", "undeploy", "ignore-not-found=true"))
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, detail := cond()
		if ok {
			return
		}
		last = detail
		time.Sleep(pollEvery)
	}
	t.Fatalf("timed out after %s waiting for %s; last state: %s", timeout, what, last)
}

func create(t *testing.T, name, image, ttl string) {
	t.Helper()
	d, err := time.ParseDuration(ttl)
	if err != nil {
		t.Fatal(err)
	}
	odp := &v1alpha1.OnDemandPod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.OnDemandPodSpec{Image: image, TTL: &metav1.Duration{Duration: d}},
	}
	if err := k8s.Create(context.Background(), odp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k8s.Delete(context.Background(), odp) })
}

func key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

func isGone(obj client.Object, name string) bool {
	return apierrors.IsNotFound(k8s.Get(context.Background(), key(name), obj))
}
