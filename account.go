package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const accountIDQuery = `query { myself { id } }`

const clientBalanceQuery = `query { myself { clientBalance } }`

// GetAccountID returns the stable RunPod account ID authenticated by this
// client. Unlike an API key, the account ID remains unchanged when credentials
// are rotated, so callers can use it to scope durable provider resources.
func (c *Client) GetAccountID(ctx context.Context) (string, error) {
	var result struct {
		Myself struct {
			ID string `json:"id"`
		} `json:"myself"`
	}
	if err := c.GraphQL(ctx, accountIDQuery, nil, &result); err != nil {
		return "", fmt.Errorf("get RunPod account identity: %w", err)
	}
	accountID := strings.TrimSpace(result.Myself.ID)
	if accountID == "" {
		return "", fmt.Errorf("get RunPod account identity: response omitted myself.id")
	}
	return accountID, nil
}

// GetClientBalanceUSDMicros returns RunPod's balance as exact integer USD
// micros. An omitted/null field or account, sub-micro value, or overflow is
// refused rather than silently becoming zero or rounding.
func (c *Client) GetClientBalanceUSDMicros(ctx context.Context) (int64, error) {
	var result struct {
		Myself *struct {
			ClientBalance *json.Number `json:"clientBalance"`
		} `json:"myself"`
	}
	if err := c.GraphQL(ctx, clientBalanceQuery, nil, &result); err != nil {
		return 0, fmt.Errorf("get RunPod client balance: %w", err)
	}
	if result.Myself == nil {
		return 0, fmt.Errorf("get RunPod client balance: response omitted myself")
	}
	if result.Myself.ClientBalance == nil {
		return 0, fmt.Errorf("get RunPod client balance: response omitted myself.clientBalance")
	}
	micros, err := parseUSDMicros(result.Myself.ClientBalance.String())
	if err != nil {
		return 0, fmt.Errorf("get RunPod client balance: %w", err)
	}
	return micros, nil
}
