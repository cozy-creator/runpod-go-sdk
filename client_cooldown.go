package runpod

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
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

func (c *Client) retainRateLimit(err *APIError, delay time.Duration) time.Time {
	until := c.cooldown.now().Add(delay)
	c.cooldown.mu.Lock()
	defer c.cooldown.mu.Unlock()
	if until.After(c.cooldown.until) {
		c.cooldown.until = until
		c.cooldown.err = *err
		c.cooldown.err.ResponseBody = append([]byte(nil), err.ResponseBody...)
	}
	return until
}

// Reading the provider's body adds diagnostics, never more cooldown time or
// replacement of a later, longer limit observed by another in-flight request.
func (c *Client) enrichRateLimit(until time.Time, err *APIError) {
	c.cooldown.mu.Lock()
	defer c.cooldown.mu.Unlock()
	if c.cooldown.until.Equal(until) {
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

// Cooldown is how long the provider asked this client to wait; zero when it may call now.
func (c *Client) Cooldown() time.Duration {
	if limited := c.rateLimited(); limited != nil {
		return limited.RetryAfter
	}
	return 0
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

// quotaDelay is the longest exhausted RateLimit window, such as "hour";r=0;t=6000: r is the
// requests left in the window and t the seconds until it resets.
func quotaDelay(header http.Header) time.Duration {
	var delay time.Duration
	for _, value := range header.Values("RateLimit") {
		for _, window := range strings.Split(value, ",") {
			var remaining, reset string
			for _, param := range strings.Split(window, ";")[1:] {
				name, v, _ := strings.Cut(strings.TrimSpace(param), "=")
				switch name {
				case "r":
					remaining = v
				case "t":
					reset = v
				}
			}
			if t, ok := delaySeconds(reset); ok && remaining == "0" {
				delay = max(delay, t)
			}
		}
	}
	return delay
}

// delaySeconds parses a count of seconds, saturating one too large rather than dropping it.
func delaySeconds(v string) (time.Duration, bool) {
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}
