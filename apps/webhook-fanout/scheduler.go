package main

import (
	"context"
	"math/rand"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
)

// Scheduler runs saved queries on their cron schedule.
type Scheduler struct {
	store *Store
	cfg   config
	log   zerolog.Logger
}

func newScheduler(store *Store, cfg config, log zerolog.Logger) *Scheduler {
	return &Scheduler{store: store, cfg: cfg, log: log}
}

// Run ticks every SchedulerInterval and dispatches due saved queries.
func (s *Scheduler) Run(ctx context.Context) {
	// Add jitter so multiple replicas don't all fire at once.
	jitter := time.Duration(rand.Int63n(int64(s.cfg.SchedulerJitter)))
	select {
	case <-time.After(jitter):
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(s.cfg.SchedulerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.tick(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	queries, err := s.store.ScheduledQueries(ctx)
	if err != nil {
		s.log.Error().Err(err).Msg("load scheduled queries")
		return
	}

	for _, q := range queries {
		go s.runQuery(ctx, q)
	}
}

func (s *Scheduler) runQuery(ctx context.Context, q savedQuestion) {
	_, span := tracer.Start(ctx, "scheduler.run_query")
	defer span.End()

	s.log.Info().
		Str("saved_question_id", q.ID).
		Str("tenant_id", q.TenantID).
		Str("cron", q.ScheduleCron).
		Msg("executing scheduled saved query")

	// Parse next run time.
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := parser.Parse(q.ScheduleCron)
	if err != nil {
		s.log.Error().Err(err).Str("cron", q.ScheduleCron).Msg("invalid cron expression")
		metricSavedQueryRuns.WithLabelValues(q.TenantID, "cron_error").Inc()
		return
	}

	nextRun := schedule.Next(time.Now())

	// Idempotency: check if this (question_id, scheduled_at window) was already run.
	// We use the store to update atomically.
	if err := s.store.MarkScheduledQueryRan(ctx, q.ID, nextRun); err != nil {
		s.log.Error().Err(err).Str("id", q.ID).Msg("mark scheduled query ran")
		metricSavedQueryRuns.WithLabelValues(q.TenantID, "db_error").Inc()
		return
	}

	// TODO(phase-12): call PEP graph HTTP endpoint to execute the saved query.
	// For now, emit an audit event stub and record the run.
	metricSavedQueryRuns.WithLabelValues(q.TenantID, "ok").Inc()
}
