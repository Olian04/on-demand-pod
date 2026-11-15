// Package metrics exposes the operator's Prometheus metrics. It depends on the
// API types only, never on the controller, so the controller can depend on it
// through a small interface.
package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Olian04/on-demand-pod/api/v1alpha1"
)

// phases are all values status.phase can take. Reporting every one, even at
// zero, keeps a series from vanishing and breaking rate and alert queries.
var phases = []string{"Pending", "Running", "Succeeded", "Failed"}

// listTimeout bounds a scrape. The list is served from the informer cache, so
// this only guards against a stalled cache.
const listTimeout = 5 * time.Second

// Metrics holds the operator's instruments.
type Metrics struct {
	expired prometheus.Counter
}

// New registers the metrics on reg. active is read at scrape time, which keeps
// the gauge exact without operator-local state: a deleted CR cannot leave a
// stale count behind. The cost is one cache list per scrape.
func New(reg prometheus.Registerer, active client.Reader) (*Metrics, error) {
	m := &Metrics{
		expired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ondemandpod_expired_pods_total",
			Help: "OnDemandPods deleted because their lease expired.",
		}),
	}
	for _, c := range []prometheus.Collector{m.expired, &activeCollector{reader: active}} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register metrics: %w", err)
		}
	}
	return m, nil
}

// PodExpired records one lease expiry.
func (m *Metrics) PodExpired() { m.expired.Inc() }

var activeDesc = prometheus.NewDesc(
	"ondemandpod_active_pods",
	"OnDemandPods that currently exist, by phase. A CR without a phase has no Pod yet and is not counted.",
	[]string{"phase"}, nil,
)

type activeCollector struct{ reader client.Reader }

func (c *activeCollector) Describe(ch chan<- *prometheus.Desc) { ch <- activeDesc }

func (c *activeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	var list v1alpha1.OnDemandPodList
	if err := c.reader.List(ctx, &list); err != nil {
		ch <- prometheus.NewInvalidMetric(activeDesc, err)
		return
	}
	counts := make(map[string]int, len(phases))
	for i := range list.Items {
		if p := list.Items[i].Status.Phase; p != "" {
			counts[p]++
		}
	}
	for _, p := range phases {
		ch <- prometheus.MustNewConstMetric(activeDesc, prometheus.GaugeValue, float64(counts[p]), p)
	}
}
