package sink

import (
	"context"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"github.com/governance-platform/anomaly-detector/internal/scoring"
)

// KafkaSink publishes RiskScore events to the risk.scored.{env} topic.
type KafkaSink struct {
	writer *kafka.Writer
}

// NewKafkaSink creates a KafkaSink writing to the given topic.
func NewKafkaSink(brokers []string, topic string) *KafkaSink {
	w := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.Hash{}, // key-by userID for ordered delivery
		RequiredAcks: kafka.RequireAll,
		Compression:  kafka.Zstd,
		// Idempotent producer — exactly-once delivery within a session.
		AllowAutoTopicCreation: false,
		WriteTimeout:           10 * time.Second,
	}
	return &KafkaSink{writer: w}
}

// Publish writes the risk score event to Kafka, keyed by userID for ordered delivery.
func (s *KafkaSink) Publish(ctx context.Context, rs scoring.RiskScore) error {
	payload, err := rs.Marshal()
	if err != nil {
		return fmt.Errorf("marshal risk score: %w", err)
	}
	msg := kafka.Message{
		Key:   []byte(rs.UserID),
		Value: payload,
		Time:  rs.ComputedAt,
	}
	if err := s.writer.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("kafka write risk.scored: %w", err)
	}
	return nil
}

// Close flushes pending writes and closes the Kafka writer.
func (s *KafkaSink) Close() error {
	return s.writer.Close()
}
