package baseline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists and loads UserBaseline checkpoints to/from PostgreSQL.
// Redis is the hot path; PostgreSQL is the durable checkpoint.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a Store backed by the given connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Load retrieves the baseline for (tenantID, userID). Returns nil if not found.
func (s *Store) Load(ctx context.Context, tenantID, userID string) (*UserBaseline, error) {
	const q = `
		SELECT hour_histogram, dow_histogram, resource_set, row_count_quantiles,
		       ip_set, datasource_set, event_count, warm_up_done, last_updated_at
		FROM risk_baselines
		WHERE tenant_id = $1 AND user_id = $2`

	row := s.pool.QueryRow(ctx, q, tenantID, userID)

	var (
		hourJSON, dowJSON, resourceJSON, rcQuantilesJSON []byte
		ipJSON, dsJSON                                   []byte
		eventCount                                       int64
		warmUpDone                                       bool
		lastUpdatedAt                                    time.Time
	)
	err := row.Scan(
		&hourJSON, &dowJSON, &resourceJSON, &rcQuantilesJSON,
		&ipJSON, &dsJSON, &eventCount, &warmUpDone, &lastUpdatedAt,
	)
	if err != nil {
		return nil, nil // not found
	}

	b := NewBaseline(tenantID, userID)
	b.EventCount = eventCount
	b.WarmUpDone = warmUpDone
	b.LastUpdatedAt = lastUpdatedAt

	var hourMap map[string]float64
	if err := json.Unmarshal(hourJSON, &hourMap); err == nil {
		for k, v := range hourMap {
			var idx int
			if _, err := fmt.Sscanf(k, "%d", &idx); err == nil && idx >= 0 && idx < 24 {
				b.HourHist[idx] = v
			}
		}
	}

	var dowMap map[string]float64
	if err := json.Unmarshal(dowJSON, &dowMap); err == nil {
		for k, v := range dowMap {
			var idx int
			if _, err := fmt.Sscanf(k, "%d", &idx); err == nil && idx >= 0 && idx < 7 {
				b.DOWHist[idx] = v
			}
		}
	}

	var resourceList []struct {
		Hash string `json:"h"`
		TS   int64  `json:"t"`
	}
	if err := json.Unmarshal(resourceJSON, &resourceList); err == nil {
		for _, r := range resourceList {
			b.ResourceSet[r.Hash] = r.TS
		}
	}

	var ipList []struct {
		Hash string `json:"h"`
		TS   int64  `json:"t"`
	}
	if err := json.Unmarshal(ipJSON, &ipList); err == nil {
		for _, ip := range ipList {
			b.IPSet[ip.Hash] = ip.TS
		}
	}

	if err := json.Unmarshal(dsJSON, &b.DataSourceCounts); err != nil {
		b.DataSourceCounts = make(map[string]float64)
	}

	var rcQuantiles map[string]float64
	if err := json.Unmarshal(rcQuantilesJSON, &rcQuantiles); err == nil {
		b.RowCountMean, _ = rcQuantiles["mean"]
		b.RowCountM2, _ = rcQuantiles["m2"]
	}

	return b, nil
}

// Upsert checkpoints the baseline to PostgreSQL.
func (s *Store) Upsert(ctx context.Context, b *UserBaseline) error {
	hourMap := make(map[string]float64, 24)
	for i, v := range b.HourHist {
		if v > 0 {
			hourMap[fmt.Sprintf("%d", i)] = v
		}
	}

	dowMap := make(map[string]float64, 7)
	for i, v := range b.DOWHist {
		if v > 0 {
			dowMap[fmt.Sprintf("%d", i)] = v
		}
	}

	type kv struct {
		Hash string `json:"h"`
		TS   int64  `json:"t"`
	}
	resourceList := make([]kv, 0, len(b.ResourceSet))
	for h, ts := range b.ResourceSet {
		resourceList = append(resourceList, kv{Hash: h, TS: ts})
	}

	ipList := make([]kv, 0, len(b.IPSet))
	for h, ts := range b.IPSet {
		ipList = append(ipList, kv{Hash: h, TS: ts})
	}

	rcQuantiles := map[string]float64{
		"mean": b.RowCountMean,
		"m2":   b.RowCountM2,
	}

	hourJSON, _ := json.Marshal(hourMap)
	dowJSON, _ := json.Marshal(dowMap)
	resourceJSON, _ := json.Marshal(resourceList)
	rcJSON, _ := json.Marshal(rcQuantiles)
	ipJSON, _ := json.Marshal(ipList)
	dsJSON, _ := json.Marshal(b.DataSourceCounts)

	const q = `
		INSERT INTO risk_baselines
			(tenant_id, user_id, hour_histogram, dow_histogram, resource_set,
			 row_count_quantiles, ip_set, datasource_set, event_count, warm_up_done, last_updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (tenant_id, user_id) DO UPDATE SET
			hour_histogram      = EXCLUDED.hour_histogram,
			dow_histogram       = EXCLUDED.dow_histogram,
			resource_set        = EXCLUDED.resource_set,
			row_count_quantiles = EXCLUDED.row_count_quantiles,
			ip_set              = EXCLUDED.ip_set,
			datasource_set      = EXCLUDED.datasource_set,
			event_count         = EXCLUDED.event_count,
			warm_up_done        = EXCLUDED.warm_up_done,
			last_updated_at     = EXCLUDED.last_updated_at`

	_, err := s.pool.Exec(ctx, q,
		b.TenantID, b.UserID,
		hourJSON, dowJSON, resourceJSON, rcJSON, ipJSON, dsJSON,
		b.EventCount, b.WarmUpDone, b.LastUpdatedAt,
	)
	return err
}
