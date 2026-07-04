package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
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

	// DefaultMaxRetryAttempts is the default number of retry attempts for
	// failed requests.
	DefaultMaxRetryAttempts = 3

	// DefaultRetryDelay is the base delay for exponential backoff between
	// retry attempts.
	DefaultRetryDelay = 1 * time.Second

	// maxRetryDelay caps the exponential backoff.
	maxRetryDelay = 30 * time.Second
)

// Client is the RunPod API client. Construct with NewClient; configure with
// ClientOption functions. Safe for concurrent use.
type Client struct {
	apiKey            string
	baseURL           string
	serverlessBaseURL string
	graphqlBaseURL    string

	httpClient *http.Client
	userAgent  string

	debug            bool
	maxRetryAttempts int
	retryDelay       time.Duration

	logger Logger
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

// WithBaseURL sets a custom base URL for the REST API
func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.baseURL = baseURL
	}
}

// WithServerlessBaseURL sets a custom base URL for serverless operations
func WithServerlessBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.serverlessBaseURL = baseURL
	}
}

// WithGraphQLBaseURL sets a custom base URL for GraphQL operations.
func WithGraphQLBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.graphqlBaseURL = baseURL
	}
}

// WithTimeout sets a custom timeout for HTTP requests
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.httpClient.Timeout = timeout
	}
}

// WithHTTPClient sets a custom HTTP client
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

// WithDebug enables or disables debug logging
func WithDebug(debug bool) ClientOption {
	return func(c *Client) {
		c.debug = debug
	}
}

// WithUserAgent sets a custom user agent string
func WithUserAgent(userAgent string) ClientOption {
	return func(c *Client) {
		c.userAgent = userAgent
	}
}

// WithMaxRetryAttempts sets the maximum number of retry attempts
func WithMaxRetryAttempts(maxAttempts int) ClientOption {
	return func(c *Client) {
		c.maxRetryAttempts = maxAttempts
	}
}

// WithRetryDelay sets the base delay for exponential backoff between retries
func WithRetryDelay(delay time.Duration) ClientOption {
	return func(c *Client) {
		c.retryDelay = delay
	}
}

// WithLogger sets a custom logger for debug output
func WithLogger(logger Logger) ClientOption {
	return func(c *Client) {
		c.logger = logger
	}
}

// NewClient creates a new RunPod API client.
func NewClient(apiKey string, opts ...ClientOption) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, NewValidationError("apiKey", "cannot be empty")
	}

	c := &Client{
		apiKey:            apiKey,
		baseURL:           DefaultBaseURL,
		serverlessBaseURL: DefaultServerlessBaseURL,
		graphqlBaseURL:    DefaultGraphQLBaseURL,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
		},
		userAgent:        DefaultUserAgent,
		maxRetryAttempts: DefaultMaxRetryAttempts,
		retryDelay:       DefaultRetryDelay,
		logger:           &defaultLogger{},
	}

	for _, opt := range opts {
		opt(c)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	if c.logger == nil {
		c.logger = &defaultLogger{}
	}

	return c, nil
}

// makeRequest performs an HTTP request with retry logic.
//
// Retries use exponential backoff with jitter, honoring the Retry-After
// header on 429 responses. 5xx responses are retried only for non-POST
// requests: POSTs are not idempotent (retrying POST /pods or /v2/{id}/run
// can create duplicate pods or jobs), and RunPod signals ordinary stock-outs
// as 500 "no instances available". 429 is safe to retry for any method since
// the request was rejected before processing.
func (c *Client) makeRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	var jsonBody []byte
	if body != nil {
		var err error
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	var lastErr error
	var retryAfter time.Duration

	for attempt := 0; attempt <= c.maxRetryAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.backoff(attempt, retryAfter)):
			}
			retryAfter = 0
		}

		resp, err := c.doRequest(ctx, method, endpoint, jsonBody, body)
		if err != nil {
			lastErr = err

			if !c.isRetryableError(err) {
				return nil, err
			}

			if c.debug {
				c.logger.Printf("[DEBUG] Request attempt %d failed, retrying: %v", attempt+1, err)
			}
			continue
		}

		if c.isRetryableHTTPStatus(method, resp.StatusCode) && attempt < c.maxRetryAttempts {
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: retryable server error", resp.StatusCode)

			if c.debug {
				c.logger.Printf("[DEBUG] HTTP %d received, retrying attempt %d", resp.StatusCode, attempt+1)
			}
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("request failed after %d attempts: %w", c.maxRetryAttempts+1, lastErr)
}

