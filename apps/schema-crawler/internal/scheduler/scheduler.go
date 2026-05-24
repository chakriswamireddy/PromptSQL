// Package scheduler runs crawls on a 6-hour schedule per data source,
// with bounded concurrency and support for admin-triggered "crawl now."
package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/governance-platform/schema-crawler/internal/crawler"
	"github.com/governance-platform/schema-crawler/internal/store"
)

// VaultResolver resolves a Vault secret path to a DSN.
type VaultResolver func(ctx context.Context, secretRef string) (string, error)

// Scheduler manages the crawl schedule.
type Scheduler struct {
	db          *store.DB
	crawlerFn   *crawler.Crawler
	vaultResolve VaultResolver
	interval    time.Duration
	concurrency int
	log         zerolog.Logger

	// triggerCh allows admin-triggered crawls.
	triggerCh chan triggerMsg
}

type triggerMsg struct {
	tenantID     string
	dataSourceID string
}

// New creates a Scheduler.
func New(db *store.DB, c *crawler.Crawler, v VaultResolver, interval time.Duration, concurrency int, log zerolog.Logger) *Scheduler {
	return &Scheduler{
		db:           db,
		crawlerFn:    c,
		vaultResolve: v,
		interval:     interval,
		concurrency:  concurrency,
		log:          log,
		triggerCh:    make(chan triggerMsg, 64),
	}
}

// Trigger enqueues an on-demand crawl for a specific data source.
func (s *Scheduler) Trigger(tenantID, dataSourceID string) {
	select {
	case s.triggerCh <- triggerMsg{tenantID: tenantID, dataSourceID: dataSourceID}:
	default:
		s.log.Warn().Str("data_source_id", dataSourceID).Msg("trigger channel full; dropping on-demand crawl request")
	}
}

// Run starts the scheduling loop and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	// Run immediately on startup.
	s.crawlAll(ctx, "scheduler")

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.crawlAll(ctx, "scheduler")
		case msg := <-s.triggerCh:
			go s.crawlOne(ctx, msg.tenantID, msg.dataSourceID, "admin")
		}
	}
}

func (s *Scheduler) crawlAll(ctx context.Context, triggeredBy string) {
	// tenants table has no RLS — safe to query without tenant context.
	tenantIDs, err := s.db.ListTenantIDs(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("list tenant ids failed")
		return
	}

	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup

	for _, tenantID := range tenantIDs {
		dataSources, err := s.db.ListActiveDataSources(ctx, tenantID)
		if err != nil {
			s.log.Error().Err(err).Str("tenant_id", tenantID).Msg("list data sources failed")
			continue
		}
		for _, ds := range dataSources {
			wg.Add(1)
			sem <- struct{}{}
			go func(tenantID, dataSourceID, secretRef string) {
				defer wg.Done()
				defer func() { <-sem }()
				s.crawlOneWithSecret(ctx, tenantID, dataSourceID, secretRef, triggeredBy)
			}(ds.TenantID, ds.ID, ds.ConnectionSecretRef)
		}
	}
	wg.Wait()
}

func (s *Scheduler) crawlOne(ctx context.Context, tenantID, dataSourceID, triggeredBy string) {
	sources, err := s.db.ListActiveDataSources(ctx, tenantID)
	if err != nil {
		s.log.Error().Err(err).Str("data_source_id", dataSourceID).Msg("list data sources failed")
		return
	}
	for _, ds := range sources {
		if ds.ID == dataSourceID {
			s.crawlOneWithSecret(ctx, tenantID, dataSourceID, ds.ConnectionSecretRef, triggeredBy)
			return
		}
	}
	s.log.Warn().Str("data_source_id", dataSourceID).Msg("data source not found for on-demand crawl")
}

func (s *Scheduler) crawlOneWithSecret(ctx context.Context, tenantID, dataSourceID, secretRef, triggeredBy string) {
	dsn, err := s.vaultResolve(ctx, secretRef)
	if err != nil {
		s.log.Error().Err(err).Str("secret_ref", secretRef).Msg("vault resolve failed")
		return
	}

	if err := s.crawlerFn.Run(ctx, tenantID, dataSourceID, dsn, triggeredBy); err != nil {
		s.log.Error().Err(err).
			Str("tenant_id", tenantID).
			Str("data_source_id", dataSourceID).
			Msg("crawl run failed")
	}
}
