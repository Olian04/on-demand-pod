package controller

import (
	"errors"
	"fmt"
	"time"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

// Config holds the operator-wide lease limits.
type Config struct {
	// DefaultTTL applies when a CR sets no ttl.
	DefaultTTL time.Duration
	// MaxTTL caps every lease. It is also the kubelet-enforced Pod deadline, so
	// it bounds a Pod's life even if the operator is down.
	MaxTTL time.Duration
}

// Validate rejects limits that cannot produce a sensible lease. Failing at
// startup is cheaper than discovering at expiry time that every pod is
// deleted instantly.
func (c Config) Validate() error {
	if c.DefaultTTL <= 0 {
		return fmt.Errorf("default TTL must be positive, got %s", c.DefaultTTL)
	}
	if c.MaxTTL < time.Second {
		return fmt.Errorf("max TTL must be at least one second, got %s", c.MaxTTL)
	}
	if c.DefaultTTL > c.MaxTTL {
		return errors.New("default TTL must not exceed max TTL")
	}
	return nil
}

// lease is the time window of one CR, derived only from the CR and Config.
type lease struct {
	expiresAt time.Time
	// clamped is true when spec.ttl exceeded the max and was reduced.
	clamped bool
}

// leaseOf computes the lease of a CR. It clamps instead of rejecting because
// there is no admission webhook to reject with.
//
// The result is truncated to whole seconds: metav1.Time serializes at second
// precision, and an unrounded value would never compare equal to what is read
// back, causing a status write on every reconcile. The cost is that a lease
// may end up to one second early.
func (c Config) leaseOf(odp *v1alpha1.OnDemandPod) lease {
	ttl := c.DefaultTTL
	if odp.Spec.TTL != nil {
		ttl = odp.Spec.TTL.Duration
	}
	clamped := ttl > c.MaxTTL
	if clamped {
		ttl = c.MaxTTL
	}
	return lease{
		expiresAt: odp.CreationTimestamp.Add(ttl).Truncate(time.Second),
		clamped:   clamped,
	}
}
