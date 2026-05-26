// Package scim implements SCIM 2.0 (RFC 7644) for SSO provisioning.
package scim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/org/platform/apps/compliance-service/internal/store"
)

var tracer = otel.Tracer("compliance-service/scim")

const scimContentType = "application/scim+json"

type Handler struct {
	db  *store.DB
	log *zap.Logger
}

func NewHandler(db *store.DB, log *zap.Logger) *Handler {
	return &Handler{db: db, log: log}
}

// AuthMiddleware validates the Bearer token against scim_tokens.
func (h *Handler) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "scim.Auth")
		defer span.End()

		tenantID := r.PathValue("tenant_id")
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", scimContentType)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(scimError("unauthorized", "Bearer token required")) //nolint
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		hash := sha256hex(token)

		var tokenID string
		err := h.db.Pool.QueryRow(ctx,
			`SELECT id FROM scim_tokens
			  WHERE tenant_id = $1 AND token_hash = $2
			    AND revoked_at IS NULL
			    AND (expires_at IS NULL OR expires_at > now())`,
			tenantID, hash).Scan(&tokenID)
		if err != nil {
			span.SetAttributes(attribute.Bool("auth.ok", false))
			w.Header().Set("Content-Type", scimContentType)
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(scimError("unauthorized", "invalid or expired token")) //nolint
			return
		}

		// Update last_used_at asynchronously.
		go h.db.Pool.Exec(context.Background(), //nolint
			`UPDATE scim_tokens SET last_used_at = now() WHERE id = $1`, tokenID)

		next.ServeHTTP(w, r)
	})
}

// ServiceProviderConfig returns SCIM service provider capabilities.
func (h *Handler) ServiceProviderConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", scimContentType)
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":   map[string]bool{"supported": true},
		"bulk":    map[string]interface{}{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":  map[string]interface{}{"supported": true, "maxResults": 200},
		"sort":    map[string]bool{"supported": false},
		"etag":    map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]string{
			{"type": "oauthbearertoken", "name": "OAuth Bearer Token", "description": "Platform SCIM Bearer Token"},
		},
	})
}

// ListUsers handles GET /scim/v2/tenants/:tenant_id/Users
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "scim.ListUsers")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	span.SetAttributes(attribute.String("tenant_id", tenantID))

	rows, err := h.db.Pool.Query(ctx,
		`SELECT id, email, display_name, active, created_at FROM users WHERE tenant_id = $1 ORDER BY email`,
		tenantID)
	if err != nil {
		h.scimInternalError(w, err)
		return
	}
	defer rows.Close()

	var resources []map[string]interface{}
	for rows.Next() {
		var id, email, displayName string
		var active bool
		var createdAt time.Time
		rows.Scan(&id, &email, &displayName, &active, &createdAt) //nolint
		resources = append(resources, scimUser(id, email, displayName, active, createdAt, tenantID))
	}
	if resources == nil {
		resources = []map[string]interface{}{}
	}

	w.Header().Set("Content-Type", scimContentType)
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": len(resources),
		"startIndex":   1,
		"itemsPerPage": len(resources),
		"Resources":    resources,
	})
}

// CreateUser handles POST /scim/v2/tenants/:tenant_id/Users
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "scim.CreateUser")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	var body struct {
		UserName    string `json:"userName"`
		DisplayName string `json:"displayName"`
		Active      *bool  `json:"active"`
		Emails      []struct {
			Value   string `json:"value"`
			Primary bool   `json:"primary"`
		} `json:"emails"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.scimBadRequest(w, "invalid body")
		return
	}

	email := body.UserName
	for _, e := range body.Emails {
		if e.Primary {
			email = e.Value
			break
		}
	}
	active := true
	if body.Active != nil {
		active = *body.Active
	}

	var userID string
	err := h.db.Pool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, display_name, active)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, email) DO UPDATE SET display_name=EXCLUDED.display_name, active=EXCLUDED.active
		 RETURNING id, created_at`,
		tenantID, email, body.DisplayName, active).Scan(&userID, new(time.Time))
	if err != nil {
		h.scimInternalError(w, err)
		return
	}

	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(scimUser(userID, email, body.DisplayName, active, time.Now(), tenantID)) //nolint
	span.SetAttributes(attribute.String("user_id", userID))
}

func (h *Handler) GetUser(w http.ResponseWriter, r *http.Request)     { h.userByID(w, r) }
func (h *Handler) ReplaceUser(w http.ResponseWriter, r *http.Request) { h.CreateUser(w, r) }

