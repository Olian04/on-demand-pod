// Package testenv starts an envtest control plane with the OnDemandPod CRD
// installed. It exists so every package's integration tests share one
// bootstrap instead of copying it.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// Start launches kube-apiserver and etcd and returns the environment and its
// config. The caller must Stop the environment. The cost is a few seconds per
// call, so start it once per test package from TestMain.
func Start() (*envtest.Environment, *rest.Config, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, nil, err
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(root, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	// KUBEBUILDER_ASSETS wins; otherwise fall back to what `make setup-envtest`
	// installed so tests also run straight from an IDE.
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		dirs, _ := filepath.Glob(filepath.Join(root, "bin", "k8s", "*"))
		if len(dirs) > 0 {
			env.BinaryAssetsDirectory = dirs[0]
		}
	}
	cfg, err := env.Start()
	if err != nil {
		return nil, nil, fmt.Errorf("start envtest: %w", err)
	}
	return env, cfg, nil
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate testenv source file")
	}
	return filepath.Join(filepath.Dir(file), "..", ".."), nil
}
