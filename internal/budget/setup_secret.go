package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const setupSessionTTL = 15 * time.Minute

func setupSessionKey(sessionID string) string {
	return "setup:session:" + sessionID
}

func setupOrgKey(sessionID string) string {
	return "setup:org:" + sessionID
}

// PutSetupSecret stores the one-time tg_ key and org id for /setup reveal (TTL 15m).
func (c *Client) PutSetupSecret(ctx context.Context, sessionID, orgID, rawKey string) error {
	if sessionID == "" || rawKey == "" {
		return fmt.Errorf("setup secret requires session_id and key")
	}
	pipe := c.rdb.Pipeline()
	pipe.Set(ctx, setupSessionKey(sessionID), rawKey, setupSessionTTL)
	pipe.Set(ctx, setupOrgKey(sessionID), orgID, setupSessionTTL)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store setup secret: %w", err)
	}
	return nil
}

// TakeSetupSecret GETDELs the one-time key. Empty string means already revealed or expired.
func (c *Client) TakeSetupSecret(ctx context.Context, sessionID string) (string, error) {
	val, err := c.rdb.GetDel(ctx, setupSessionKey(sessionID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return val, nil
}

// SetupOrgID returns the org bound to a checkout session (for Slack form after reveal).
func (c *Client) SetupOrgID(ctx context.Context, sessionID string) (string, error) {
	val, err := c.rdb.Get(ctx, setupOrgKey(sessionID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return val, nil
}
