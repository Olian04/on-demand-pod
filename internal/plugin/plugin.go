// Package plugin implements the kubectl-ondemand commands. It holds no
// business logic: each function is one plain Kubernetes API operation on an
// OnDemandPod (create, get/list, merge-patch spec.ttl, delete), so applying
// YAML by hand behaves identically. The lease itself is the operator's job.
package plugin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

// RunOptions describes the OnDemandPod that Create submits.
type RunOptions struct {
	Image string
	// Name is generated when empty.
	Name string
	// TTL is left to the operator's default when nil.
	TTL *time.Duration
	// Command is the container command: the first element becomes spec.command
	// and the rest spec.args, matching how a shell reads `-- cmd args`.
	Command []string
}

const nameAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// GenerateName returns a name like "odp-k3x9a". It uses crypto/rand so two
// people starting a pod in the same second do not collide.
func GenerateName() (string, error) {
	suffix := make([]byte, 5)
	for i := range suffix {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(nameAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generate name: %w", err)
		}
		suffix[i] = nameAlphabet[n.Int64()]
	}
	return "odp-" + string(suffix), nil
}

// Create is `kubectl create` of an OnDemandPod.
func Create(ctx context.Context, c client.Client, namespace string, o RunOptions) (*v1alpha1.OnDemandPod, error) {
	if o.Image == "" {
		return nil, errors.New("an image is required")
	}
	name := o.Name
	if name == "" {
		var err error
		if name, err = GenerateName(); err != nil {
			return nil, err
		}
	}
	odp := &v1alpha1.OnDemandPod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       v1alpha1.OnDemandPodSpec{Image: o.Image},
	}
	if o.TTL != nil {
		odp.Spec.TTL = &metav1.Duration{Duration: *o.TTL}
	}
	if len(o.Command) > 0 {
		odp.Spec.Command = o.Command[:1]
		if len(o.Command) > 1 {
			odp.Spec.Args = o.Command[1:]
		}
	}
	if err := c.Create(ctx, odp); err != nil {
		return nil, fmt.Errorf("create %s: %w", name, err)
	}
	return odp, nil
}

// WaitReady polls until the Pod is Ready, or reaches a terminal phase, or ctx
// ends. progress, if non-nil, receives the Ready reason whenever it changes so
// a user stuck on an image pull sees why. It returns the last object read.
//
// It polls instead of watching because the wait is short, interactive and
// needs no extra machinery; the cost is up to one poll interval of latency.
func WaitReady(ctx context.Context, c client.Client, key types.NamespacedName, poll time.Duration, progress func(reason, message string)) (*v1alpha1.OnDemandPod, error) {
	var lastReason string
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		var odp v1alpha1.OnDemandPod
		if err := c.Get(ctx, key, &odp); err != nil {
			return nil, fmt.Errorf("get %s: %w", key.Name, err)
		}
		ready := meta.FindStatusCondition(odp.Status.Conditions, "Ready")
		switch {
		case ready != nil && ready.Status == metav1.ConditionTrue:
			return &odp, nil
		case odp.Status.Phase == "Failed" || odp.Status.Phase == "Succeeded":
			return &odp, fmt.Errorf("%s ended as %s%s", key.Name, odp.Status.Phase, reasonSuffix(ready))
		}
		if ready != nil && ready.Reason != lastReason {
			lastReason = ready.Reason
			if progress != nil {
				progress(ready.Reason, ready.Message)
			}
		}
		select {
		case <-ctx.Done():
			return &odp, fmt.Errorf("stopped waiting for %s%s: %w", key.Name, reasonSuffix(ready), ctx.Err())
		case <-ticker.C:
		}
	}
}

func reasonSuffix(ready *metav1.Condition) string {
	if ready == nil || ready.Reason == "" {
		return ""
	}
	return " (" + ready.Reason + ")"
}

