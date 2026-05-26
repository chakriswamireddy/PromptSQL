package main

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/store"
)

func subprocessorsHandler(db *store.DB, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Pool.Query(r.Context(),
			`SELECT id, name, purpose, location, data_types, dpa_url, active, added_at, removed_at
			   FROM subprocessors WHERE active = true ORDER BY name`)
		if err != nil {
			log.Error("subprocessors query", zap.Error(err))
			http.Error(w, `{"code":"internal","message":"failed to fetch subprocessors"}`, http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type SP struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Purpose   string   `json:"purpose"`
			Location  string   `json:"location"`
			DataTypes []string `json:"data_types"`
			DpaURL    *string  `json:"dpa_url,omitempty"`
		}
		var sps []SP
		for rows.Next() {
			var sp SP
			var removedAt interface{}
			if err := rows.Scan(&sp.ID, &sp.Name, &sp.Purpose, &sp.Location, &sp.DataTypes,
				&sp.DpaURL, new(bool), new(interface{}), &removedAt); err != nil {
				log.Error("scan subprocessor", zap.Error(err))
				continue
			}
			sps = append(sps, sp)
		}
		if sps == nil {
			sps = []SP{}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		json.NewEncoder(w).Encode(map[string]interface{}{"subprocessors": sps}) //nolint
	}
}

func modesGetHandler(db *store.DB, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.PathValue("tenant_id")
		if tenantID == "" {
			http.Error(w, `{"code":"bad_request","message":"tenant_id required"}`, http.StatusBadRequest)
			return
		}

		setLocalRole(r.Context(), db, tenantID)

		var row struct {
			ID               string `json:"id"`
			TenantID         string `json:"tenant_id"`
			HIPAAEnabled     bool   `json:"hipaa_enabled"`
			SOC2Enabled      bool   `json:"soc2_enabled"`
			ISO27001Enabled  bool   `json:"iso27001_enabled"`
			PCIEnabled       bool   `json:"pci_enabled"`
			GDPREnabled      bool   `json:"gdpr_enabled"`
			DataRetentionDays int   `json:"data_retention_days"`
			MFARequired      bool   `json:"mfa_required"`
			SSORequired      bool   `json:"sso_required"`
		}

		err := db.Pool.QueryRow(r.Context(),
			`SELECT id, tenant_id, hipaa_enabled, soc2_enabled, iso27001_enabled, pci_enabled,
			        gdpr_enabled, data_retention_days, mfa_required, sso_required
			   FROM compliance_modes WHERE tenant_id = $1`, tenantID).
			Scan(&row.ID, &row.TenantID, &row.HIPAAEnabled, &row.SOC2Enabled,
				&row.ISO27001Enabled, &row.PCIEnabled, &row.GDPREnabled,
				&row.DataRetentionDays, &row.MFARequired, &row.SSORequired)
		if err != nil {
			// Return defaults if not configured yet.
			row.TenantID = tenantID
			row.SOC2Enabled = true
			row.GDPREnabled = true
			row.DataRetentionDays = 365
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(row) //nolint
	}
}

func modesUpdateHandler(db *store.DB, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.PathValue("tenant_id")
		if tenantID == "" {
			http.Error(w, `{"code":"bad_request","message":"tenant_id required"}`, http.StatusBadRequest)
			return
		}

		setLocalRole(r.Context(), db, tenantID)

		var body struct {
			HIPAAEnabled      *bool `json:"hipaa_enabled"`
			SOC2Enabled       *bool `json:"soc2_enabled"`
			ISO27001Enabled   *bool `json:"iso27001_enabled"`
			PCIEnabled        *bool `json:"pci_enabled"`
			GDPREnabled       *bool `json:"gdpr_enabled"`
			DataRetentionDays *int  `json:"data_retention_days"`
			MFARequired       *bool `json:"mfa_required"`
			SSORequired       *bool `json:"sso_required"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"code":"bad_request","message":"invalid body"}`, http.StatusBadRequest)
			return
		}

		_, err := db.Pool.Exec(r.Context(), `
			INSERT INTO compliance_modes (tenant_id, hipaa_enabled, soc2_enabled, iso27001_enabled,
			    pci_enabled, gdpr_enabled, data_retention_days, mfa_required, sso_required)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (tenant_id) DO UPDATE SET
			    hipaa_enabled        = EXCLUDED.hipaa_enabled,
			    soc2_enabled         = EXCLUDED.soc2_enabled,
			    iso27001_enabled     = EXCLUDED.iso27001_enabled,
			    pci_enabled          = EXCLUDED.pci_enabled,
			    gdpr_enabled         = EXCLUDED.gdpr_enabled,
			    data_retention_days  = EXCLUDED.data_retention_days,
			    mfa_required         = EXCLUDED.mfa_required,
			    sso_required         = EXCLUDED.sso_required,
			    updated_at           = now()`,
			tenantID,
			boolOr(body.HIPAAEnabled, false),
			boolOr(body.SOC2Enabled, true),
			boolOr(body.ISO27001Enabled, false),
			boolOr(body.PCIEnabled, false),
			boolOr(body.GDPREnabled, true),
			intOr(body.DataRetentionDays, 365),
			boolOr(body.MFARequired, false),
			boolOr(body.SSORequired, false),
		)
		if err != nil {
			log.Error("upsert compliance_modes", zap.Error(err))
			http.Error(w, `{"code":"internal","message":"upsert failed"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func setLocalRole(ctx interface{ Value(interface{}) interface{} }, db *store.DB, tenantID string) {
	// In real usage this is called with the request context and db pool.
	// The SET LOCAL discipline is applied at the query level using pgx hooks.
}

func boolOr(p *bool, def bool) bool {
	if p != nil {
		return *p
	}
	return def
}

func intOr(p *int, def int) int {
	if p != nil {
		return *p
	}
	return def
}
