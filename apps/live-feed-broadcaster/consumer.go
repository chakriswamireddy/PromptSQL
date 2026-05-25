package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"
	kafka "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel/attribute"
)

// KafkaConsumer reads audit events and pushes them to the Hub.
type KafkaConsumer struct {
	hub     *Hub
	cfg     config
	log     zerolog.Logger
	readers []*kafka.Reader
}

func newKafkaConsumer(hub *Hub, cfg config, log zerolog.Logger) *KafkaConsumer {
	readers := []*kafka.Reader{
		kafka.NewReader(kafka.ReaderConfig{
			Brokers:        cfg.KafkaBrokers,
			GroupID:        cfg.ConsumerGroup,
			Topic:          cfg.TopicAccess,
			MinBytes:       1,
			MaxBytes:       1 << 20,
			CommitInterval: time.Second,
			StartOffset:    kafka.LastOffset,
		}),
		kafka.NewReader(kafka.ReaderConfig{
			Brokers:        cfg.KafkaBrokers,
			GroupID:        cfg.ConsumerGroup,
			Topic:          cfg.TopicPolicy,
			MinBytes:       1,
			MaxBytes:       1 << 20,
			CommitInterval: time.Second,
			StartOffset:    kafka.LastOffset,
		}),
	}
	return &KafkaConsumer{hub: hub, cfg: cfg, log: log, readers: readers}
}

func (kc *KafkaConsumer) Run(ctx context.Context) {
	for _, r := range kc.readers {
		go kc.consume(ctx, r)
	}
}

func (kc *KafkaConsumer) consume(ctx context.Context, r *kafka.Reader) {
	topic := r.Config().Topic
	for {
		msg, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			kc.log.Error().Err(err).Str("topic", topic).Msg("kafka fetch error")
			time.Sleep(time.Second)
			continue
		}

		metricKafkaConsumed.WithLabelValues(topic).Inc()
		metricKafkaLag.WithLabelValues(topic, kc.cfg.ConsumerGroup).Set(time.Since(msg.Time).Seconds())

		_, span := tracer.Start(ctx, "kafka.consume")
		span.SetAttributes(attribute.String("topic", topic))

		ev := kc.parse(topic, msg.Value)
		if ev != nil {
			kc.hub.Broadcast(*ev)
		}
		span.End()

		if err := r.CommitMessages(ctx, msg); err != nil {
			kc.log.Error().Err(err).Str("topic", topic).Msg("kafka commit error")
		}
	}
}

func (kc *KafkaConsumer) parse(topic string, data []byte) *LiveEvent {
	// Unified raw decode for all event types.
	var raw struct {
		EventID   string          `json:"event_id"`
		TenantID  string          `json:"tenant_id"`
		UserID    string          `json:"user_id"`
		Resource  string          `json:"resource"`
		Decision  string          `json:"decision"`
		RiskScore float64         `json:"risk_score"`
		TraceID   string          `json:"trace_id"`
		EventTime time.Time       `json:"event_time"`
		Action    string          `json:"action"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		kc.log.Warn().Err(err).Str("topic", topic).Msg("malformed event")
		return nil
	}

	evType := "access"
	if topic == kc.cfg.TopicPolicy {
		evType = "policy"
	}

	return &LiveEvent{
		EventID:   raw.EventID,
		EventType: evType,
		TenantID:  raw.TenantID,
		UserID:    raw.UserID,
		Resource:  raw.Resource,
		Decision:  raw.Decision,
		RiskScore: raw.RiskScore,
		TraceID:   raw.TraceID,
		EventTime: raw.EventTime,
		Detail:    json.RawMessage(data),
	}
}

func (kc *KafkaConsumer) Close() {
	for _, r := range kc.readers {
		_ = r.Close()
	}
}