// backoff computes the wait before retry `attempt` (1-based). A
// server-provided Retry-After wins; otherwise exponential backoff with
// jitter on the upper half of the window.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	d := c.retryDelay
	if d <= 0 {
		d = DefaultRetryDelay
	}
	for i := 1; i < attempt && d < maxRetryDelay; i++ {
		d *= 2
	}
	if d > maxRetryDelay {
		d = maxRetryDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// parseRetryAfter parses a Retry-After header value: either delay-seconds or
// an HTTP date. Returns 0 when absent/unparseable.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// doRequest performs a single HTTP request.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, jsonBody []byte, body interface{}) (*http.Response, error) {
	var buf io.Reader
	if jsonBody != nil {
		buf = bytes.NewReader(jsonBody)
	}

	fullURL := c.buildURL(endpoint)

	req, err := http.NewRequestWithContext(ctx, method, fullURL, buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.setRequestHeaders(req, jsonBody != nil)

	if c.debug {
		c.logger.Printf("[DEBUG] %s %s", method, fullURL)
		if body != nil {
			bodyJSON, _ := json.MarshalIndent(body, "", "  ")
			c.logger.Printf("[DEBUG] Request Body: %s", string(bodyJSON))
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	return resp, nil
}

// buildURL constructs the full URL for a given endpoint. Absolute URLs pass
// through unchanged (the GraphQL and serverless method groups build absolute
// URLs against their own bases); anything else is a REST API path.
func (c *Client) buildURL(endpoint string) string {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	return c.baseURL + endpoint
}

// serverlessURL builds an absolute URL against the serverless base.
func (c *Client) serverlessURL(path string) string {
	return strings.TrimRight(c.serverlessBaseURL, "/") + path
}

// setRequestHeaders sets the required headers for the request
func (c *Client) setRequestHeaders(req *http.Request, hasBody bool) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)

	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
}

// handleResponse processes the HTTP response and handles errors
func (c *Client) handleResponse(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if c.debug {
		c.logger.Printf("[DEBUG] Response Status: %d", resp.StatusCode)
		c.logger.Printf("[DEBUG] Response Body: %s", string(body))
	}

	if resp.StatusCode >= 400 {
		return c.parseErrorResponse(resp.StatusCode, resp.Header, body)
	}

	if v != nil && len(body) > 0 {
		if err := json.Unmarshal(body, v); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
	}

	return nil
}

// parseErrorResponse parses error responses from the API into *APIError.
func (c *Client) parseErrorResponse(statusCode int, header http.Header, body []byte) error {
	var retryAfter time.Duration
	if statusCode == 429 {
		retryAfter = parseRetryAfter(header.Get("Retry-After"))
	}

	// Try to parse as structured API error.
	var structured APIError
	if err := json.Unmarshal(body, &structured); err == nil && structured.Message != "" {
		structured.StatusCode = statusCode
		structured.RetryAfter = retryAfter
		return &structured
	}

	apiErr := &APIError{StatusCode: statusCode, RetryAfter: retryAfter}

	// Try to parse as simple error message.
	var simpleErr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &simpleErr); err == nil {
		if simpleErr.Error != "" {
			apiErr.Message = simpleErr.Error
		} else if simpleErr.Message != "" {
			apiErr.Message = simpleErr.Message
		}
	}
	if apiErr.Message == "" {
		switch statusCode {
		case 401:
			apiErr.Message = "invalid or expired API key"
		case 403:
			apiErr.Message = "insufficient permissions"
		case 404:
			apiErr.Message = "resource not found"
		case 429:
			apiErr.Message = "rate limit exceeded"
		case 500, 502, 503, 504:
			apiErr.Message = "server error"
		default:
			apiErr.Message = strings.TrimSpace(string(body))
		}
	}
	return apiErr
}

// isRetryableError determines if an error should trigger a retry. API errors
// retry only on 5xx; validation errors never retry; transport-level failures
// (no HTTP response) are considered transient.
func (c *Client) isRetryableError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.IsServerError()
	}
	var valErr *ValidationError
	if errors.As(err, &valErr) {
		return false
	}
	return true
}

// isRetryableHTTPStatus determines if an HTTP status code should trigger a
// retry for the given method. See makeRequest for the POST rationale.
func (c *Client) isRetryableHTTPStatus(method string, statusCode int) bool {
	switch statusCode {
	case 429:
		return true
	case 500, 502, 503, 504:
		return method != http.MethodPost
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
