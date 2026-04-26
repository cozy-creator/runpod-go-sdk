package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the default RunPod REST API base URL
	DefaultBaseURL = "https://rest.runpod.io/v1"

	// DefaultServerlessBaseURL is the base URL for serverless operations
	DefaultServerlessBaseURL = "https://api.runpod.ai"

	// DefaultGraphQLBaseURL is the default RunPod GraphQL API URL
	DefaultGraphQLBaseURL = "https://api.runpod.io/graphql"

	// DefaultTimeout is the default HTTP client timeout
	DefaultTimeout = 30 * time.Second

	// DefaultUserAgent is the default user agent string
	DefaultUserAgent = "runpod-go/1.0.0"

	// MaxRetryAttempts is the maximum number of retry attempts for failed requests
	MaxRetryAttempts = 3

	// RetryDelay is the base delay between retry attempts
	RetryDelay = 1 * time.Second
)

// Client represents the RunPod API client
type Client struct {
	// API configuration
	APIKey            string
	BaseURL           string
	ServerlessBaseURL string
	GraphQLBaseURL    string

	// HTTP client configuration
	HTTPClient *http.Client
	UserAgent  string

	// Client options
	Debug            bool
	MaxRetryAttempts int
	RetryDelay       time.Duration

	// Logger for debug output
	Logger Logger
}

// Logger interface for custom logging
type Logger interface {
	Printf(format string, v ...interface{})
}

// defaultLogger implements a basic logger using the standard log package
type defaultLogger struct{}

func (l *defaultLogger) Printf(format string, v ...interface{}) {
	log.Printf(format, v...)
}

// ClientOption is a function type for configuring the client
type ClientOption func(*Client)

// WithBaseURL sets a custom base URL for the API
func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.BaseURL = baseURL
	}
}

// WithServerlessBaseURL sets a custom base URL for serverless operations
func WithServerlessBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.ServerlessBaseURL = baseURL
	}
}

// WithGraphQLBaseURL sets a custom base URL for GraphQL operations.
func WithGraphQLBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.GraphQLBaseURL = baseURL
	}
}

// WithTimeout sets a custom timeout for HTTP requests
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.HTTPClient.Timeout = timeout
	}
}

// WithHTTPClient sets a custom HTTP client
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		c.HTTPClient = httpClient
	}
}

// WithDebug enables or disables debug logging
func WithDebug(debug bool) ClientOption {
	return func(c *Client) {
		c.Debug = debug
	}
}

// WithUserAgent sets a custom user agent string
func WithUserAgent(userAgent string) ClientOption {
	return func(c *Client) {
		c.UserAgent = userAgent
	}
}

// WithMaxRetryAttempts sets the maximum number of retry attempts
func WithMaxRetryAttempts(maxAttempts int) ClientOption {
	return func(c *Client) {
		c.MaxRetryAttempts = maxAttempts
	}
}

// WithRetryDelay sets the base delay between retry attempts
func WithRetryDelay(delay time.Duration) ClientOption {
	return func(c *Client) {
		c.RetryDelay = delay
	}
}

// WithLogger sets a custom logger for debug output
func WithLogger(logger Logger) ClientOption {
	return func(c *Client) {
		c.Logger = logger
	}
}

// NewClient creates a new RunPod API client
func NewClient(apiKey string, opts ...ClientOption) *Client {
	if apiKey == "" {
		panic("API key is required")
	}

	c := &Client{
		APIKey:            apiKey,
		BaseURL:           DefaultBaseURL,
		ServerlessBaseURL: DefaultServerlessBaseURL,
		GraphQLBaseURL:    DefaultGraphQLBaseURL,
		HTTPClient: &http.Client{
			Timeout: DefaultTimeout,
		},
		UserAgent:        DefaultUserAgent,
		Debug:            false,
		MaxRetryAttempts: MaxRetryAttempts,
		RetryDelay:       RetryDelay,
		Logger:           &defaultLogger{},
	}

	// Apply all options
	for _, opt := range opts {
		opt(c)
	}

	return c
}

