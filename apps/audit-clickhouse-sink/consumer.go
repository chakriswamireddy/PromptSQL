package main

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
	kafka "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("audit-clickhouse-sink")

var (
	metricConsumed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_clickhouse_consumed_total",
		Help: "Total messages consumed from Kafka.",
	}, []string{"topic"})

	metricInserted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_clickhouse_inserted_total",
		Help: "Total rows inserted into ClickHouse.",
	}, []string{"table"})

	metricInsertErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_clickhouse_insert_errors_total",
		Help: "Total ClickHouse insert errors.",
	}, []string{"table"})

	metricLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "audit_clickhouse_consumer_lag_seconds",
		Help: "Consumer lag in seconds (event_time to ingest_time delta).",
	}, []string{"topic"})
)

// policyEvent mirrors the wire payload from the audit-client SDK.
type policyEvent struct {
	EventID     string         `json:"event_id"`
	TenantID    string         `json:"tenant_id"`
	ActorID     string         `json:"actor_id"`
	ActorToken  string         `json:"actor_token"`
	Action      string         `json:"action"`
	PolicyID    string         `json:"policy_id"`
	BeforeState any            `json:"before_state"`
	AfterState  any            `json:"after_state"`
	Metadata    map[string]any `json:"metadata"`
	EventTime   time.Time      `json:"event_time"`
}

type accessEvent struct {
	EventID       string    `json:"event_id"`
	TenantID      string    `json:"tenant_id"`
	UserID        string    `json:"user_id"`
	ActorToken    string    `json:"actor_token"`
	DataSourceID  string    `json:"data_source_id"`
	Resource      string    `json:"resource"`
	Action        string    `json:"action"`
	Decision      string    `json:"decision"`
	Reason        string    `json:"reason"`
	RowCount      int64     `json:"row_count"`
	QueryHash     string    `json:"query_hash"`
	DurationMs    int64     `json:"duration_ms"`
	RiskScore     float64   `json:"risk_score"`
	BreakGlass    bool      `json:"break_glass"`
	PolicyVersion string    `json:"policy_version"`
	EventTime     time.Time `json:"event_time"`
}

type systemEvent struct {
	EventID   string    `json:"event_id"`
	TenantID  string    `json:"tenant_id"`
	Action    string    `json:"action"`
	Detail    any       `json:"detail"`
	EventTime time.Time `json:"event_time"`
}

// Consumer reads from Kafka and bulk-inserts into ClickHouse.
type Consumer struct {
	cfg     config
	ch      clickhouse.Conn
	readers map[string]*kafka.Reader
	log     zerolog.Logger
}

func newConsumer(cfg config, ch clickhouse.Conn, log zerolog.Logger) *Consumer {
	readers := map[string]*kafka.Reader{
		cfg.TopicPolicy: newReader(cfg, cfg.TopicPolicy),
		cfg.TopicAccess: newReader(cfg, cfg.TopicAccess),
		cfg.TopicSystem: newReader(cfg, cfg.TopicSystem),
	}
	return &Consumer{cfg: cfg, ch: ch, readers: readers, log: log}
}

func newReader(cfg config, topic string) *kafka.Reader {
	return kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.KafkaBrokers,
		GroupID:        cfg.ConsumerGroup,
		Topic:          topic,
		MinBytes:       1,
		MaxBytes:       10 << 20,
		CommitInterval: time.Second,
		StartOffset:    kafka.LastOffset,
	})
}

func (c *Consumer) Run(ctx context.Context) {
	for topic, reader := range c.readers {
		go c.runTopic(ctx, topic, reader)
	}
}

func (c *Consumer) runTopic(ctx context.Context, topic string, reader *kafka.Reader) {
	batch := make([]kafka.Message, 0, c.cfg.BatchSize)
	timer := time.NewTimer(c.cfg.BatchTimeout)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.insertBatch(ctx, topic, batch)
		batch = batch[:0]
		timer.Reset(c.cfg.BatchTimeout)
	}

	for {
		// Poll with a short deadline so the timer can fire.
		fctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		msg, err := reader.FetchMessage(fctx)
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
		metricConsumed.WithLabelValues(topic).Inc()

		select {
		case <-timer.C:
			flush()
		default:
			if len(batch) >= c.cfg.BatchSize {
				flush()
			}
		}

		if err := reader.CommitMessages(ctx, msg); err != nil {
			c.log.Error().Err(err).Str("topic", topic).Msg("commit failed")
		}
	}
}

func (c *Consumer) insertBatch(ctx context.Context, topic string, msgs []kafka.Message) {
	_, span := tracer.Start(ctx, "clickhouse.insert_batch")
	defer span.End()
	span.SetAttributes(attribute.String("topic", topic), attribute.Int("count", len(msgs)))

	switch topic {
	case c.cfg.TopicPolicy:
		c.insertPolicyBatch(ctx, msgs)
	case c.cfg.TopicAccess:
		c.insertAccessBatch(ctx, msgs)
	case c.cfg.TopicSystem:
		c.insertSystemBatch(ctx, msgs)
	}
}

