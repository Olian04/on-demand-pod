package v1alpha1_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

const quotaResource = corev1.ResourceName("count/ondemandpods.ondemand.olian04.github.io")

// Fact 3 of the design: a ResourceQuota can cap how many OnDemandPods a
// namespace holds. envtest runs no controller-manager, so nothing computes
// status.used; the test seeds it the way the quota controller would. What it
// proves is that the API server's quota admission recognises the resource name
// and enforces the limit on create.
func TestObjectCountQuotaLimitsOnDemandPods(t *testing.T) {
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "quota"}}
	if err := k8s.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "q", Namespace: ns.Name},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{quotaResource: resource.MustParse("1")}},
	}
	if err := k8s.Create(ctx, quota); err != nil {
		t.Fatalf("quota with %s was rejected: %v", quotaResource, err)
	}
	quota.Status.Hard = quota.Spec.Hard
	quota.Status.Used = corev1.ResourceList{quotaResource: resource.MustParse("0")}
	if err := k8s.Status().Update(ctx, quota); err != nil {
		t.Fatal(err)
	}

	mk := func(name string) *v1alpha1.OnDemandPod {
		return &v1alpha1.OnDemandPod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
			Spec:       v1alpha1.OnDemandPodSpec{Image: testImage},
		}
	}
	if err := k8s.Create(ctx, mk("first")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := k8s.Create(ctx, mk("second"))
	if err == nil {
		t.Fatal("second create was accepted: the quota is not enforced")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("second create error = %v, want Forbidden (exceeded quota)", err)
	}
}
