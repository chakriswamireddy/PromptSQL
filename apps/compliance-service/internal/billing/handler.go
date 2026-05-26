// Package billing handles Stripe subscription lifecycle and webhook events.
package billing

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/store"
)

var tracer = otel.Tracer("compliance-service/billing")

type Handler struct {
	db            *store.DB
	secretKey     string
	webhookSecret string
	log           *zap.Logger
}

func NewHandler(db *store.DB, secretKey, webhookSecret string, log *zap.Logger) *Handler {
	stripe.Key = secretKey
	return &Handler{db: db, secretKey: secretKey, webhookSecret: webhookSecret, log: log}
}

// Webhook handles POST /v1/billing/webhook from Stripe.
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "billing.Webhook")
	defer span.End()

	const maxBodyBytes = 65536
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), h.webhookSecret)
	if err != nil {
		h.log.Warn("stripe webhook signature invalid", zap.Error(err))
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	span.SetAttributes(attribute.String("stripe.event_type", string(event.Type)))
	h.log.Info("stripe webhook", zap.String("type", string(event.Type)), zap.String("id", event.ID))

	switch event.Type {
	case "customer.subscription.created", "customer.subscription.updated":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err == nil {
			h.upsertSubscription(ctx, &sub)
		}
	case "customer.subscription.deleted":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err == nil {
			h.db.Pool.Exec(ctx, //nolint
				`UPDATE billing_subscriptions SET status='canceled', updated_at=now()
				  WHERE stripe_sub_id=$1`, sub.ID)
		}
	case "invoice.payment_failed":
		var inv stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &inv); err == nil && inv.Subscription != nil {
			h.db.Pool.Exec(ctx, //nolint
				`UPDATE billing_subscriptions SET status='past_due', updated_at=now()
				  WHERE stripe_sub_id=$1`, inv.Subscription.ID)
		}
	}

	w.WriteHeader(http.StatusOK)
}

// GetSubscription handles GET /v1/admin/:tenant_id/billing/subscription
func (h *Handler) GetSubscription(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "billing.GetSubscription")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	type Row struct {
		TenantID          string    `json:"tenant_id"`
		StripeCustomerID  string    `json:"stripe_customer_id"`
		StripSubID        string    `json:"stripe_sub_id"`
		PlanTier          string    `json:"plan_tier"`
		Status            string    `json:"status"`
		PeriodStart       time.Time `json:"current_period_start"`
		PeriodEnd         time.Time `json:"current_period_end"`
		CancelAtPeriodEnd bool      `json:"cancel_at_period_end"`
		AIUsageUnits      int64     `json:"ai_usage_units"`
	}
	var row Row
	err := h.db.Pool.QueryRow(ctx,
		`SELECT tenant_id, stripe_customer_id, stripe_sub_id, plan_tier, status,
		        current_period_start, current_period_end, cancel_at_period_end, ai_usage_units
		   FROM billing_subscriptions WHERE tenant_id=$1`, tenantID).
		Scan(&row.TenantID, &row.StripeCustomerID, &row.StripSubID, &row.PlanTier, &row.Status,
			&row.PeriodStart, &row.PeriodEnd, &row.CancelAtPeriodEnd, &row.AIUsageUnits)
	if err != nil {
		row.TenantID = tenantID
		row.PlanTier = "design_partner"
		row.Status = "trialing"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(row) //nolint
}

func (h *Handler) upsertSubscription(ctx interface {
	Value(interface{}) interface{}
}, sub *stripe.Subscription) {
	// Resolve tenant from Stripe customer metadata.
	tenantID := sub.Metadata["tenant_id"]
	if tenantID == "" {
		return
	}
	tier := "starter"
	if sub.Metadata["plan_tier"] != "" {
		tier = sub.Metadata["plan_tier"]
	}

	ctx2 := r.Context()
	h.db.Pool.Exec(ctx2, //nolint
		`INSERT INTO billing_subscriptions
		    (tenant_id, stripe_customer_id, stripe_sub_id, plan_tier, status,
		     current_period_start, current_period_end, cancel_at_period_end)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		     stripe_sub_id         = EXCLUDED.stripe_sub_id,
		     plan_tier             = EXCLUDED.plan_tier,
		     status                = EXCLUDED.status,
		     current_period_start  = EXCLUDED.current_period_start,
		     current_period_end    = EXCLUDED.current_period_end,
		     cancel_at_period_end  = EXCLUDED.cancel_at_period_end,
		     updated_at            = now()`,
		tenantID, sub.Customer.ID, sub.ID, tier, string(sub.Status),
		time.Unix(sub.CurrentPeriodStart, 0),
		time.Unix(sub.CurrentPeriodEnd, 0),
		sub.CancelAtPeriodEnd,
	)
}
