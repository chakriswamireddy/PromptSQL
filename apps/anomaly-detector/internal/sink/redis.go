// Package sink writes scored risk events to Redis and Kafka.
package sink

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/governance-platform/anomaly-detector/internal/scoring"
)

// RedisSink writes risk scores to Redis with a configurable TTL.
type RedisSink struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisSink creates a RedisSink.
func NewRedisSink(rdb *redis.Client, ttl time.Duration) *RedisSink {
	return &RedisSink{rdb: rdb, ttl: ttl}
}

// Write serialises the RiskScore and stores it at risk:score:{tenantID}:{userID}.
func (s *RedisSink) Write(ctx context.Context, rs scoring.RiskScore) error {
	payload, err := rs.Marshal()
	if err != nil {
		return fmt.Errorf("marshal risk score: %w", err)
	}
	key := scoreKey(rs.TenantID, rs.UserID)
	return s.rdb.Set(ctx, key, payload, s.ttl).Err()
}

// scoreKey returns the canonical Redis key for a user's risk score.
func scoreKey(tenantID, userID string) string {
	return fmt.Sprintf("risk:score:%s:%s", tenantID, userID)
}
