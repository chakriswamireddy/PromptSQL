package audit

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rs/zerolog"
	kafka "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("audit-client")

// Config configures the audit client.
type Config struct {
	Brokers []string
	// TopicPolicy is the Kafka topic for policy events (e.g. "audit.policy.dev").
	TopicPolicy string
	// TopicAccess is the Kafka topic for access events.
	TopicAccess string
	// TopicSystem is the Kafka topic for system events.
	TopicSystem string
	// Service is the producing service name embedded in every event.
	Service string
	// HMACKey is the per-tenant HMAC key for actor token derivation.
	// If empty, actor tokens are omitted (dev mode).
	HMACKey []byte
	// FlushInterval is how often the batch is flushed. Default: 500 ms.
	FlushInterval time.Duration
	// FlushBytes is the batch size that triggers an early flush. Default: 1 MB.
	FlushBytes int
	// DiskBufferDir is the directory for the on-disk spool on Kafka outage.
	DiskBufferDir string
	// DiskBufferMaxBytes is the maximum size of the spool. Default: 256 MB.
	DiskBufferMaxBytes int64
	// Enabled gates the entire client. If false, all calls are no-ops.
	Enabled bool

	Log zerolog.Logger
}

func (c *Config) setDefaults() {
	if c.FlushInterval == 0 {
		c.FlushInterval = 500 * time.Millisecond
	}
	if c.FlushBytes == 0 {
		c.FlushBytes = 1 << 20 // 1 MB
	}
	if c.DiskBufferMaxBytes == 0 {
		c.DiskBufferMaxBytes = 256 << 20 // 256 MB
	}
}

var (
	metricBuffered = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_buffered_total",
		Help: "Total events written to disk buffer during Kafka outage.",
	}, []string{"topic"})

	metricDropped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_dropped_total",
		Help: "Total events dropped when disk buffer ceiling was reached.",
	}, []string{"topic"})

	metricProduced = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_produced_total",
		Help: "Total events successfully produced to Kafka.",
	}, []string{"topic"})

	metricProduceErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "audit_produce_errors_total",
		Help: "Total Kafka produce errors.",
	}, []string{"topic"})

	metricBatchFlushDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "audit_batch_flush_duration_seconds",
		Help:    "Time to flush one batch to Kafka.",
		Buckets: prometheus.DefBuckets,
	}, []string{"topic"})
)

// Client is the audit event producer. It is safe for concurrent use.
type Client struct {
	cfg     Config
	writers map[string]*kafka.Writer // keyed by topic
	mu      sync.Mutex

	policyBatch []kafka.Message
	accessBatch []kafka.Message
	systemBatch []kafka.Message
	batchBytes  int

	diskUsed atomic.Int64

	quit chan struct{}
	done chan struct{}
}

// New creates and starts the audit client. Call Close when done.
func New(cfg Config) *Client {
	cfg.setDefaults()
	c := &Client{
		cfg:  cfg,
		quit: make(chan struct{}),
		done: make(chan struct{}),
	}
	if cfg.Enabled {
		c.writers = map[string]*kafka.Writer{
			cfg.TopicPolicy: c.newWriter(cfg.TopicPolicy),
			cfg.TopicAccess: c.newWriter(cfg.TopicAccess),
			cfg.TopicSystem: c.newWriter(cfg.TopicSystem),
		}
		go c.flusher()
	}
	return c
}

func (c *Client) newWriter(topic string) *kafka.Writer {
	return &kafka.Writer{
		Addr:                   kafka.TCP(c.cfg.Brokers...),
		Topic:                  topic,
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: false,
		Compression:            kafka.Zstd,
		BatchTimeout:           c.cfg.FlushInterval,
	}
}

// PolicyEvent enqueues a policy audit event (non-blocking).
func (c *Client) PolicyEvent(ctx context.Context, actorID, tenantID string, ev PolicyEvent) {
	if !c.cfg.Enabled {
		return
	}
	_, span := tracer.Start(ctx, "audit.policy_event")
	defer span.End()
	span.SetAttributes(attribute.String("audit.action", string(ev.Action)))

	ev.EventID = newEventID()
	ev.Schema = SchemaV1
	ev.Service = c.cfg.Service
	ev.EventTime = time.Now().UTC()
	ev.TenantID = tenantID
	ev.ActorID = actorID
	ev.ActorToken = c.tokenize(tenantID, actorID)

	c.enqueue(c.cfg.TopicPolicy, tenantID, ev)
}

// AccessEvent enqueues an access decision audit event (non-blocking).
func (c *Client) AccessEvent(ctx context.Context, ev AccessEvent) {
	if !c.cfg.Enabled {
		return
	}
	_, span := tracer.Start(ctx, "audit.access_event")
	defer span.End()
	span.SetAttributes(
		attribute.String("audit.decision", string(ev.Decision)),
		attribute.String("audit.resource", ev.Resource),
	)

	ev.EventID = newEventID()
	ev.Schema = SchemaV1
	ev.Service = c.cfg.Service
	ev.EventTime = time.Now().UTC()
	ev.ActorToken = c.tokenize(ev.TenantID, ev.UserID)

	c.enqueue(c.cfg.TopicAccess, ev.UserID, ev)
}

