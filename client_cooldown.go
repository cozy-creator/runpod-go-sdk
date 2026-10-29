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

func (c *clientCooldown) retainRateLimit(err *APIError, delay time.Duration) time.Time {
	until := c.now().Add(delay)
	c.mu.Lock()
	defer c.mu.Unlock()
	if until.After(c.until) {
		c.until = until
		c.err = *err
		c.err.ResponseBody = append([]byte(nil), err.ResponseBody...)
	}
	return until
}

// Reading the provider's body adds diagnostics, never more cooldown time or
// replacement of a later, longer limit observed by another in-flight request.
func (c *clientCooldown) enrichRateLimit(until time.Time, err *APIError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.until.Equal(until) {
		c.err = *err
		c.err.ResponseBody = append([]byte(nil), err.ResponseBody...)
	}
}

func (c *clientCooldown) rateLimited() *APIError {
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := c.until.Sub(c.now())
	if remaining <= 0 {
		return nil
	}
	err := c.err
	err.ResponseBody = append([]byte(nil), err.ResponseBody...)
	err.RetryAfter = remaining
	return &err
}

// Cooldown is the REST v2 control-plane cooldown. Inference endpoints have
// independent quotas, available through ServerlessCooldown.
func (c *Client) Cooldown() time.Duration {
	if limited := c.cooldown.rateLimited(); limited != nil {
		return limited.RetryAfter
	}
	return 0
}

// ServerlessCooldown reports the independent quota cooldown for one endpoint.
func (c *Client) ServerlessCooldown(endpointID string) time.Duration {
	gate := c.requestCooldown(c.serverlessURL("/v2/" + endpointID + "/run"))
	if limited := gate.rateLimited(); limited != nil {
		return limited.RetryAfter
	}
	return 0
}

func (c *Client) retainRateLimit(err *APIError, delay time.Duration) time.Time {
	return c.cooldown.retainRateLimit(err, delay)
}
func (c *Client) rateLimited() *APIError { return c.cooldown.rateLimited() }

// The inference service limits each endpoint independently from the REST
// control plane. Recognize only the inference operations built by jobs.go;
// pod log URLs can share the fixture host without sharing an inference quota.
func (c *Client) requestCooldown(endpoint string) *clientCooldown {
	prefix := strings.TrimRight(c.serverlessBaseURL, "/") + "/v2/"
	if !strings.HasPrefix(endpoint, prefix) {
		return &c.cooldown
	}
	parts := strings.Split(strings.TrimPrefix(endpoint, prefix), "/")
	if len(parts) < 2 {
		return &c.cooldown
	}
	switch parts[1] {
	case "run", "runsync", "status", "stream", "cancel", "retry", "purge-queue", "health":
	default:
		return &c.cooldown
	}
	c.inferenceMu.Lock()
	defer c.inferenceMu.Unlock()
	if c.inferenceCooldowns == nil {
		c.inferenceCooldowns = map[string]*clientCooldown{}
	}
	if c.inferenceCooldowns[parts[0]] == nil {
		c.inferenceCooldowns[parts[0]] = &clientCooldown{}
	}
	return c.inferenceCooldowns[parts[0]]
}

// Keep ordinary short same-call retries, but return a typed provider refusal
// when waiting exceeds the ordinary backoff ceiling or the caller's budget.
// The full server deadline remains retained for every later request either way.
func (c *clientCooldown) waitRateLimit(ctx context.Context) error {
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
