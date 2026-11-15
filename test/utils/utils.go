// Package utils holds helpers shared by the end-to-end tests.
package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Run executes cmd in the project root and returns its combined output. The
// output is part of the error, because a bare exit status is useless when a
// make or kubectl step fails in CI.
func Run(cmd *exec.Cmd) (string, error) {
	dir, err := GetProjectDir()
	if err != nil {
		return "", err
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s failed: %w\n%s", strings.Join(cmd.Args, " "), err, out)
	}
	return string(out), nil
}

// LoadImageToKindClusterWithName makes a locally built image available to the
// kind cluster named by KIND_CLUSTER (default "kind").
func LoadImageToKindClusterWithName(name string) error {
	cluster := "kind"
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok {
		cluster = v
	}
	kind := "kind"
	if v, ok := os.LookupEnv("KIND"); ok {
		kind = v
	}
	_, err := Run(exec.Command(kind, "load", "docker-image", name, "--name", cluster))
	return err
}

// GetProjectDir returns the repository root, found by walking up from the
// working directory to go.mod, so tests work from any package directory.
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for dir := wd; ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", wd)
		}
		dir = parent
	}
}
