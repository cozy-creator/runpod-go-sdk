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

// GetClientBalanceUSD returns RunPod's exact JSON decimal balance for the
// authenticated account. An omitted/null field or account is refused rather
// than silently becoming zero; the SDK never converts provider money through
// binary floating point.
func (c *Client) GetClientBalanceUSD(ctx context.Context) (json.Number, error) {
	var result struct {
		Myself *struct {
			ClientBalance *json.Number `json:"clientBalance"`
		} `json:"myself"`
	}
	if err := c.GraphQL(ctx, clientBalanceQuery, nil, &result); err != nil {
		return "", fmt.Errorf("get RunPod client balance: %w", err)
	}
	if result.Myself == nil {
		return "", fmt.Errorf("get RunPod client balance: response omitted myself")
	}
	if result.Myself.ClientBalance == nil {
		return "", fmt.Errorf("get RunPod client balance: response omitted myself.clientBalance")
	}
	return *result.Myself.ClientBalance, nil
}