// PatchUser handles PATCH for activate/deactivate.
func (h *Handler) PatchUser(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "scim.PatchUser")
	defer span.End()

	tenantID := r.PathValue("tenant_id")
	userID := r.PathValue("user_id")

	var body struct {
		Operations []struct {
			Op    string      `json:"op"`
			Path  string      `json:"path"`
			Value interface{} `json:"value"`
		} `json:"Operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.scimBadRequest(w, "invalid patch body")
		return
	}

	for _, op := range body.Operations {
		if strings.EqualFold(op.Path, "active") {
			active := op.Value == true || op.Value == "true"
			h.db.Pool.Exec(ctx, //nolint
				`UPDATE users SET active = $1 WHERE id = $2 AND tenant_id = $3`,
				active, userID, tenantID)
		}
	}

	h.userByID(w, r)
	_ = span
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx, _ := tracer.Start(r.Context(), "scim.DeleteUser")
	tenantID := r.PathValue("tenant_id")
	userID := r.PathValue("user_id")
	h.db.Pool.Exec(ctx, `UPDATE users SET active=false WHERE id=$1 AND tenant_id=$2`, userID, tenantID) //nolint
	w.WriteHeader(http.StatusNoContent)
}

// Groups — minimal implementation (platform uses roles, not groups natively).
func (h *Handler) ListGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", scimContentType)
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": 0, "startIndex": 1, "itemsPerPage": 0, "Resources": []interface{}{},
	})
}
func (h *Handler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(http.StatusNotImplemented)
}
func (h *Handler) GetGroup(w http.ResponseWriter, r *http.Request) {
	h.scimNotFound(w, "group not found")
}
func (h *Handler) ReplaceGroup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(http.StatusNotImplemented)
}
func (h *Handler) PatchGroup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(http.StatusNotImplemented)
}
func (h *Handler) DeleteGroup(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }

// Token management.
func (h *Handler) CreateToken(w http.ResponseWriter, r *http.Request) {
	ctx, _ := tracer.Start(r.Context(), "scim.CreateToken")
	tenantID := r.PathValue("tenant_id")

	var body struct {
		Label     string `json:"label"`
		CreatedBy string `json:"created_by"`
		ExpiresIn *int   `json:"expires_in_days"`
	}
	json.NewDecoder(r.Body).Decode(&body) //nolint

	rawToken := generateToken()
	hash := sha256hex(rawToken)

	var expiresAt interface{}
	if body.ExpiresIn != nil && *body.ExpiresIn > 0 {
		expiresAt = time.Now().AddDate(0, 0, *body.ExpiresIn)
	}

	var tokenID string
	h.db.Pool.QueryRow(ctx,
		`INSERT INTO scim_tokens (tenant_id, token_hash, label, created_by, expires_at)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		tenantID, hash, body.Label, body.CreatedBy, expiresAt).Scan(&tokenID) //nolint

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": tokenID, "token": rawToken}) //nolint
}

func (h *Handler) ListTokens(w http.ResponseWriter, r *http.Request) {
	ctx, _ := tracer.Start(r.Context(), "scim.ListTokens")
	tenantID := r.PathValue("tenant_id")
	rows, _ := h.db.Pool.Query(ctx,
		`SELECT id, label, last_used_at, expires_at, created_at FROM scim_tokens
		  WHERE tenant_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, tenantID)
	if rows != nil {
		defer rows.Close()
	}

	type T struct {
		ID         string     `json:"id"`
		Label      string     `json:"label"`
		LastUsedAt *time.Time `json:"last_used_at,omitempty"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
		CreatedAt  time.Time  `json:"created_at"`
	}
	var tokens []T
	if rows != nil {
		for rows.Next() {
			var t T
			rows.Scan(&t.ID, &t.Label, &t.LastUsedAt, &t.ExpiresAt, &t.CreatedAt) //nolint
			tokens = append(tokens, t)
		}
	}
	if tokens == nil {
		tokens = []T{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tokens": tokens}) //nolint
}

func (h *Handler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	ctx, _ := tracer.Start(r.Context(), "scim.RevokeToken")
	tenantID := r.PathValue("tenant_id")
	tokenID := r.PathValue("token_id")
	h.db.Pool.Exec(ctx, `UPDATE scim_tokens SET revoked_at=now() WHERE id=$1 AND tenant_id=$2`, tokenID, tenantID) //nolint
	w.WriteHeader(http.StatusNoContent)
}

// helpers

func (h *Handler) userByID(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := r.PathValue("tenant_id")
	userID := r.PathValue("user_id")
	var email, displayName string
	var active bool
	var createdAt time.Time
	err := h.db.Pool.QueryRow(ctx,
		`SELECT email, display_name, active, created_at FROM users WHERE id=$1 AND tenant_id=$2`,
		userID, tenantID).Scan(&email, &displayName, &active, &createdAt)
	if err != nil {
		h.scimNotFound(w, "user not found")
		return
	}
	w.Header().Set("Content-Type", scimContentType)
	json.NewEncoder(w).Encode(scimUser(userID, email, displayName, active, createdAt, tenantID)) //nolint
}

func scimUser(id, email, displayName string, active bool, createdAt time.Time, tenantID string) map[string]interface{} {
	return map[string]interface{}{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"id":          id,
		"userName":    email,
		"displayName": displayName,
		"active":      active,
		"emails":      []map[string]interface{}{{"value": email, "primary": true}},
		"meta": map[string]string{
			"resourceType": "User",
			"created":      createdAt.Format(time.RFC3339),
			"location":     "/v1/scim/v2/tenants/" + tenantID + "/Users/" + id,
		},
	}
}

func scimError(scimType, detail string) map[string]interface{} {
	return map[string]interface{}{
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		"scimType": scimType, "detail": detail, "status": "400",
	}
}

func (h *Handler) scimInternalError(w http.ResponseWriter, err error) {
	h.log.Error("scim internal", zap.Error(err))
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		"detail": "internal server error", "status": "500",
	})
}

func (h *Handler) scimBadRequest(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(scimError("invalidValue", msg)) //nolint
}

func (h *Handler) scimNotFound(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", scimContentType)
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]interface{}{ //nolint
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		"detail": msg, "status": "404",
	})
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b) //nolint
	return hex.EncodeToString(b)
}
