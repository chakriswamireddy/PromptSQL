package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// JTIStore uses Redis SET NX (set-if-not-exists) to detect JWT replay attacks.
// Each jti is stored for accessTTL + clockSkew so that any token replayed within
// its validity window is caught. False-positive rate is 0 (exact set, not bloom).
type JTIStore struct {
	rdb *redis.Client
	ttl time.Duration // should be accessTTL + clockSkew (default 11 min)
}

// NewJTIStore returns a JTIStore backed by rdb.
// ttl should be (access token TTL + clock skew tolerance); defaults to 11 min.
func NewJTIStore(rdb *redis.Client, ttl time.Duration) *JTIStore {
	if ttl == 0 {
		ttl = 11 * time.Minute
	}
	return &JTIStore{rdb: rdb, ttl: ttl}
}

// Claim atomically marks jti as used. It returns nil on first use, and
// ErrReplay on any subsequent call with the same jti within the TTL window.
// Redis unavailability returns a wrapped error (caller decides fail-open vs closed).
func (s *JTIStore) Claim(ctx context.Context, jti string) error {
	key := fmt.Sprintf("jti:%s", jti)
	// SET key "1" NX EX ttl — returns true if the key was newly set.
	ok, err := s.rdb.SetNX(ctx, key, "1", s.ttl).Result()
	if err != nil {
		return fmt.Errorf("jti: redis error: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrReplay, jti)
	}
	return nil
}

// ErrReplay is returned when a jti has already been seen.
var ErrReplay = fmt.Errorf("jwt replay detected")
