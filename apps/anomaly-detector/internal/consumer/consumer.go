// Package consumer reads audit.access events from Kafka and drives the scoring pipeline.
package consumer

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/rs/zerolog"
	kafka "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/governance-platform/anomaly-detector/internal/baseline"
	"github.com/governance-platform/anomaly-detector/internal/metrics"
	"github.com/governance-platform/anomaly-detector/internal/scoring"
	"github.com/governance-platform/anomaly-detector/internal/sink"
)

var tracer = otel.Tracer("anomaly-detector")

// Config holds all dependencies for the Consumer.
type Config struct {
	KafkaBrokers    []string
	Topic           string
	ConsumerGroup   string
	BatchSize       int
	BatchTimeout    time.Duration
	BaselineStore   *baseline.Store
	Redis           *sink.RedisSink
	Kafka           *sink.KafkaSink
	WarmupDays      int
	DecayHalfLife   float64
	FlushPeriod     time.Duration
	DefaultWeights  map[string]float64
	Log             zerolog.Logger
}

// Consumer drives the end-to-end scoring pipeline for a single Kafka topic partition group.
type Consumer struct {
	cfg     Config
	reader  *kafka.Reader

	// In-memory baselines: key = "tenantID:userID"
	mu        sync.RWMutex
	baselines map[string]*baseline.UserBaseline

	// Dirty set: baselines updated since last flush
	dirty map[string]struct{}

	// Per-user last score time (for decay calculation)
	lastScoreTime map[string]time.Time
	prevScores    map[string]int
}

// New creates a Consumer wired to the given topic.
func New(cfg Config) *Consumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.KafkaBrokers,
		GroupID:        cfg.ConsumerGroup,
		Topic:          cfg.Topic,
		MinBytes:       1,
		MaxBytes:       10 << 20,
		CommitInterval: time.Second,
		StartOffset:    kafka.LastOffset,
	})

	return &Consumer{
		cfg:           cfg,
		reader:        reader,
		baselines:     make(map[string]*baseline.UserBaseline),
		dirty:         make(map[string]struct{}),
		lastScoreTime: make(map[string]time.Time),
		prevScores:    make(map[string]int),
	}
}

// Run starts the consumer loop and the baseline flush ticker. Blocks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) {
	go c.flushLoop(ctx)

	batch := make([]kafka.Message, 0, c.cfg.BatchSize)
	timer := time.NewTimer(c.cfg.BatchTimeout)
	defer timer.Stop()

	flush := func() {
		if len(batch) > 0 {
			c.processBatch(ctx, batch)
			batch = batch[:0]
		}
		timer.Reset(c.cfg.BatchTimeout)
	}

	for {
		pollCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		msg, err := c.reader.FetchMessage(pollCtx)
		cancel()

		if err != nil {
			select {
			case <-ctx.Done():
				flush()
				return
			case <-timer.C:
				flush()
			default:
			}
			continue
		}

		batch = append(batch, msg)

		select {
		case <-timer.C:
			flush()
		default:
			if len(batch) >= c.cfg.BatchSize {
				flush()
			}
		}

		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			c.cfg.Log.Error().Err(err).Msg("commit failed")
		}
	}
}

func (c *Consumer) processBatch(ctx context.Context, msgs []kafka.Message) {
	_, span := tracer.Start(ctx, "anomaly.process_batch")
	defer span.End()
	span.SetAttributes(attribute.Int("batch.size", len(msgs)))

	for _, m := range msgs {
		var ev baseline.AccessEvent
		if err := json.Unmarshal(m.Value, &ev); err != nil {
			c.cfg.Log.Warn().Err(err).Msg("skip malformed access event")
			continue
		}
		c.scoreEvent(ctx, ev)
		metrics.KafkaLag.WithLabelValues(c.cfg.Topic).Set(time.Since(ev.EventTime).Seconds())
	}
}

