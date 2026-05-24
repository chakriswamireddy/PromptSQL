package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// poolKey identifies a pool per (tenantID, dataSourceID).
type poolKey struct {
	tenantID     string
	dataSourceID string
}

// backendPool manages per-(tenant, datasource) PgBouncer-backed connection pools.
type backendPool struct {
	mu      sync.RWMutex
	pools   map[poolKey]*pgxpool.Pool
	cfg     config
	log     zerolog.Logger
	maxConn int
}

func newBackendPool(cfg config, log zerolog.Logger) *backendPool {
	return &backendPool{
		pools:   make(map[poolKey]*pgxpool.Pool),
		cfg:     cfg,
		log:     log,
		maxConn: cfg.PoolMaxConnsPro,
	}
}

// Acquire returns a connection from the pool for the given tenant and datasource.
// connStr must be a libpq-style DSN pointing at PgBouncer for the datasource.
func (p *backendPool) Acquire(ctx context.Context, key poolKey, connStr string) (*pgxpool.Conn, error) {
	start := time.Now()
	pool, err := p.getOrCreate(ctx, key, connStr)
	if err != nil {
		return nil, fmt.Errorf("pool create: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	proxyBackendPoolAcquireWait.WithLabelValues(key.tenantID, key.dataSourceID).
		Observe(time.Since(start).Seconds())
	if err != nil {
		return nil, fmt.Errorf("pool acquire: %w", err)
	}
	return conn, nil
}

func (p *backendPool) getOrCreate(ctx context.Context, key poolKey, connStr string) (*pgxpool.Pool, error) {
	p.mu.RLock()
	pool, ok := p.pools[key]
	p.mu.RUnlock()
	if ok {
		return pool, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if pool, ok = p.pools[key]; ok {
		return pool, nil
	}

	pcfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse pool config: %w", err)
	}
	pcfg.MaxConns = int32(p.maxConn)
	pcfg.MinConns = 2
	pcfg.MaxConnIdleTime = p.cfg.PoolIdleTimeout
	pcfg.MaxConnLifetime = 30 * time.Minute

	// Hook: after each connection acquired, enforce statement_timeout and idle_in_transaction_session_timeout.
	pcfg.AfterConnect = func(ctx context.Context, conn *pgxpool.Conn) error {
		_, err := conn.Exec(ctx, "SET statement_timeout = '30s'; SET idle_in_transaction_session_timeout = '60s'")
		return err
	}

	pool, err = pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	p.pools[key] = pool
	p.log.Info().
		Str("tenant_id", key.tenantID).
		Str("data_source_id", key.dataSourceID).
		Int("max_conns", p.maxConn).
		Msg("backend pool created")
	return pool, nil
}

// Close shuts down all managed pools gracefully.
func (p *backendPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, pool := range p.pools {
		pool.Close()
		p.log.Info().
			Str("tenant_id", key.tenantID).
			Str("data_source_id", key.dataSourceID).
			Msg("backend pool closed")
	}
}

// SweepOrphans closes pools that have had zero activity for longer than the idle timeout.
// Called every 30 s by the server's background goroutine.
func (p *backendPool) SweepOrphans() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, pool := range p.pools {
		stat := pool.Stat()
		if stat.AcquiredConns() == 0 && stat.IdleConns() == 0 {
			pool.Close()
			delete(p.pools, key)
			p.log.Info().
				Str("tenant_id", key.tenantID).
				Str("data_source_id", key.dataSourceID).
				Msg("orphan pool swept")
		}
	}
}
