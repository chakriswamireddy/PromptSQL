package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
	kafkago "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var wormTracer = otel.Tracer("audit-worm-sink")

var (
	metricConsumed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_worm_consumed_total",
		Help: "Total messages consumed from Kafka by the WORM sink.",
	}, []string{"topic"})

	metricObjectsWritten = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_worm_objects_written_total",
		Help: "Total WORM objects written to S3.",
	}, []string{"topic"})

	metricWriteErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_worm_write_errors_total",
		Help: "Total S3 write errors.",
	}, []string{"topic"})

	metricBytesWritten = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_worm_bytes_written_total",
		Help: "Total compressed bytes written to WORM storage.",
	}, []string{"topic"})
)

// Manifest is written alongside each WORM object as manifest.json.
type Manifest struct {
	HourUTC         string `json:"hour_utc"`
	Topic           string `json:"topic"`
	EventCount      int    `json:"event_count"`
	SHA256          string `json:"sha256"`
	ProducerVersion string `json:"producer_version"`
}

// WORMConsumer batches events by (tenant, topic, hour) and flushes hourly.
type WORMConsumer struct {
	cfg     config
	s3      *s3.Client
	readers map[string]*kafkago.Reader
	// buffer: topic → tenantID → []raw event bytes
	buffer map[string]map[string][][]byte
	log    zerolog.Logger
}

func newWORMConsumer(cfg config, log zerolog.Logger) (*WORMConsumer, error) {
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

	readers := map[string]*kafkago.Reader{
		cfg.TopicPolicy: newReader(cfg, cfg.TopicPolicy),
		cfg.TopicAccess: newReader(cfg, cfg.TopicAccess),
		cfg.TopicSystem: newReader(cfg, cfg.TopicSystem),
	}

	buf := make(map[string]map[string][][]byte)
	for _, t := range []string{cfg.TopicPolicy, cfg.TopicAccess, cfg.TopicSystem} {
		buf[t] = make(map[string][][]byte)
	}

	return &WORMConsumer{
		cfg:     cfg,
		s3:      s3Client,
		readers: readers,
		buffer:  buf,
		log:     log,
	}, nil
}

func newReader(cfg config, topic string) *kafkago.Reader {
	return kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:        cfg.KafkaBrokers,
		GroupID:        cfg.ConsumerGroup,
		Topic:          topic,
		MinBytes:       1,
		MaxBytes:       10 << 20,
		CommitInterval: time.Second,
		StartOffset:    kafkago.LastOffset,
	})
}

func (w *WORMConsumer) Run(ctx context.Context) {
	for topic, reader := range w.readers {
		go w.runTopic(ctx, topic, reader)
	}
	go w.periodicFlush(ctx)
}

func (w *WORMConsumer) runTopic(ctx context.Context, topic string, reader *kafkago.Reader) {
	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.log.Error().Err(err).Str("topic", topic).Msg("kafka fetch error")
			time.Sleep(time.Second)
			continue
		}

		tenantID := w.extractTenantID(msg.Value)
		w.buffer[topic][tenantID] = append(w.buffer[topic][tenantID], msg.Value)
		metricConsumed.WithLabelValues(topic).Inc()

		if err := reader.CommitMessages(ctx, msg); err != nil {
			w.log.Warn().Err(err).Str("topic", topic).Msg("commit failed")
		}
	}
}

func (w *WORMConsumer) periodicFlush(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.flushAll(ctx)
		case <-ctx.Done():
			w.flushAll(ctx)
			return
		}
	}
}

func (w *WORMConsumer) flushAll(ctx context.Context) {
	now := time.Now().UTC()
	for topic, tenants := range w.buffer {
		for tenantID, events := range tenants {
			if len(events) == 0 {
				continue
			}
			if err := w.writeObject(ctx, topic, tenantID, now, events); err != nil {
				metricWriteErrors.WithLabelValues(topic).Inc()
				w.log.Error().Err(err).Str("topic", topic).Str("tenant", tenantID).Msg("worm write failed")
				continue
			}
			metricObjectsWritten.WithLabelValues(topic).Inc()
			w.buffer[topic][tenantID] = w.buffer[topic][tenantID][:0]
		}
	}
}

func (w *WORMConsumer) writeObject(ctx context.Context, topic, tenantID string, t time.Time, events [][]byte) error {
	_, span := wormTracer.Start(ctx, "worm.write_object")
	defer span.End()
	span.SetAttributes(
		attribute.String("topic", topic),
		attribute.String("tenant_id", tenantID),
		attribute.Int("event_count", len(events)),
	)

	// Build compressed JSONL.
	var buf bytes.Buffer
	enc, _ := zstd.NewWriter(&buf)
	for _, ev := range events {
		_, _ = enc.Write(ev)
		_, _ = enc.Write([]byte("\n"))
	}
	_ = enc.Close()
	compressed := buf.Bytes()

	// Compute SHA-256.
	sum := sha256.Sum256(compressed)
	digest := hex.EncodeToString(sum[:])

	// S3 key layout: tenant=<uuid>/topic=<name>/year=.../month=.../day=.../hour=.../events-<ISO>.jsonl.zst
	topicShort := topic
	hour := t.Truncate(time.Hour)
	key := fmt.Sprintf(
		"tenant=%s/topic=%s/year=%d/month=%02d/day=%02d/hour=%02d/events-%s.jsonl.zst",
		tenantID, topicShort,
		hour.Year(), int(hour.Month()), hour.Day(), hour.Hour(),
		hour.Format("2006-01-02T15"),
	)

	// Upload with Object Lock Compliance (MinIO/S3).
	retainUntil := t.AddDate(w.cfg.RetentionYears, 0, 0)
	_, err := w.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:                    aws.String(w.cfg.S3Bucket),
		Key:                       aws.String(key),
		Body:                      bytes.NewReader(compressed),
		ContentLength:             aws.Int64(int64(len(compressed))),
		ContentType:               aws.String("application/zstd"),
		ChecksumSHA256:            aws.String(digest),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: aws.Time(retainUntil),
	})
	if err != nil {
		return fmt.Errorf("s3 put object: %w", err)
	}
	metricBytesWritten.WithLabelValues(topic).Add(float64(len(compressed)))

	// Write manifest.
	manifest := Manifest{
		HourUTC:         hour.Format(time.RFC3339),
		Topic:           topic,
		EventCount:      len(events),
		SHA256:          digest,
		ProducerVersion: w.cfg.Version,
	}
	mBytes, _ := json.Marshal(manifest)
	manifestKey := fmt.Sprintf(
		"tenant=%s/topic=%s/year=%d/month=%02d/day=%02d/hour=%02d/manifest.json",
		tenantID, topicShort,
		hour.Year(), int(hour.Month()), hour.Day(), hour.Hour(),
	)
	_, err = w.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:                    aws.String(w.cfg.S3Bucket),
		Key:                       aws.String(manifestKey),
		Body:                      bytes.NewReader(mBytes),
		ContentLength:             aws.Int64(int64(len(mBytes))),
		ContentType:               aws.String("application/json"),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: aws.Time(retainUntil),
	})
	return err
}

func (w *WORMConsumer) extractTenantID(raw []byte) string {
	var ev struct {
		TenantID string `json:"tenant_id"`
	}
	_ = json.Unmarshal(raw, &ev)
	return ev.TenantID
}

func (w *WORMConsumer) Close() {
	for _, r := range w.readers {
		_ = r.Close()
	}
}

// required to satisfy interface; unused here.
var _ io.Reader = (*bytes.Reader)(nil)
