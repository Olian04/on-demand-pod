package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

// OnDemandPodReconciler runs one Pod per OnDemandPod, mirrors its state, and
// deletes the CR when its lease ends. It keeps no state of its own: every step
// derives from the CR, the Pod and the clock.
type OnDemandPodReconciler struct {
	client.Client
	scheme *runtime.Scheme
	cfg    Config
	clock  clock.PassiveClock
}

// Option customizes an OnDemandPodReconciler.
type Option func(*OnDemandPodReconciler)

// WithClock replaces the wall clock. Tests use it to cross an expiry without
// sleeping. Note that RequeueAfter still runs on real timers, so a test that
// moves a fake clock must also trigger a reconcile.
func WithClock(c clock.PassiveClock) Option {
	return func(r *OnDemandPodReconciler) { r.clock = c }
}

// NewOnDemandPodReconciler validates cfg and returns a reconciler.
func NewOnDemandPodReconciler(c client.Client, scheme *runtime.Scheme, cfg Config, opts ...Option) (*OnDemandPodReconciler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	r := &OnDemandPodReconciler{Client: c, scheme: scheme, cfg: cfg, clock: clock.RealClock{}}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// +kubebuilder:rbac:groups=ondemand.olian04.github.io,resources=ondemandpods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=ondemand.olian04.github.io,resources=ondemandpods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create

// Reconcile enforces the lease. Nothing is deleted before expiresAt; after it
// the CR is deleted with background propagation and owner-reference garbage
// collection removes the Pod, so no finalizer is needed.
func (r *OnDemandPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var odp v1alpha1.OnDemandPod
	if err := r.Get(ctx, req.NamespacedName, &odp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	l := r.cfg.leaseOf(&odp)
	now := r.clock.Now()
	if !now.Before(l.expiresAt) {
		// Status is not written first: the object is about to disappear.
		err := r.Delete(ctx, &odp, client.PropagationPolicy(metav1.DeletePropagationBackground))
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pod, err := r.ensurePod(ctx, &odp)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.syncStatus(ctx, &odp, pod, l); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: l.expiresAt.Sub(now)}, nil
}

// ensurePod returns the CR's Pod, creating it if missing. It returns nil when
// the name is taken by a Pod this CR does not own: touching it could hijack
// someone else's workload.
func (r *OnDemandPodReconciler) ensurePod(ctx context.Context, odp *v1alpha1.OnDemandPod) (*corev1.Pod, error) {
	var pod corev1.Pod
	err := r.Get(ctx, client.ObjectKeyFromObject(odp), &pod)
	switch {
	case apierrors.IsNotFound(err):
		return r.createPod(ctx, odp)
	case err != nil:
		return nil, fmt.Errorf("get pod: %w", err)
	case !metav1.IsControlledBy(&pod, odp):
		return nil, nil
	}
	return &pod, nil
}

func (r *OnDemandPodReconciler) createPod(ctx context.Context, odp *v1alpha1.OnDemandPod) (*corev1.Pod, error) {
	pod := newPod(odp, r.cfg.MaxTTL)
	if err := controllerutil.SetControllerReference(odp, pod, r.scheme); err != nil {
		return nil, fmt.Errorf("set owner: %w", err)
	}
	if err := r.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("create pod: %w", err)
	}
	return pod, nil
}

// syncStatus writes reality into status, and only if something changed, so a
// steady state costs no API writes. A nil pod means a name conflict.
func (r *OnDemandPodReconciler) syncStatus(ctx context.Context, odp *v1alpha1.OnDemandPod, pod *corev1.Pod, l lease) error {
	before := odp.Status.DeepCopy()
	s := &odp.Status
	s.ObservedGeneration = odp.Generation
	s.ExpiresAt = new(metav1.NewTime(l.expiresAt))
	meta.SetStatusCondition(&s.Conditions, clampedCondition(l.clamped))
	if pod == nil {
		meta.SetStatusCondition(&s.Conditions, conflictCondition())
	} else {
		s.Phase = phaseOf(pod)
		meta.SetStatusCondition(&s.Conditions, readyCondition(pod))
	}
	if equality.Semantic.DeepEqual(before, s) {
		return nil
	}
	if err := r.Status().Update(ctx, odp); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// SetupWithManager wires the controller. Pods are mapped back to the CR with
// the same name instead of through Owns, because a conflicting Pod is not
// owned and would otherwise never trigger a retry once it disappears.
func (r *OnDemandPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.OnDemandPod{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToCR)).
		Named("ondemandpod").
		Complete(r)
}

// podToCR enqueues the CR sharing a Pod's name. It checks the cache first so
// the many unrelated Pods in a cluster cost a lookup and no reconcile.
func (r *OnDemandPodReconciler) podToCR(ctx context.Context, pod client.Object) []reconcile.Request {
	key := types.NamespacedName{Namespace: pod.GetNamespace(), Name: pod.GetName()}
	if err := r.Get(ctx, key, &v1alpha1.OnDemandPod{}); err != nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: key}}
}
