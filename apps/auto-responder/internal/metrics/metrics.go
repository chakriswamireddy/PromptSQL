package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	BreakGlassRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "autoresponder_breakglass_requests_total",
		Help: "Break-glass session requests by status.",
	}, []string{"status"})

	BreakGlassActiveGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "autoresponder_breakglass_active_sessions",
		Help: "Currently active break-glass sessions per tenant.",
	}, []string{"tenant"})

	PlaybookActionTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "autoresponder_playbook_action_total",
		Help: "Auto-response playbook actions evaluated.",
	}, []string{"action", "tier", "tenant"})

	StepUpIssuedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "autoresponder_step_up_issued_total",
		Help: "Step-up MFA obligation tokens issued.",
	}, []string{"tenant"})

	StepUpCompletedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "autoresponder_step_up_completed_total",
		Help: "Step-up MFA obligations satisfied.",
	}, []string{"tenant"})

	AutoRevokeTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "autoresponder_break_glass_auto_revoke_total",
		Help: "Break-glass sessions auto-revoked on TTL expiry.",
	})

	HandlerDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "autoresponder_handler_duration_seconds",
		Help:    "HTTP handler latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"handler", "status"})
)
