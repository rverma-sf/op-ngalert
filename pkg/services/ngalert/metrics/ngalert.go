package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	GrafanaBackend = "grafana"
	ProxyBackend   = "proxy"
	Namespace      = "grafana"
	Subsystem      = "alerting"
)

// ProvideService is a Metrics factory.
func ProvideService() *NGAlert {
	return NewNGAlert(prometheus.DefaultRegisterer)
}

// ProvideServiceForTest is a Metrics factory used for test.
func ProvideServiceForTest() *NGAlert {
	return NewNGAlert(prometheus.NewRegistry())
}

type NGAlert struct {
	// Registerer is used by subcomponents which register their own metrics.
	Registerer prometheus.Registerer

	schedulerMetrics            *Scheduler
	stateMetrics                *State
	multiOrgAlertmanagerMetrics *MultiOrgAlertmanager
	apiMetrics                  *API
}

// NewNGAlert manages the metrics of all the alerting components.
func NewNGAlert(r prometheus.Registerer) *NGAlert {
	return &NGAlert{
		Registerer:                  r,
		schedulerMetrics:            NewSchedulerMetrics(r),
		stateMetrics:                NewStateMetrics(r),
		multiOrgAlertmanagerMetrics: NewMultiOrgAlertmanagerMetrics(r),
		apiMetrics:                  NewAPIMetrics(r),
	}
}

func (ng *NGAlert) GetSchedulerMetrics() *Scheduler {
	return ng.schedulerMetrics
}

func (ng *NGAlert) GetStateMetrics() *State {
	return ng.stateMetrics
}

func (ng *NGAlert) GetAPIMetrics() *API {
	return ng.apiMetrics
}

func (ng *NGAlert) GetMultiOrgAlertmanagerMetrics() *MultiOrgAlertmanager {
	return ng.multiOrgAlertmanagerMetrics
}

// RemoveOrgRegistry removes the *prometheus.Registry for the specified org. It is safe to call concurrently.
func (moa *MultiOrgAlertmanager) RemoveOrgRegistry(id int64) {
	moa.registries.RemoveOrgRegistry(id)
}

// GetOrCreateOrgRegistry gets or creates a *prometheus.Registry for the specified org. It is safe to call concurrently.
func (moa *MultiOrgAlertmanager) GetOrCreateOrgRegistry(id int64) prometheus.Registerer {
	return moa.registries.GetOrCreateOrgRegistry(id)
}
