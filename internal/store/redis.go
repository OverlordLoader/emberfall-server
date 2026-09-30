package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is used for exactly these six things (design §3.7). Postgres is the
// source of truth; everything here is reconstructible.

// ---- WS fan-out pub/sub: channel "world:{id}" ----

// PublishWorld publishes a JSON event to a world's fan-out channel.
func (s *Store) PublishWorld(ctx context.Context, worldID int, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.Redis.Publish(ctx, fmt.Sprintf("world:%d", worldID), b).Err()
}

// SubscribeWorld returns a subscription to a world's fan-out channel.
func (s *Store) SubscribeWorld(ctx context.Context, worldID int) *redis.PubSub {
	return s.Redis.Subscribe(ctx, fmt.Sprintf("world:%d", worldID))
}

// ---- presence: set "presence:{world_id}" ----

func (s *Store) PresenceAdd(ctx context.Context, worldID int, playerID string) error {
	k := fmt.Sprintf("presence:%d", worldID)
	pipe := s.Redis.Pipeline()
	pipe.SAdd(ctx, k, playerID)
	pipe.Expire(ctx, k, 5*time.Minute)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *Store) PresenceRemove(ctx context.Context, worldID int, playerID string) error {
	return s.Redis.SRem(ctx, fmt.Sprintf("presence:%d", worldID), playerID).Err()
}

func (s *Store) PresenceCount(ctx context.Context, worldID int) (int64, error) {
	return s.Redis.SCard(ctx, fmt.Sprintf("presence:%d", worldID)).Result()
}

// ---- rate-limit buckets: "rl:{player_id}:{endpoint}" ----
// Simple fixed-window counter. Returns (allowed, retryAfter).

func (s *Store) RateLimit(ctx context.Context, playerID, bucket string, limit int, window time.Duration) (bool, time.Duration) {
	k := fmt.Sprintf("rl:%s:%s", playerID, bucket)
	n, err := s.Redis.Incr(ctx, k).Result()
	if err != nil {
		return true, 0 // fail open on redis errors; PG validation still applies
	}
	if n == 1 {
		_ = s.Redis.Expire(ctx, k, window).Err()
	}
	if n > int64(limit) {
		ttl, _ := s.Redis.TTL(ctx, k).Result()
		return false, ttl
	}
	return true, 0
}

// ---- sweeper distributed lock: "lock:sweeper:{world_id}" ----

func (s *Store) SweeperLock(ctx context.Context, worldID int) (bool, error) {
	return s.Redis.SetNX(ctx, fmt.Sprintf("lock:sweeper:%d", worldID), "1", 5*time.Second).Result()
}

// ---- hot city cache: "city:{id}" (30s TTL) ----

func (s *Store) CityCacheGet(ctx context.Context, cityID string) ([]byte, bool) {
	b, err := s.Redis.Get(ctx, "city:"+cityID).Bytes()
	if err != nil {
		return nil, false
	}
	return b, true
}

func (s *Store) CityCacheSet(ctx context.Context, cityID string, payload []byte) {
	_ = s.Redis.Set(ctx, "city:"+cityID, payload, 30*time.Second).Err()
}

func (s *Store) CityCacheInvalidate(ctx context.Context, cityID string) {
	_ = s.Redis.Del(ctx, "city:"+cityID).Err()
}

// ---- idempotency short-circuit: "idem:{player}:{key}" (24h) ----

func (s *Store) IdemGet(ctx context.Context, playerID, key string) ([]byte, bool) {
	b, err := s.Redis.Get(ctx, fmt.Sprintf("idem:%s:%s", playerID, key)).Bytes()
	if err != nil {
		return nil, false
	}
	return b, true
}

func (s *Store) IdemSet(ctx context.Context, playerID, key string, response []byte) {
	_ = s.Redis.Set(ctx, fmt.Sprintf("idem:%s:%s", playerID, key), response, 24*time.Hour).Err()
}
