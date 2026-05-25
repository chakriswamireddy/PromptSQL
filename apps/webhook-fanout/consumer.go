package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rs/zerolog"
	kafka "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel/attribute"
)

// KafkaConsumer reads audit events and dispatches to Fanout.
type KafkaConsumer struct {
	fanout  *Fanout
	cfg     config
	log     zerolog.Logger
	readers []*kafka.Reader
}

func newKafkaConsumer(fanout *Fanout, cfg config, log zerolog.Logger) *KafkaConsumer {
	topics := []string{cfg.TopicAccess, cfg.TopicPolicy, cfg.TopicSystem}
	readers := make([]*kafka.Reader, len(topics))
	for i, t := range topics {
		readers[i] = kafka.NewReader(kafka.ReaderConfig{
			Brokers:        cfg.KafkaBrokers,
			GroupID:        cfg.ConsumerGroup,
			Topic:          t,
			MinBytes:       1,
			MaxBytes:       10 << 20,
			CommitInterval: time.Second,
			StartOffset:    kafka.LastOffset,
		})
	}
	return &KafkaConsumer{fanout: fanout, cfg: cfg, log: log, readers: readers}
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
			kc.log.Error().Err(err).Str("topic", topic).Msg("kafka fetch")
			time.Sleep(time.Second)
			continue
		}

		metricKafkaConsumed.WithLabelValues(topic).Inc()
		metricKafkaLag.WithLabelValues(topic).Set(time.Since(msg.Time).Seconds())

		_, span := tracer.Start(ctx, "kafka.consume.fanout")
		span.SetAttributes(attribute.String("topic", topic))

		// Determine logical event type from topic.
		eventType := topicToEventType(topic, kc.cfg)
		kc.fanout.Dispatch(ctx, eventType, json.RawMessage(msg.Value))

		span.End()

		if err := r.CommitMessages(ctx, msg); err != nil {
			kc.log.Error().Err(err).Str("topic", topic).Msg("kafka commit")
		}
	}
}

func (kc *KafkaConsumer) Close() {
	for _, r := range kc.readers {
		_ = r.Close()
	}
}

func topicToEventType(topic string, cfg config) string {
	switch topic {
	case cfg.TopicAccess:
		return "access.decision"
	case cfg.TopicPolicy:
		return "policy.changed"
	case cfg.TopicSystem:
		return "system.event"
	default:
		return topic
	}
}
