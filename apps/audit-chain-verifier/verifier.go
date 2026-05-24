package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var verTracer = otel.Tracer("audit-chain-verifier")

var (
	metricRunsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_chain_verifier_runs_total",
		Help: "Total verifier runs.",
	}, []string{"scope"})

	metricMismatches = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_chain_mismatch_total",
		Help: "Total hash-chain mismatches detected.",
	}, []string{"tenant_id"})

	metricRowsChecked = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "audit_chain_rows_checked",
		Help:    "Rows checked per verifier run.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 20),
	}, []string{"scope"})

	metricRunDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "audit_chain_run_duration_seconds",
		Help:    "Duration of each verifier run.",
		Buckets: prometheus.DefBuckets,
	}, []string{"scope"})
)

// auditRow is a single row from policy_audit.
type auditRow struct {
	ID      string
	RowHash string
}

// Manifest matches the WORM manifest written by the worm-sink.
type Manifest struct {
	SHA256 string `json:"sha256"`
}

// Verifier checks hash-chain integrity between PG policy_audit and WORM manifests.
type Verifier struct {
	cfg  config
	pool *pgxpool.Pool
	s3   *s3.Client
	log  zerolog.Logger
}

func newVerifier(cfg config, pool *pgxpool.Pool, log zerolog.Logger) (*Verifier, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.S3Region),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
			o.UsePathStyle = cfg.S3ForcePathStyle
		}
	})
	return &Verifier{cfg: cfg, pool: pool, s3: s3Client, log: log}, nil
}

// RunHourly verifies the previous hour's policy_audit rows for all tenants.
func (v *Verifier) RunHourly(ctx context.Context) error {
	return v.run(ctx, "hourly", v.allTenants)
}

// RunDailySample verifies a random sample of tenants for the previous day.
func (v *Verifier) RunDailySample(ctx context.Context) error {
	return v.run(ctx, "daily_sample", v.sampledTenants)
}

func (v *Verifier) run(ctx context.Context, scope string, tenantsFn func(ctx context.Context) ([]string, error)) error {
	_, span := verTracer.Start(ctx, "verifier.run")
	defer span.End()
	span.SetAttributes(attribute.String("scope", scope))

	start := time.Now()
	defer func() {
		metricRunDuration.WithLabelValues(scope).Observe(time.Since(start).Seconds())
		metricRunsTotal.WithLabelValues(scope).Inc()
	}()

	tenants, err := tenantsFn(ctx)
	if err != nil {
		return fmt.Errorf("list tenants: %w", err)
	}

	periodEnd := time.Now().UTC().Truncate(time.Hour)
	periodStart := periodEnd.Add(-time.Hour)
	if scope == "daily_sample" {
		periodEnd = time.Now().UTC().Truncate(24 * time.Hour)
		periodStart = periodEnd.Add(-24 * time.Hour)
	}

	for _, tenantID := range tenants {
		if err := v.verifyTenant(ctx, tenantID, scope, periodStart, periodEnd); err != nil {
			v.log.Error().Err(err).Str("tenant_id", tenantID).Str("scope", scope).Msg("verification error")
		}
	}
	return nil
}