// Row is one line of `ls`.
type Row struct {
	Name, Image, Phase, Ready string
	// Remaining is the time until expiresAt; it is negative once the lease has
	// ended but the operator has not yet deleted the CR. HasExpiry is false
	// until the operator has computed expiresAt.
	Remaining time.Duration
	HasExpiry bool
}

// List is `kubectl get odp` plus the time remaining, which a printer column
// cannot compute. now is a parameter so callers and tests control the clock.
func List(ctx context.Context, c client.Client, namespace string, now time.Time) ([]Row, error) {
	var list v1alpha1.OnDemandPodList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	rows := make([]Row, 0, len(list.Items))
	for i := range list.Items {
		odp := &list.Items[i]
		r := Row{Name: odp.Name, Image: odp.Spec.Image, Phase: odp.Status.Phase, Ready: "Unknown"}
		if cond := meta.FindStatusCondition(odp.Status.Conditions, "Ready"); cond != nil {
			r.Ready = string(cond.Status)
		}
		if odp.Status.ExpiresAt != nil {
			r.HasExpiry = true
			r.Remaining = odp.Status.ExpiresAt.Sub(now)
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b Row) int { return strings.Compare(a.Name, b.Name) })
	return rows, nil
}

// ExtendResult reports what the operator made of a new ttl.
type ExtendResult struct {
	// Observed is false if the operator did not acknowledge the change in time,
	// so Clamped is not yet known.
	Observed bool
	// Clamped is true when the ttl exceeds the operator's max and was reduced.
	Clamped bool
	// ExpiresAt is the lease end the operator computed, when Observed.
	ExpiresAt time.Time
}

// Extend is `kubectl patch --type merge -p '{"spec":{"ttl":"<d>"}}'`. The
// duration is the new total lease length from creation, not an increment,
// because spec.ttl is the lease length. It then waits up to observeTimeout for
// the operator to act on the new generation so a clamp can be reported.
func Extend(ctx context.Context, c client.Client, key types.NamespacedName, ttl time.Duration, poll, observeTimeout time.Duration) (ExtendResult, error) {
	odp := &v1alpha1.OnDemandPod{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	patch := fmt.Appendf(nil, `{"spec":{"ttl":%q}}`, ttl.String())
	if err := c.Patch(ctx, odp, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return ExtendResult{}, fmt.Errorf("extend %s: %w", key.Name, err)
	}
	want := odp.Generation

	ctx, cancel := context.WithTimeout(ctx, observeTimeout)
	defer cancel()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		var cur v1alpha1.OnDemandPod
		if err := c.Get(ctx, key, &cur); err == nil && cur.Status.ObservedGeneration >= want && cur.Status.ExpiresAt != nil {
			return ExtendResult{
				Observed:  true,
				Clamped:   meta.IsStatusConditionTrue(cur.Status.Conditions, "TTLClamped"),
				ExpiresAt: cur.Status.ExpiresAt.Time,
			}, nil
		}
		select {
		case <-ctx.Done():
			return ExtendResult{}, nil
		case <-ticker.C:
		}
	}
}

// Remove is `kubectl delete`. Background propagation matches what the operator
// does at expiry.
func Remove(ctx context.Context, c client.Client, key types.NamespacedName) error {
	odp := &v1alpha1.OnDemandPod{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if err := c.Delete(ctx, odp, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
		return fmt.Errorf("remove %s: %w", key.Name, err)
	}
	return nil
}

// FormatRemaining renders a Row's time left for humans.
func FormatRemaining(r Row) string {
	switch {
	case !r.HasExpiry:
		return "<unknown>"
	case r.Remaining <= 0:
		return "expired"
	default:
		return r.Remaining.Round(time.Second).String()
	}
}

// WriteTable prints rows as the `ls` table.
func WriteTable(out io.Writer, rows []Row) error {
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tIMAGE\tPHASE\tREADY\tREMAINING"); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Image, r.Phase, r.Ready, FormatRemaining(r)); err != nil {
			return err
		}
	}
	return w.Flush()
}
