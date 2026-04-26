package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

type graphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

type graphQLError struct {
	Message string `json:"message"`
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphQLError  `json:"errors,omitempty"`
}

// GraphQL executes a typed GraphQL request against RunPod's GraphQL API.
func (c *Client) GraphQL(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	if strings.TrimSpace(query) == "" {
		return NewValidationError("query", "cannot be empty")
	}

	endpoint := c.graphQLURLWithAPIKey()
	req := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	resp, err := c.makeRequest(ctx, "POST", endpoint, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return NewNetworkError("failed to read GraphQL response body", err)
	}

	if c.Debug {
		c.Logger.Printf("[DEBUG] GraphQL response status=%d body=%s", resp.StatusCode, string(body))
	}

	if resp.StatusCode >= 400 {
		return c.parseErrorResponse(resp.StatusCode, body)
	}

	var envelope graphQLResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("failed to unmarshal GraphQL response envelope: %w", err)
	}

	if len(envelope.Errors) > 0 {
		msg := strings.TrimSpace(envelope.Errors[0].Message)
		if msg == "" {
			msg = "GraphQL request failed"
		}
		return NewAPIErrorWithDetails(400, "graphql error", msg)
	}

	if result == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}

	if err := json.Unmarshal(envelope.Data, result); err != nil {
		return fmt.Errorf("failed to unmarshal GraphQL data payload: %w", err)
	}
	return nil
}

func (c *Client) graphQLURLWithAPIKey() string {
	base := strings.TrimSpace(c.GraphQLBaseURL)
	if base == "" {
		base = DefaultGraphQLBaseURL
	}

	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	if q.Get("api_key") == "" {
		q.Set("api_key", c.APIKey)
		u.RawQuery = q.Encode()
	}
	return u.String()
}
