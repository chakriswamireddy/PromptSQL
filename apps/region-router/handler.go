package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	routeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "region_router_routes_total",
		Help: "Total routing decisions by target region and reason",
	}, []string{"source_region", "target_region", "reason"})

	routeLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "region_router_route_latency_seconds",
		Help:    "Latency of upstream proxy round-trip",
		Buckets: prometheus.DefBuckets,
	}, []string{"target_region"})

	residencyViolations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "region_router_residency_violations_total",
		Help: "Requests refused because they violate data residency constraints",
	}, []string{"tenant_id", "source_region", "required_region"})
)

// routerHandler is the main HTTP handler for the region router.
type routerHandler struct {
	cfg      *Config
	store    *ResidencyStore
	proxies  map[string]*httputil.ReverseProxy  // region → upstream proxy
	tracer   trace.Tracer
	log      *slog.Logger
}

func newRouterHandler(cfg *Config, store *ResidencyStore, tracer trace.Tracer, log *slog.Logger) (*routerHandler, error) {
	proxies := make(map[string]*httputil.ReverseProxy, len(cfg.AllowedRegions))
	for _, region := range cfg.AllowedRegions {
		upstream := regionUpstreamURL(region)
		u, err := url.Parse(upstream)
		if err != nil {
			return nil, fmt.Errorf("invalid upstream URL for region %s: %w", region, err)
		}
		proxies[region] = httputil.NewSingleHostReverseProxy(u)
	}
	return &routerHandler{cfg: cfg, store: store, proxies: proxies, tracer: tracer, log: log}, nil
}

// ServeHTTP is the routing entry point.
// Expected header: X-Janus-Tenant-ID (set by api-gateway after JWT validation).
func (h *routerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "region_router.route",
		trace.WithAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.path", r.URL.Path),
			attribute.String("local_region", h.cfg.LocalRegion),
		),
	)
	defer span.End()

	tenantID := r.Header.Get("X-Janus-Tenant-ID")
	if tenantID == "" {
		http.Error(w, `{"code":"missing_tenant","message":"X-Janus-Tenant-ID header required"}`, http.StatusBadRequest)
		return
	}

	tr, err := h.store.Get(ctx, tenantID)
	if err != nil {
		h.log.WarnContext(ctx, "residency lookup failed", "tenant_id", tenantID, "error", err)
		http.Error(w, `{"code":"tenant_not_found","message":"tenant not found"}`, http.StatusNotFound)
		return
	}

	targetRegion, reason := routeRequest(tr, h.cfg.LocalRegion, r)

	// Enforce: if the residency wall requires a different region and we ARE in the
	// wrong region, refuse. The client must retry against the correct regional endpoint.
	if reason == "data_residency" && targetRegion != h.cfg.LocalRegion {
		residencyViolations.WithLabelValues(tenantID, h.cfg.LocalRegion, targetRegion).Inc()
		span.SetAttributes(attribute.Bool("residency_violation", true))
		w.Header().Set("X-Janus-Required-Region", targetRegion)
		http.Error(w, fmt.Sprintf(`{"code":"wrong_region","message":"tenant data must be processed in %s","required_region":%q}`, targetRegion, targetRegion), http.StatusMisdirectedRequest)
		return
	}

	proxy, ok := h.proxies[targetRegion]
	if !ok {
		http.Error(w, `{"code":"no_upstream","message":"no upstream configured for target region"}`, http.StatusBadGateway)
		return
	}

	// Propagate routing metadata to upstream for audit logging.
	r = r.WithContext(ctx)
	r.Header.Set("X-Janus-Source-Region", h.cfg.LocalRegion)
	r.Header.Set("X-Janus-Target-Region", targetRegion)
	r.Header.Set("X-Janus-Route-Reason", reason)

	routeTotal.WithLabelValues(h.cfg.LocalRegion, targetRegion, reason).Inc()
	span.SetAttributes(
		attribute.String("target_region", targetRegion),
		attribute.String("route_reason", reason),
	)

	start := time.Now()
	proxy.ServeHTTP(w, r)
	routeLatency.WithLabelValues(targetRegion).Observe(time.Since(start).Seconds())

	h.log.InfoContext(ctx, "routed",
		"tenant_id", tenantID,
		"source", h.cfg.LocalRegion,
		"target", targetRegion,
		"reason", reason,
		"method", r.Method,
		"path", r.URL.Path,
	)
}

// regionUpstreamURL returns the internal URL for a region's api-gateway.
// In production this would resolve to the cross-region ALB or PrivateLink endpoint.
func regionUpstreamURL(region string) string {
	return fmt.Sprintf("https://api-gateway.%s.governance-internal.io", region)
}

// healthHandler returns 200 if the service is alive.
func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

// readyHandler checks DB connectivity before declaring ready.
func readyHandler(store *ResidencyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := store.db.PingContext(ctx); err != nil {
			http.Error(w, `{"status":"not_ready","reason":"db_unreachable"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ready"}`)
	}
}
