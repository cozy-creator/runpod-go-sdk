package runpod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type graphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLErrorLocation is one source location reported by GraphQL validation
// or execution.
type GraphQLErrorLocation struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// GraphQLError preserves one complete provider error entry. Extensions stays
// raw by key so newly added provider diagnostics are not silently discarded.
type GraphQLError struct {
	Message    string                     `json:"message"`
	Locations  []GraphQLErrorLocation     `json:"locations,omitempty"`
	Path       []interface{}              `json:"path,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// GraphQLResponseError preserves every provider error rather than collapsing
// the response to the first message or pretending it was an HTTP 400.
type GraphQLResponseError struct {
	Errors []GraphQLError
}

func (e *GraphQLResponseError) Error() string {
	messages := make([]string, 0, len(e.Errors))
	for _, item := range e.Errors {
		if message := strings.TrimSpace(item.Message); message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		return "runpod: GraphQL request failed without provider error text"
	}
	return "runpod: GraphQL request failed: " + strings.Join(messages, "; ")
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQL executes a typed GraphQL request against RunPod's GraphQL API.
func (c *Client) GraphQL(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	envelope, err := c.graphQL(ctx, query, variables)
	if err != nil {
		return err
	}

	if len(envelope.Errors) > 0 {
		return &GraphQLResponseError{Errors: append([]GraphQLError(nil), envelope.Errors...)}
	}

	if result == nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}

	if err := json.Unmarshal(envelope.Data, result); err != nil {
		return fmt.Errorf("failed to unmarshal GraphQL data payload: %w", err)
	}
	return nil
}

// graphQL preserves partial data and alias-scoped errors for batch queries.
func (c *Client) graphQL(ctx context.Context, query string, variables map[string]interface{}) (graphQLResponse, error) {
	return c.graphQLWithRetryLimit(ctx, query, variables, c.maxRetryAttempts)
}

func (c *Client) graphQLWithRetryLimit(ctx context.Context, query string, variables map[string]interface{}, maxRetries int) (graphQLResponse, error) {
	if strings.TrimSpace(query) == "" {
		return graphQLResponse{}, NewValidationError("query", "cannot be empty")
	}

	endpoint := strings.TrimSpace(c.graphqlBaseURL)
	if endpoint == "" {
		endpoint = DefaultGraphQLBaseURL
	}
	req := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	bodyJSON, err := json.Marshal(req)
	if err != nil {
		return graphQLResponse{}, fmt.Errorf("marshal GraphQL request: %w", err)
	}
	resp, err := c.makeRequestBytesWithRetryLimit(ctx, "POST", endpoint, bodyJSON, nil, maxRetries)
	if err != nil {
		return graphQLResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return graphQLResponse{}, fmt.Errorf("failed to read GraphQL response body: %w", err)
	}

	if c.debug {
		c.logger.Printf("[DEBUG] GraphQL response status=%d body=%s", resp.StatusCode, string(body))
	}

	if resp.StatusCode >= 400 {
		return graphQLResponse{}, c.parseErrorResponse(resp.StatusCode, resp.Header, body)
	}

	var envelope graphQLResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return graphQLResponse{}, fmt.Errorf("failed to unmarshal GraphQL response envelope: %w", err)
	}

	return envelope, nil
}