func (c *Consumer) scoreEvent(ctx context.Context, ev baseline.AccessEvent) {
	start := time.Now()

	metrics.EventsConsumed.WithLabelValues(ev.TenantID).Inc()

	key := ev.TenantID + ":" + ev.UserID

	c.mu.Lock()
	b, ok := c.baselines[key]
	if !ok {
		// Load from DB or create fresh.
		loaded, err := c.cfg.BaselineStore.Load(ctx, ev.TenantID, ev.UserID)
		if err != nil || loaded == nil {
			loaded = baseline.NewBaseline(ev.TenantID, ev.UserID)
		}
		b = loaded
		c.baselines[key] = b
	}

	features := baseline.Extract(ev)
	b.Update(features, ev.EventTime)
	c.dirty[key] = struct{}{}

	// Check warm-up.
	warmupThreshold := int64(c.cfg.WarmupDays) * 24 * 3600 // seconds
	warmupDone := b.EventCount >= 20 && (ev.EventTime.Unix()-b.LastUpdatedAt.Add(-time.Duration(b.EventCount)*time.Hour).Unix()) > warmupThreshold
	if !b.WarmUpDone && warmupDone {
		b.WarmUpDone = true
	}

	if !b.WarmUpDone {
		c.mu.Unlock()
		metrics.EventsSkippedWarmup.WithLabelValues(ev.TenantID).Inc()
		return
	}

	zscores := b.ZScores(features)
	prevScore := c.prevScores[key]
	lastTime, hasLast := c.lastScoreTime[key]
	c.mu.Unlock()

	if zscores == nil {
		metrics.EventsSkippedWarmup.WithLabelValues(ev.TenantID).Inc()
		return
	}

	elapsed := time.Duration(0)
	if hasLast {
		elapsed = time.Since(lastTime)
	}

	sensitivityMult := baseline.SensitivityMultiplier(ev.Classification)
	rs := scoring.Compute(
		ev.TenantID, ev.UserID,
		zscores, c.cfg.DefaultWeights,
		sensitivityMult,
		prevScore,
		c.cfg.DecayHalfLife,
		elapsed,
	)

	c.mu.Lock()
	c.prevScores[key] = rs.DecayedTotal
	c.lastScoreTime[key] = rs.ComputedAt
	c.mu.Unlock()

	// Write to Redis (hot path).
	if err := c.cfg.Redis.Write(ctx, rs); err != nil {
		c.cfg.Log.Error().Err(err).Str("user", ev.UserID).Msg("redis write failed")
		metrics.RedisWriteErrors.Inc()
	}

	// Publish to risk.scored topic.
	if err := c.cfg.Kafka.Publish(ctx, rs); err != nil {
		c.cfg.Log.Error().Err(err).Str("user", ev.UserID).Msg("kafka publish failed")
		metrics.KafkaPublishErrors.Inc()
	}

	metrics.EventsScored.WithLabelValues(ev.TenantID).Inc()
	if rs.DecayedTotal >= 70 {
		metrics.ScoreHigh.WithLabelValues(ev.TenantID).Inc()
	}
	metrics.ScoringDuration.Observe(time.Since(start).Seconds())
}

// flushLoop periodically checkpoints dirty baselines to PostgreSQL.
func (c *Consumer) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.FlushPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.flushDirty(context.Background())
			return
		case <-ticker.C:
			c.flushDirty(ctx)
		}
	}
}

func (c *Consumer) flushDirty(ctx context.Context) {
	start := time.Now()

	c.mu.Lock()
	toFlush := make([]*baseline.UserBaseline, 0, len(c.dirty))
	for key := range c.dirty {
		if b, ok := c.baselines[key]; ok {
			toFlush = append(toFlush, b)
		}
	}
	c.dirty = make(map[string]struct{})
	c.mu.Unlock()

	for _, b := range toFlush {
		if err := c.cfg.BaselineStore.Upsert(ctx, b); err != nil {
			c.cfg.Log.Error().Err(err).
				Str("tenant", b.TenantID).Str("user", b.UserID).
				Msg("baseline flush error")
			metrics.BaselineFlushErrors.Inc()
		}
	}

	metrics.BaselineFlushDuration.Observe(time.Since(start).Seconds())
}

// Close shuts down the Kafka reader.
func (c *Consumer) Close() {
	_ = c.reader.Close()
}