// makeRequest performs an HTTP request with retry logic
func (c *Client) makeRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	var lastErr error

	for attempt := 0; attempt <= c.MaxRetryAttempts; attempt++ {
		if attempt > 0 {
			// Wait before retrying
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.RetryDelay * time.Duration(attempt)):
			}
		}

		resp, err := c.doRequest(ctx, method, endpoint, body)
		if err != nil {
			lastErr = err

			// Check if this is a retryable error
			if !c.isRetryableError(err) {
				return nil, err
			}

			if c.Debug {
				c.Logger.Printf("[DEBUG] Request attempt %d failed, retrying: %v", attempt+1, err)
			}
			continue
		}

		// Check if response indicates a retryable error
		if c.isRetryableHTTPStatus(resp.StatusCode) && attempt < c.MaxRetryAttempts {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: retryable server error", resp.StatusCode)

			if c.Debug {
				c.Logger.Printf("[DEBUG] HTTP %d received, retrying attempt %d", resp.StatusCode, attempt+1)
			}
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", c.MaxRetryAttempts+1, lastErr)
}

// doRequest performs a single HTTP request
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	var buf io.Reader

	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		buf = bytes.NewBuffer(jsonBody)
	}

	// Determine the full URL based on endpoint
	fullURL := c.buildURL(endpoint)

	req, err := http.NewRequestWithContext(ctx, method, fullURL, buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	c.setRequestHeaders(req, body != nil)

	if c.Debug {
		c.Logger.Printf("[DEBUG] %s %s", method, fullURL)
		if body != nil {
			bodyJSON, _ := json.MarshalIndent(body, "", "  ")
			c.Logger.Printf("[DEBUG] Request Body: %s", string(bodyJSON))
		}
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, NewNetworkError("HTTP request failed", err)
	}

	return resp, nil
}

// buildURL constructs the full URL for a given endpoint
func (c *Client) buildURL(endpoint string) string {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}

	// If endpoint starts with /v2/ or contains api.runpod.ai, it's a serverless endpoint
	if strings.HasPrefix(endpoint, "/v2/") || strings.Contains(endpoint, "api.runpod.ai") {
		if strings.HasPrefix(endpoint, "/v2/") {
			return c.ServerlessBaseURL + endpoint
		}
		return endpoint // Assume it's already a full URL
	}

	// Standard REST API endpoint
	return c.BaseURL + endpoint
}

// setRequestHeaders sets the required headers for the request
func (c *Client) setRequestHeaders(req *http.Request, hasBody bool) {
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("User-Agent", c.UserAgent)

	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
}

// handleResponse processes the HTTP response and handles errors
func (c *Client) handleResponse(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return NewNetworkError("failed to read response body", err)
	}

	if c.Debug {
		c.Logger.Printf("[DEBUG] Response Status: %d", resp.StatusCode)
		c.Logger.Printf("[DEBUG] Response Body: %s", string(body))
	}

	// Handle error responses
	if resp.StatusCode >= 400 {
		return c.parseErrorResponse(resp.StatusCode, body)
	}

	// Parse successful response
	if v != nil && len(body) > 0 {
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
	}

	return nil
}

// parseErrorResponse parses error responses from the API
func (c *Client) parseErrorResponse(statusCode int, body []byte) error {
	// Try to parse as structured API error
	var apiErr APIError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Message != "" {
		apiErr.StatusCode = statusCode
		return &apiErr
	}

	// Try to parse as simple error message
	var simpleErr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &simpleErr); err == nil {
		message := simpleErr.Error
		if message == "" {
			message = simpleErr.Message
		}
		if message != "" {
			return &APIError{
				StatusCode: statusCode,
				Message:    message,
			}
		}
	}

	// Handle specific status codes
	switch statusCode {
	case 401:
		return NewAuthError("invalid or expired API key")
	case 403:
		return NewAuthError("insufficient permissions")
	case 404:
		return NewAPIError(404, "resource not found")
	case 429:
		retryAfter := "unknown"
		if resp := c.getResponseHeader("Retry-After"); resp != "" {
			retryAfter = resp + " seconds"
		}
		return NewRateLimitError("rate limit exceeded", retryAfter)
	case 500, 502, 503, 504:
		return NewAPIError(statusCode, "server error")
	default:
		// Fallback to raw response body
		return NewAPIError(statusCode, string(body))
	}
}

// getResponseHeader is a helper to get response headers (will be implemented later)
func (c *Client) getResponseHeader(key string) string {
	// This will be implemented to access response headers
	// For now, return empty string
	return ""
}

// isRetryableError determines if an error should trigger a retry
func (c *Client) isRetryableError(err error) bool {
	// Network errors are generally retryable
	if IsNetworkError(err) {
		return true
	}

	// Timeout errors are retryable
	if IsTimeoutError(err) {
		return true
	}

	// API errors with 5xx status codes are retryable
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.IsServerError()
	}

	return false
}

