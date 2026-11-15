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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

// OnDemandPodReconciler runs one Pod per OnDemandPod and mirrors its state.
// It keeps no state of its own: every step derives from the CR and the Pod.
type OnDemandPodReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ondemand.olian04.github.io,resources=ondemandpods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ondemand.olian04.github.io,resources=ondemandpods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create

// Reconcile creates the Pod if it is missing and mirrors its phase and
// readiness into status. There is no finalizer: the Pod is the only child and
// owner-reference garbage collection removes it with the CR.
func (r *OnDemandPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var odp v1alpha1.OnDemandPod
	if err := r.Get(ctx, req.NamespacedName, &odp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var pod corev1.Pod
	err := r.Get(ctx, req.NamespacedName, &pod)
	switch {
	case apierrors.IsNotFound(err):
		created, err := r.createPod(ctx, &odp)
		if err != nil {
			return ctrl.Result{}, err
		}
		pod = *created
	case err != nil:
		return ctrl.Result{}, fmt.Errorf("get pod: %w", err)
	case !metav1.IsControlledBy(&pod, &odp):
		// Touching a Pod we do not own could hijack someone else's workload.
		return ctrl.Result{}, r.reportConflict(ctx, &odp)
	}

	return ctrl.Result{}, r.mirror(ctx, &odp, &pod)
}

func (r *OnDemandPodReconciler) createPod(ctx context.Context, odp *v1alpha1.OnDemandPod) (*corev1.Pod, error) {
	pod := newPod(odp)
	if err := controllerutil.SetControllerReference(odp, pod, r.Scheme); err != nil {
		return nil, fmt.Errorf("set owner: %w", err)
	}
	if err := r.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("create pod: %w", err)
	}
	return pod, nil
}

func (r *OnDemandPodReconciler) reportConflict(ctx context.Context, odp *v1alpha1.OnDemandPod) error {
	return r.updateStatus(ctx, odp, func(s *v1alpha1.OnDemandPodStatus) {
		meta.SetStatusCondition(&s.Conditions, conflictCondition())
	})
}

func (r *OnDemandPodReconciler) mirror(ctx context.Context, odp *v1alpha1.OnDemandPod, pod *corev1.Pod) error {
	return r.updateStatus(ctx, odp, func(s *v1alpha1.OnDemandPodStatus) {
		s.Phase = phaseOf(pod)
		meta.SetStatusCondition(&s.Conditions, readyCondition(pod))
	})
}

// updateStatus applies fn and writes status only if something changed, so a
// steady state costs no API writes.
func (r *OnDemandPodReconciler) updateStatus(ctx context.Context, odp *v1alpha1.OnDemandPod, fn func(*v1alpha1.OnDemandPodStatus)) error {
	before := odp.Status.DeepCopy()
	fn(&odp.Status)
	odp.Status.ObservedGeneration = odp.Generation
	if equality.Semantic.DeepEqual(before, &odp.Status) {
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