func (c *Consumer) insertPolicyBatch(ctx context.Context, msgs []kafka.Message) {
	batch, err := c.ch.PrepareBatch(ctx, "INSERT INTO audit_policy")
	if err != nil {
		metricInsertErrors.WithLabelValues("audit_policy").Add(float64(len(msgs)))
		c.log.Error().Err(err).Msg("clickhouse: prepare batch audit_policy")
		return
	}
	for _, m := range msgs {
		var ev policyEvent
		if err := json.Unmarshal(m.Value, &ev); err != nil {
			c.log.Warn().Err(err).Msg("skip malformed policy event")
			continue
		}
		beforeJSON, _ := json.Marshal(ev.BeforeState)
		afterJSON, _ := json.Marshal(ev.AfterState)
		meta := ev.Metadata
		requestID, _ := meta["request_id"].(string)
		traceID, _ := meta["trace_id"].(string)
		ip, _ := meta["ip"].(string)
		userAgent, _ := meta["user_agent"].(string)

		_ = batch.Append(
			toUUID(ev.EventID),
			toUUID(ev.TenantID),
			toUUID(ev.ActorID),
			ev.ActorToken,
			ev.Action,
			toUUID(ev.PolicyID),
			string(beforeJSON),
			string(afterJSON),
			toUUID(requestID),
			traceID,
			net.ParseIP(ip),
			userAgent,
			"v1",
			ev.EventTime,
		)
		lag := time.Since(ev.EventTime).Seconds()
		metricLag.WithLabelValues(c.cfg.TopicPolicy).Set(lag)
	}
	if err := batch.Send(); err != nil {
		metricInsertErrors.WithLabelValues("audit_policy").Add(float64(len(msgs)))
		c.log.Error().Err(err).Msg("clickhouse: send batch audit_policy")
		return
	}
	metricInserted.WithLabelValues("audit_policy").Add(float64(len(msgs)))
}

func (c *Consumer) insertAccessBatch(ctx context.Context, msgs []kafka.Message) {
	batch, err := c.ch.PrepareBatch(ctx, "INSERT INTO audit_access")
	if err != nil {
		metricInsertErrors.WithLabelValues("audit_access").Add(float64(len(msgs)))
		c.log.Error().Err(err).Msg("clickhouse: prepare batch audit_access")
		return
	}
	for _, m := range msgs {
		var ev accessEvent
		if err := json.Unmarshal(m.Value, &ev); err != nil {
			c.log.Warn().Err(err).Msg("skip malformed access event")
			continue
		}
		bg := uint8(0)
		if ev.BreakGlass {
			bg = 1
		}
		_ = batch.Append(
			toUUID(ev.EventID),
			toUUID(ev.TenantID),
			toUUID(ev.UserID),
			ev.ActorToken,
			toUUID(ev.DataSourceID),
			ev.Resource,
			ev.Action,
			ev.Decision,
			ev.Reason,
			ev.RowCount,
			ev.QueryHash,
			ev.DurationMs,
			ev.RiskScore,
			bg,
			ev.PolicyVersion,
			"v1",
			ev.EventTime,
		)
		metricLag.WithLabelValues(c.cfg.TopicAccess).Set(time.Since(ev.EventTime).Seconds())
	}
	if err := batch.Send(); err != nil {
		metricInsertErrors.WithLabelValues("audit_access").Add(float64(len(msgs)))
		c.log.Error().Err(err).Msg("clickhouse: send batch audit_access")
		return
	}
	metricInserted.WithLabelValues("audit_access").Add(float64(len(msgs)))
}

func (c *Consumer) insertSystemBatch(ctx context.Context, msgs []kafka.Message) {
	batch, err := c.ch.PrepareBatch(ctx, "INSERT INTO audit_system")
	if err != nil {
		metricInsertErrors.WithLabelValues("audit_system").Add(float64(len(msgs)))
		return
	}
	for _, m := range msgs {
		var ev systemEvent
		if err := json.Unmarshal(m.Value, &ev); err != nil {
			continue
		}
		detail, _ := json.Marshal(ev.Detail)
		_ = batch.Append(
			toUUID(ev.EventID),
			toUUID(ev.TenantID),
			ev.Action,
			string(detail),
			"v1",
			ev.EventTime,
		)
	}
	if err := batch.Send(); err != nil {
		metricInsertErrors.WithLabelValues("audit_system").Add(float64(len(msgs)))
		return
	}
	metricInserted.WithLabelValues("audit_system").Add(float64(len(msgs)))
}

func (c *Consumer) Close() {
	for _, r := range c.readers {
		_ = r.Close()
	}
}

// toUUID converts a string UUID to [16]byte; returns zero UUID on error.
func toUUID(s string) [16]byte {
	if len(s) != 36 {
		return [16]byte{}
	}
	var b [16]byte
	clean := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	for i := 0; i < 16; i++ {
		v, err := strconv.ParseUint(clean[i*2:i*2+2], 16, 8)
		if err != nil {
			return [16]byte{}
		}
		b[i] = byte(v)
	}
	return b
}