// isRetryableHTTPStatus determines if an HTTP status code should trigger a retry
func (c *Client) isRetryableHTTPStatus(statusCode int) bool {
	switch statusCode {
	case 500, 502, 503, 504:
		return true
	case 429: // Rate limit - could be retryable with backoff
		return true
	default:
		return false
	}
}

// validateRequired checks if required fields are present
func (c *Client) validateRequired(fieldName string, value interface{}) error {
	if value == nil {
		return NewValidationError(fieldName, "is required")
	}

	switch v := value.(type) {
	case string:
		if v == "" {
			return NewValidationError(fieldName, "cannot be empty")
		}
	case []string:
		if len(v) == 0 {
			return NewValidationError(fieldName, "cannot be empty")
		}
	}

	return nil
}

// validatePositive checks if a number is positive
func (c *Client) validatePositive(fieldName string, value int) error {
	if value <= 0 {
		return NewValidationErrorWithValue(fieldName, "must be positive", value)
	}
	return nil
}

// validatePositiveFloat checks if a float is positive
func (c *Client) validatePositiveFloat(fieldName string, value float64) error {
	if value <= 0 {
		return NewValidationErrorWithValue(fieldName, "must be positive", value)
	}
	return nil
}

// buildURLWithParams builds a URL with query parameters
func (c *Client) buildURLWithParams(endpoint string, params map[string]string) string {
	baseURL := c.buildURL(endpoint)

	if len(params) == 0 {
		return baseURL
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}

	q := u.Query()
	for key, value := range params {
		if value != "" {
			q.Set(key, value)
		}
	}

	u.RawQuery = q.Encode()
	return u.String()
}

// buildListURL builds a URL with list options (pagination)
func (c *Client) buildListURL(endpoint string, opts *ListOptions) string {
	if opts == nil {
		return c.buildURL(endpoint)
	}

	params := make(map[string]string)

	if opts.Limit > 0 {
		params["limit"] = strconv.Itoa(opts.Limit)
	}

	if opts.Offset > 0 {
		params["offset"] = strconv.Itoa(opts.Offset)
	}

	return c.buildURLWithParams(endpoint, params)
}

// Get performs a GET request
func (c *Client) Get(ctx context.Context, endpoint string, result interface{}) error {
	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	return c.handleResponse(resp, result)
}

// Post performs a POST request
func (c *Client) Post(ctx context.Context, endpoint string, body interface{}, result interface{}) error {
	resp, err := c.makeRequest(ctx, "POST", endpoint, body)
	if err != nil {
		return err
	}
	return c.handleResponse(resp, result)
}

// Put performs a PUT request
func (c *Client) Put(ctx context.Context, endpoint string, body interface{}, result interface{}) error {
	resp, err := c.makeRequest(ctx, "PUT", endpoint, body)
	if err != nil {
		return err
	}
	return c.handleResponse(resp, result)
}

// Delete performs a DELETE request
func (c *Client) Delete(ctx context.Context, endpoint string) error {
	resp, err := c.makeRequest(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	return c.handleResponse(resp, nil)
}

// Patch performs a PATCH request
func (c *Client) Patch(ctx context.Context, endpoint string, body interface{}, result interface{}) error {
	resp, err := c.makeRequest(ctx, "PATCH", endpoint, body)
	if err != nil {
		return err
	}
	return c.handleResponse(resp, result)
}

// GetAPIKey returns the configured API key (masked for security)
func (c *Client) GetAPIKey() string {
	if len(c.APIKey) <= 8 {
		return "***"
	}
	return c.APIKey[:4] + "***" + c.APIKey[len(c.APIKey)-4:]
}

// GetBaseURL returns the configured base URL
func (c *Client) GetBaseURL() string {
	return c.BaseURL
}

// GetServerlessBaseURL returns the configured serverless base URL
func (c *Client) GetServerlessBaseURL() string {
	return c.ServerlessBaseURL
}

// GetGraphQLBaseURL returns the configured GraphQL base URL.
func (c *Client) GetGraphQLBaseURL() string {
	return c.GraphQLBaseURL
}

// IsDebugEnabled returns whether debug mode is enabled
func (c *Client) IsDebugEnabled() bool {
	return c.Debug
}
