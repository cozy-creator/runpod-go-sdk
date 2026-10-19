package runpod

import (
	"context"
	"sync"
	"time"
)

// clientCooldown belongs to the authenticated client, not a request's context.
// Requests already in flight may finish; no lock is held over network IO.
type clientCooldown struct {
	mu    sync.Mutex
	until time.Time
	err   APIError
	clock func() time.Time
}

func (c *clientCooldown) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

func (c *Client) retainRateLimit(err *APIError, delay time.Duration) {
	until := c.cooldown.now().Add(delay)
	c.cooldown.mu.Lock()
	defer c.cooldown.mu.Unlock()
	if until.After(c.cooldown.until) {
		c.cooldown.until = until
		c.cooldown.err = *err
		c.cooldown.err.ResponseBody = append([]byte(nil), err.ResponseBody...)
	}
}

func (c *Client) rateLimited() *APIError {
	c.cooldown.mu.Lock()
	defer c.cooldown.mu.Unlock()
	remaining := c.cooldown.until.Sub(c.cooldown.now())
	if remaining <= 0 {
		return nil
	}
	err := c.cooldown.err
	err.ResponseBody = append([]byte(nil), err.ResponseBody...)
	err.RetryAfter = remaining
	return &err
}

// Keep ordinary short same-call retries, but return a typed provider refusal
// when waiting exceeds the ordinary backoff ceiling or the caller's budget.
// The full server deadline remains retained for every later request either way.
func (c *Client) waitRateLimit(ctx context.Context) error {
	for {
		limited := c.rateLimited()
		if limited == nil {
			return nil
		}
		if limited.RetryAfter > maxRetryDelay {
			return limited
		}
		if deadline, ok := ctx.Deadline(); ok && limited.RetryAfter >= time.Until(deadline) {
			return limited
		}
		timer := time.NewTimer(limited.RetryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return limited
		case <-timer.C:
		}
	}
}
