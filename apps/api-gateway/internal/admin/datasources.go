package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DataSourcesHandler handles /v1/admin/{tenantSlug}/data-sources/* routes.
type DataSourcesHandler struct {
	pool *pgxpool.Pool
}

func NewDataSourcesHandler(pool *pgxpool.Pool) *DataSourcesHandler {
	return &DataSourcesHandler{pool: pool}
}

type dataSourceRow struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Database  string    `json:"database"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

func (h *DataSourcesHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, span := tracer.Start(r.Context(), "admin.datasources.list")
	defer span.End()

	sess := SessionFromContext(ctx)
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, errBody("unauthorized", "session required"))
		return
	}

	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, fmt.Sprintf("SET LOCAL app.tenant_id = '%s'", sess.TenantID)); err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}

	rows, err := conn.Query(ctx, `
		SELECT id::text, tenant_id::text, name, kind,
		       COALESCE(host,'') AS host,
		       COALESCE(port, 5432) AS port,
		       COALESCE(database_name,'') AS database_name,
		       COALESCE(status,'disconnected') AS status,
		       created_at
		FROM data_sources
		WHERE tenant_id = $1
		ORDER BY created_at DESC`, sess.TenantID)
	if err != nil {
		span.RecordError(err)
		writeJSON(w, http.StatusInternalServerError, errBody("db_error", err.Error()))
		return
	}
	defer rows.Close()

	var items []dataSourceRow
	for rows.Next() {
		var ds dataSourceRow
		if err := rows.Scan(
			&ds.ID, &ds.TenantID, &ds.Name, &ds.Kind,
			&ds.Host, &ds.Port, &ds.Database, &ds.Status, &ds.CreatedAt,
		); err != nil {
			continue
		}
		items = append(items, ds)
	}
	if items == nil {
		items = []dataSourceRow{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}
