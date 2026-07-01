package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/saksham/token-guard-ai/internal/config"
)

type Client struct {
	rdb         *redis.Client
	reserveSHA  string
	releaseSHA  string
	settleSHA   string
	ttl         time.Duration
}

func NewClient(cfg config.Config) (*Client, error) {
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	opt.PoolSize = cfg.RedisPoolSize
	opt.MinIdleConns = cfg.RedisMinIdleConns
	opt.ReadTimeout = cfg.RedisCommandTimeout
	opt.WriteTimeout = cfg.RedisCommandTimeout

	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	c := &Client{
		rdb: rdb,
		ttl: cfg.ReservationTTL,
	}
	if err := c.loadScripts(ctx); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return c, nil
}

func NewClientFromRedis(rdb *redis.Client, ttl time.Duration) (*Client, error) {
	c := &Client{rdb: rdb, ttl: ttl}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.loadScripts(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) loadScripts(ctx context.Context) error {
	var err error
	c.reserveSHA, err = c.rdb.ScriptLoad(ctx, reserveBudgetLua).Result()
	if err != nil {
		return fmt.Errorf("load reserve_budget: %w", err)
	}
	c.releaseSHA, err = c.rdb.ScriptLoad(ctx, releaseBudgetLua).Result()
	if err != nil {
		return fmt.Errorf("load release_budget: %w", err)
	}
	c.settleSHA, err = c.rdb.ScriptLoad(ctx, settleBudgetLua).Result()
	if err != nil {
		return fmt.Errorf("load settle_budget: %w", err)
	}
	return nil
}

func (c *Client) Close() error {
	if c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

func (c *Client) Ready(ctx context.Context) bool {
	return c.rdb.Ping(ctx).Err() == nil
}

func budgetKey(bucketID string) string  { return "budget:" + bucketID }
func reservationKey(requestID string) string { return "reservation:" + requestID }
