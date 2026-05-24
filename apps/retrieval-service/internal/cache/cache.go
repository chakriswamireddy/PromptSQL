// Package cache manages Redis-backed caches for snapshots, doc results, and embeddings.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type Cache struct {
	rdb   *redis.Client
	sf    singleflight.Group

	SnapshotTTL  time.Duration
	DocResultTTL time.Duration
	EmbedTTL     time.Duration
}

func New(rdb *redis.Client, snapshotTTL, docResultTTL, embedTTL time.Duration) *Cache {
	return &Cache{
		rdb:          rdb,
		SnapshotTTL:  snapshotTTL,
		DocResultTTL: docResultTTL,
		EmbedTTL:     embedTTL,
	}
}

// ── Snapshot cache ────────────────────────────────────────────────────────────

// SnapshotKey builds a Redis key for (userId, dataSourceId, schemaVersion, policySetVersion).
func SnapshotKey(userID, dataSourceID, schemaVersion, policySetVersion string) string {
	return fmt.Sprintf("retrieval:snap:%s:%s:%s:%s", userID, dataSourceID, schemaVersion, policySetVersion)
}

func (c *Cache) GetSnapshot(ctx context.Context, key string) ([]byte, bool, error) {
	val, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

func (c *Cache) SetSnapshot(ctx context.Context, key string, data []byte) error {
	return c.rdb.Set(ctx, key, data, c.SnapshotTTL).Err()
}

// GetOrBuildSnapshot prevents stampede via singleflight on the cache key.
func (c *Cache) GetOrBuildSnapshot(ctx context.Context, key string, build func() ([]byte, error)) ([]byte, bool, error) {
	// Fast path: cache hit.
	if data, hit, err := c.GetSnapshot(ctx, key); hit || err != nil {
		return data, hit, err
	}

	// Slow path: deduplicated build.
	v, err, _ := c.sf.Do(key, func() (any, error) {
		data, err := build()
		if err != nil {
			return nil, err
		}
		_ = c.SetSnapshot(ctx, key, data)
		return data, nil
	})
	if err != nil {
		return nil, false, err
	}
	return v.([]byte), false, nil
}

// InvalidateSnapshotPrefix removes all snapshot keys for a (dataSourceID).
// Called when a schema version bumps.
func (c *Cache) InvalidateSnapshotPrefix(ctx context.Context, dataSourceID string) error {
	pattern := fmt.Sprintf("retrieval:snap:*:%s:*", dataSourceID)
	keys, err := c.rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	return c.rdb.Del(ctx, keys...).Err()
}

// ── Doc retrieval result cache ────────────────────────────────────────────────

func DocResultKey(userID, queryHash, policySetVersion string) string {
	return fmt.Sprintf("retrieval:docs:%s:%s:%s", userID, queryHash, policySetVersion)
}

func (c *Cache) GetDocResult(ctx context.Context, key string, out any) (bool, error) {
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, out)
}

func (c *Cache) SetDocResult(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, data, c.DocResultTTL).Err()
}

// ── Embedding cache ───────────────────────────────────────────────────────────

func EmbedKey(query, model string) string {
	h := sha256.Sum256([]byte(query + "|" + model))
	return fmt.Sprintf("retrieval:emb:%x", h)
}

func (c *Cache) GetEmbedding(ctx context.Context, key string) ([]float32, bool, error) {
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var vec []float32
	return vec, true, json.Unmarshal(data, &vec)
}

func (c *Cache) SetEmbedding(ctx context.Context, key string, vec []float32) error {
	data, err := json.Marshal(vec)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, data, c.EmbedTTL).Err()
}

// ── QueryHash ─────────────────────────────────────────────────────────────────

func QueryHash(query string, filterIDs []string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(query))
	for _, id := range filterIDs {
		_, _ = h.Write([]byte(id))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
