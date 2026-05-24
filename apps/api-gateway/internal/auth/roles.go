package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const roleCacheTTL = 60 * time.Second

// RoleResolver resolves a user's flat role list from PostgreSQL with a Redis cache.
// The cache key is versioned by users.updated_at so that role changes invalidate
// the cache within the TTL. Sensitive grants also fan out via pub/sub (see PubSub).
type RoleResolver struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
}

// NewRoleResolver returns a RoleResolver.
func NewRoleResolver(pool *pgxpool.Pool, rdb *redis.Client) *RoleResolver {
	return &RoleResolver{pool: pool, rdb: rdb}
}

// Resolve returns the flat list of role names for (tenantID, userID).
// It first checks Redis; on miss, queries PostgreSQL and caches the result.
// The caller must have already verified the JWT; this function runs without
// SET LOCAL (it queries using the migrator/superuser path for cache population).
func (r *RoleResolver) Resolve(ctx context.Context, tenantID, userID string) ([]string, error) {
	cacheKey, err := r.cacheKey(ctx, tenantID, userID)
	if err != nil {
		// On any cache-key error, fall back to DB.
		return r.resolveFromDB(ctx, tenantID, userID)
	}

	// Redis hit
	if cached, err := r.rdb.SMembers(ctx, cacheKey).Result(); err == nil && len(cached) > 0 {
		return cached, nil
	}

	roles, err := r.resolveFromDB(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}

	// Populate cache (best effort; ignore errors)
	if len(roles) > 0 {
		pipe := r.rdb.Pipeline()
		for _, role := range roles {
			pipe.SAdd(ctx, cacheKey, role)
		}
		pipe.Expire(ctx, cacheKey, roleCacheTTL)
		_, _ = pipe.Exec(ctx)
	}
	return roles, nil
}

// InvalidateCache removes the role cache for (tenantID, userID) for all updated_at versions.
// Called when a role grant/revoke occurs. Pub/sub fanout to other gateway pods
// is wired in Phase 12; for now a targeted key delete is sufficient.
func (r *RoleResolver) InvalidateCache(ctx context.Context, tenantID, userID string) {
	// Delete by pattern — scan and delete keys matching user:{userID}:* under the tenant prefix.
	pattern := fmt.Sprintf("roles:%s:%s:*", tenantID, userID)
	var cursor uint64
	for {
		keys, nextCursor, err := r.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			break
		}
		if len(keys) > 0 {
			r.rdb.Del(ctx, keys...)
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
}

// cacheKey builds a cache key versioned by users.updated_at.
// This makes any update to the users row (including role changes that bump updated_at
// via trigger) automatically produce a cache miss.
func (r *RoleResolver) cacheKey(ctx context.Context, tenantID, userID string) (string, error) {
	var updatedAt time.Time
	err := r.pool.QueryRow(ctx,
		"SELECT updated_at FROM users WHERE id = $1 AND tenant_id = $2 AND status = 'active'",
		userID, tenantID,
	).Scan(&updatedAt)
	if err != nil {
		return "", fmt.Errorf("roles: fetch user updated_at: %w", err)
	}
	return fmt.Sprintf("roles:%s:%s:%d", tenantID, userID, updatedAt.UnixMicro()), nil
}

// resolveFromDB queries the role hierarchy and flattens it.
// It uses a recursive CTE to traverse parent_role_id links.
func (r *RoleResolver) resolveFromDB(ctx context.Context, tenantID, userID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE role_tree AS (
		  SELECT r.id, r.name, r.parent_role_id
		  FROM user_roles ur
		  JOIN roles r ON r.id = ur.role_id
		  WHERE ur.user_id = $1
		    AND ur.tenant_id = $2
		    AND (ur.expires_at IS NULL OR ur.expires_at > now())
		  UNION ALL
		  SELECT r.id, r.name, r.parent_role_id
		  FROM roles r
		  JOIN role_tree rt ON rt.parent_role_id = r.id
		)
		SELECT DISTINCT name FROM role_tree
	`, userID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("roles: query: %w", err)
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("roles: scan: %w", err)
		}
		roles = append(roles, name)
	}
	return roles, rows.Err()
}
