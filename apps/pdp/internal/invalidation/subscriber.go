// Package invalidation handles Redis pub/sub invalidation messages from the admin console.
//
// When a policy is mutated, the admin console (Phase 4) publishes on
// "policy.invalidate.{tenantId}" with a JSON payload:
//
//	{"policy_set_version": 42, "policy_ids": ["p1", "p2"]}
//
// This subscriber increments the local version counter, evicts L1 cache entries
// for the tenant, and schedules a recompile of the affected policies.
//
// Fallback: a 30-second poller compares DB policy_set_version against the local
// counter and triggers a refresh if they diverge — this covers pub/sub outages.
package invalidation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	pdpmetrics "github.com/governance-platform/pdp/internal/metrics"
)

const (
	channelPrefix  = "policy.invalidate."
	pollInterval   = 30 * time.Second
)

// InvalidateMessage is the payload published on "policy.invalidate.{tenantId}".
type InvalidateMessage struct {
	PolicySetVersion int64    `json:"policy_set_version"`
	PolicyIDs        []string `json:"policy_ids"`
}

// VersionStore holds the per-tenant policy-set version as observed by this node.
type VersionStore struct {
	mu       sync.RWMutex
	versions map[string]int64
}

func NewVersionStore() *VersionStore {
	return &VersionStore{versions: make(map[string]int64)}
}

func (v *VersionStore) Get(tenantID string) int64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.versions[tenantID]
}

func (v *VersionStore) Set(tenantID string, ver int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.versions[tenantID] = ver
}

// InvalidateCallback is called when a tenant's policies should be reloaded.
// tenantID is the affected tenant; policyIDs lists the changed policies (may be empty = full reload).
type InvalidateCallback func(ctx context.Context, tenantID string, policyIDs []string)

// Subscriber listens for Redis pub/sub invalidation messages.
type Subscriber struct {
	rdb        *redis.Client
	versions   *VersionStore
	callback   InvalidateCallback
	log        zerolog.Logger
	tenants    sync.Map // tenantID → subscribed (bool)
	activePolls int32   // atomic count of active poll goroutines
}

// New creates a Subscriber.
func New(rdb *redis.Client, versions *VersionStore, callback InvalidateCallback, log zerolog.Logger) *Subscriber {
	return &Subscriber{
		rdb:      rdb,
		versions: versions,
		callback: callback,
		log:      log.With().Str("component", "invalidation").Logger(),
	}
}

// Subscribe starts listening on the Redis pub/sub channel for tenantID.
// Call this when a tenant's policies are first loaded.
// The goroutine exits when ctx is cancelled.
func (s *Subscriber) Subscribe(ctx context.Context, tenantID string) {
	if _, loaded := s.tenants.LoadOrStore(tenantID, true); loaded {
		return // already subscribed
	}
	channel := channelPrefix + tenantID
	sub := s.rdb.Subscribe(ctx, channel)
	go func() {
		defer s.tenants.Delete(tenantID)
		defer sub.Close()
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				s.handleMessage(ctx, tenantID, msg.Payload)
			}
		}
	}()
}

func (s *Subscriber) handleMessage(ctx context.Context, tenantID, payload string) {
	var msg InvalidateMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		s.log.Error().Err(err).Str("tenant", tenantID).Msg("malformed invalidation message")
		pdpmetrics.InvalidateTotal.WithLabelValues("parse_error").Inc()
		return
	}
	current := s.versions.Get(tenantID)
	if msg.PolicySetVersion <= current {
		// Already at this version or newer.
		pdpmetrics.InvalidateTotal.WithLabelValues("noop").Inc()
		return
	}
	s.versions.Set(tenantID, msg.PolicySetVersion)
	s.log.Info().
		Str("tenant", tenantID).
		Int64("version", msg.PolicySetVersion).
		Strs("policy_ids", msg.PolicyIDs).
		Msg("invalidation received — reloading")
	pdpmetrics.InvalidateTotal.WithLabelValues("ok").Inc()
	s.callback(ctx, tenantID, msg.PolicyIDs)
}

// VersionChecker is a closure that returns the canonical policy_set_version from DB.
type VersionChecker func(ctx context.Context, tenantID string) (int64, error)

// StartPoller starts a goroutine that polls for version drift every 30 seconds.
// This is the belt-and-suspenders fallback for pub/sub outages.
func (s *Subscriber) StartPoller(ctx context.Context, tenantID string, checker VersionChecker) {
	atomic.AddInt32(&s.activePolls, 1)
	go func() {
		defer atomic.AddInt32(&s.activePolls, -1)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				dbVer, err := checker(ctx, tenantID)
				if err != nil {
					s.log.Warn().Err(err).Str("tenant", tenantID).Msg("poll: version check failed")
					continue
				}
				local := s.versions.Get(tenantID)
				if dbVer > local {
					s.log.Info().
						Str("tenant", tenantID).
						Int64("db_ver", dbVer).
						Int64("local_ver", local).
						Msg("poll: version drift detected — triggering reload")
					s.versions.Set(tenantID, dbVer)
					s.callback(ctx, tenantID, nil) // nil = full reload
				}
			}
		}
	}()
}

// Publish sends an invalidation message for tenantID.
// Called by the admin console (Phase 4) after any policy mutation.
func Publish(ctx context.Context, rdb *redis.Client, tenantID string, version int64, policyIDs []string) error {
	msg, err := json.Marshal(InvalidateMessage{
		PolicySetVersion: version,
		PolicyIDs:        policyIDs,
	})
	if err != nil {
		return fmt.Errorf("invalidation: marshal: %w", err)
	}
	channel := channelPrefix + tenantID
	return rdb.Publish(ctx, channel, msg).Err()
}