// SystemEvent enqueues a system-level audit event (non-blocking).
func (c *Client) SystemEvent(ctx context.Context, ev SystemEvent) {
	if !c.cfg.Enabled {
		return
	}
	ev.EventID = newEventID()
	ev.Schema = SchemaV1
	ev.Service = c.cfg.Service
	ev.EventTime = time.Now().UTC()

	c.enqueue(c.cfg.TopicSystem, "", ev)
}

func (c *Client) enqueue(topic, key string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		c.cfg.Log.Error().Err(err).Str("topic", topic).Msg("audit: marshal failed")
		return
	}
	msg := kafka.Message{Key: []byte(key), Value: b}

	c.mu.Lock()
	defer c.mu.Unlock()

	switch topic {
	case c.cfg.TopicPolicy:
		c.policyBatch = append(c.policyBatch, msg)
	case c.cfg.TopicAccess:
		c.accessBatch = append(c.accessBatch, msg)
	default:
		c.systemBatch = append(c.systemBatch, msg)
	}
	c.batchBytes += len(b)

	if c.batchBytes >= c.cfg.FlushBytes {
		c.flushLocked(context.Background())
	}
}

// flusher runs the periodic flush goroutine.
func (c *Client) flusher() {
	defer close(c.done)
	tick := time.NewTicker(c.cfg.FlushInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			c.mu.Lock()
			c.flushLocked(context.Background())
			c.mu.Unlock()
		case <-c.quit:
			c.mu.Lock()
			c.flushLocked(context.Background())
			c.mu.Unlock()
			return
		}
	}
}

// flushLocked flushes all pending batches. Caller must hold c.mu.
func (c *Client) flushLocked(ctx context.Context) {
	c.flushTopic(ctx, c.cfg.TopicPolicy, &c.policyBatch)
	c.flushTopic(ctx, c.cfg.TopicAccess, &c.accessBatch)
	c.flushTopic(ctx, c.cfg.TopicSystem, &c.systemBatch)
	c.batchBytes = 0
}

func (c *Client) flushTopic(ctx context.Context, topic string, batch *[]kafka.Message) {
	if len(*batch) == 0 {
		return
	}
	msgs := *batch
	*batch = nil

	start := time.Now()
	w, ok := c.writers[topic]
	if !ok {
		return
	}
	if err := w.WriteMessages(ctx, msgs...); err != nil {
		metricProduceErrors.WithLabelValues(topic).Add(float64(len(msgs)))
		c.cfg.Log.Error().Err(err).Str("topic", topic).Int("count", len(msgs)).Msg("audit: kafka write failed — spooling to disk")
		c.spillToDisk(topic, msgs)
		return
	}
	metricProduced.WithLabelValues(topic).Add(float64(len(msgs)))
	metricBatchFlushDuration.WithLabelValues(topic).Observe(time.Since(start).Seconds())
}

// spillToDisk writes a batch to the local disk spool when Kafka is unavailable.
func (c *Client) spillToDisk(topic string, msgs []kafka.Message) {
	if c.cfg.DiskBufferDir == "" {
		metricDropped.WithLabelValues(topic).Add(float64(len(msgs)))
		return
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, m := range msgs {
		_ = enc.Encode(m.Value)
	}
	data := buf.Bytes()
	size := int64(len(data))
	if c.diskUsed.Load()+size > c.cfg.DiskBufferMaxBytes {
		metricDropped.WithLabelValues(topic).Add(float64(len(msgs)))
		c.cfg.Log.Error().Str("topic", topic).Msg("audit: disk buffer full — dropping events")
		return
	}
	dir := filepath.Join(c.cfg.DiskBufferDir, topic)
	_ = os.MkdirAll(dir, 0o700)
	fname := filepath.Join(dir, fmt.Sprintf("%d.jsonl.zz", time.Now().UnixNano()))
	f, err := os.Create(fname) //nolint:gosec
	if err != nil {
		metricDropped.WithLabelValues(topic).Add(float64(len(msgs)))
		return
	}
	defer f.Close() //nolint:errcheck
	zw := zlib.NewWriter(f)
	_, werr := zw.Write(data)
	zerr := zw.Close()
	if werr != nil || zerr != nil {
		_ = os.Remove(fname)
		metricDropped.WithLabelValues(topic).Add(float64(len(msgs)))
		return
	}
	c.diskUsed.Add(size)
	metricBuffered.WithLabelValues(topic).Add(float64(len(msgs)))
}

// tokenize derives a deterministic HMAC token for a (tenantID, actorID) pair.
// Tokens are not reversible without the key.
func (c *Client) tokenize(tenantID, actorID string) string {
	if len(c.cfg.HMACKey) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, c.cfg.HMACKey)
	mac.Write([]byte(tenantID + ":" + actorID)) //nolint:errcheck
	return hex.EncodeToString(mac.Sum(nil))
}

// Close drains the batch and shuts down Kafka writers.
func (c *Client) Close() error {
	if !c.cfg.Enabled {
		return nil
	}
	close(c.quit)
	<-c.done
	var firstErr error
	for _, w := range c.writers {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
