package runpod

import (
	"context"
	"fmt"
	"strings"
)

const accountIDQuery = `query { myself { id } }`

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