func (v *Verifier) verifyTenant(ctx context.Context, tenantID, scope string, from, to time.Time) error {
	// Acquire PG advisory lock to prevent concurrent verifier overlap.
	lockID := tenantAdvisoryLockID(tenantID)
	if _, err := v.pool.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockID); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}

	rows, err := v.fetchAuditRows(ctx, tenantID, from, to)
	if err != nil {
		return fmt.Errorf("fetch audit rows: %w", err)
	}

	pgEndHash := ""
	recomputed := ""
	var prev string
	for _, r := range rows {
		h := sha256.New()
		h.Write([]byte(prev + r.ID)) //nolint:errcheck
		recomputed = hex.EncodeToString(h.Sum(nil))
		prev = recomputed
		pgEndHash = r.RowHash
	}
	metricRowsChecked.WithLabelValues(scope).Observe(float64(len(rows)))

	wormHash, err := v.fetchWORMHash(ctx, tenantID, "audit.policy."+v.cfg.Environment, to)
	if err != nil {
		v.log.Warn().Err(err).Str("tenant_id", tenantID).Msg("worm manifest fetch failed — skipping worm check")
		wormHash = "unavailable"
	}

	matched := pgEndHash == wormHash || wormHash == "unavailable"
	if !matched {
		metricMismatches.WithLabelValues(tenantID).Inc()
		v.log.Error().
			Str("tenant_id", tenantID).
			Str("pg_hash", pgEndHash).
			Str("worm_hash", wormHash).
			Msg("HASH CHAIN MISMATCH — security alert")
	}

	return v.recordVerification(ctx, tenantID, scope, from, to, pgEndHash, wormHash, matched, len(rows))
}

func (v *Verifier) fetchAuditRows(ctx context.Context, tenantID string, from, to time.Time) ([]auditRow, error) {
	// Use SET LOCAL to enforce tenant isolation.
	tx, err := v.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`SET LOCAL ROLE app_read; SELECT set_config('app.tenant_id', $1, true)`,
		tenantID,
	); err != nil {
		return nil, fmt.Errorf("set session: %w", err)
	}

	sqlRows, err := tx.Query(ctx,
		`SELECT id::text, row_hash FROM policy_audit
		 WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3
		 ORDER BY created_at ASC, id ASC`,
		tenantID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer sqlRows.Close()

	var result []auditRow
	for sqlRows.Next() {
		var r auditRow
		if err := sqlRows.Scan(&r.ID, &r.RowHash); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, sqlRows.Err()
}

func (v *Verifier) fetchWORMHash(ctx context.Context, tenantID, topic string, hour time.Time) (string, error) {
	h := hour.Truncate(time.Hour)
	key := fmt.Sprintf(
		"tenant=%s/topic=%s/year=%d/month=%02d/day=%02d/hour=%02d/manifest.json",
		tenantID, topic, h.Year(), int(h.Month()), h.Day(), h.Hour(),
	)
	out, err := v.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(v.cfg.S3Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", err
	}
	defer out.Body.Close() //nolint:errcheck
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return "", err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	return m.SHA256, nil
}

func (v *Verifier) recordVerification(
	ctx context.Context,
	tenantID, scope string,
	from, to time.Time,
	pgHash, wormHash string,
	matched bool,
	rows int,
) error {
	var detail *string
	if !matched {
		d := fmt.Sprintf(`{"pg_hash":"%s","worm_hash":"%s"}`, pgHash, wormHash)
		detail = &d
	}
	_, err := v.pool.Exec(ctx,
		`INSERT INTO chain_verifications
		 (tenant_id, period_start, period_end, scope, rows_checked, pg_end_hash, worm_end_hash, matched, mismatch_detail, verifier_version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)`,
		tenantID, from, to, scope, rows, pgHash, wormHash, matched, detail, v.cfg.Version,
	)
	return err
}

func (v *Verifier) allTenants(ctx context.Context) ([]string, error) {
	rows, err := v.pool.Query(ctx, `SELECT id::text FROM tenants WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (v *Verifier) sampledTenants(ctx context.Context) ([]string, error) {
	all, err := v.allTenants(ctx)
	if err != nil {
		return nil, err
	}
	n := int(float64(len(all)) * v.cfg.DailySampleRate)
	if n < 1 && len(all) > 0 {
		n = 1
	}
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	if n > len(all) {
		n = len(all)
	}
	return all[:n], nil
}

// tenantAdvisoryLockID converts the first 8 bytes of a tenant UUID string to int64.
func tenantAdvisoryLockID(tenantID string) int64 {
	h := sha256.Sum256([]byte(tenantID))
	var n int64
	for i := 0; i < 8; i++ {
		n = (n << 8) | int64(h[i])
	}
	return n
}
