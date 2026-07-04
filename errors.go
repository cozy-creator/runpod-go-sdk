package runpod

import (
	"errors"
	"fmt"
)

// ErrNoCapacity is a sentinel matched (via errors.Is) by capacity-related
// pod-create failures. RunPod signals stock-outs as HTTP 500 with a
// "no instances available" style message; the SDK classifies those into
// *NoCapacityError which matches this sentinel.
var ErrNoCapacity = errors.New("runpod: no capacity")

// NoCapacityError is returned when RunPod has no stock for the requested
// GPU type / datacenter combination. errors.Is(err, ErrNoCapacity) is true.
type NoCapacityError struct {
	GPUTypeID     string
	DataCenterIDs []string
	Cause         error
}

func (e *NoCapacityError) Error() string {
	msg := "no capacity"
	if e.GPUTypeID != "" {
		msg += fmt.Sprintf(" for GPU type %q", e.GPUTypeID)
	}
	if len(e.DataCenterIDs) > 0 {
		msg += fmt.Sprintf(" in datacenters %v", e.DataCenterIDs)
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return "runpod: " + msg
}

func (e *NoCapacityError) Unwrap() error { return e.Cause }

func (e *NoCapacityError) Is(target error) bool { return target == ErrNoCapacity }

// FallbackAttempt records one failed candidate during a pod-create fan-out.
type FallbackAttempt struct {
	GPUTypeID string
	Err       error
}

// FallbackExhaustedError is returned by CreatePodWithFallback when every
// candidate GPU type failed. errors.Is sees through to the per-attempt
// errors, so errors.Is(err, ErrNoCapacity) is true when any attempt was a
// capacity failure.
type FallbackExhaustedError struct {
	Attempts []FallbackAttempt
}

func (e *FallbackExhaustedError) Error() string {
	tried := make([]string, len(e.Attempts))
	for i, a := range e.Attempts {
		tried[i] = a.GPUTypeID
	}
	last := "no attempts made"
	if n := len(e.Attempts); n > 0 && e.Attempts[n-1].Err != nil {
		last = e.Attempts[n-1].Err.Error()
	}
	return fmt.Sprintf("runpod: all %d candidate GPU types failed %v; last error: %s", len(e.Attempts), tried, last)
}

// Unwrap exposes the per-attempt errors to errors.Is / errors.As.
func (e *FallbackExhaustedError) Unwrap() []error {
	out := make([]error, 0, len(e.Attempts))
	for _, a := range e.Attempts {
		if a.Err != nil {
			out = append(out, a.Err)
		}
	}
	return out
}

type APIError struct {
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
	Details    string `json:"details,omitempty"`
	Code       string `json:"code,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("RunPod API Error %d (%s): %s - %s", e.StatusCode, e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("RunPod API Error %d: %s", e.StatusCode, e.Message)
}

func (e *APIError) IsNotFound() bool {
	return e.StatusCode == 404
}

func (e *APIError) IsBadRequest() bool {
	return e.StatusCode == 400
}

func (e *APIError) IsUnauthorized() bool {
	return e.StatusCode == 401
}

func (e *APIError) IsForbidden() bool {
	return e.StatusCode == 403
}

func (e *APIError) IsRateLimited() bool {
	return e.StatusCode == 429
}

func (e *APIError) IsServerError() bool {
	return e.StatusCode >= 500 && e.StatusCode < 600
}

func (e *APIError) IsClientError() bool {
	return e.StatusCode >= 400 && e.StatusCode < 500
}

type ValidationError struct {
	Field   string      `json:"field"`
	Message string      `json:"message"`
	Value   interface{} `json:"value,omitempty"`
}

func (e *ValidationError) Error() string {
	if e.Value != nil {
		return fmt.Sprintf("validation error for field '%s': %s (value: %v)", e.Field, e.Message, e.Value)
	}
	return fmt.Sprintf("validation error for field '%s': %s", e.Field, e.Message)
}

type ValidationErrors []ValidationError

func (ve ValidationErrors) Error() string {
	if len(ve) == 1 {
		return ve[0].Error()
	}
	return fmt.Sprintf("multiple validation errors: %d errors", len(ve))
}

type NetworkError struct {
	Message string
	Cause   error
}

// Error implements the error interface
func (e *NetworkError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("network error: %s (caused by: %v)", e.Message, e.Cause)
	}
	return fmt.Sprintf("network error: %s", e.Message)
}

// Unwrap implements the unwrapper interface for error chains
func (e *NetworkError) Unwrap() error {
	return e.Cause
}

type TimeoutError struct {
	Operation string
	Duration  string
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("timeout error: %s operation timed out after %s", e.Operation, e.Duration)
}

// AuthError represents an authentication error
type AuthError struct {
	Message string
}

// Error implements the error interface
func (e *AuthError) Error() string {
	return fmt.Sprintf("authentication error: %s", e.Message)
}

// RateLimitError represents a rate limiting error
type RateLimitError struct {
	Message    string
	RetryAfter string
	Limit      int
	Remaining  int
	ResetTime  string
}

// CapabilityNotAvailableError indicates the provider/API does not expose a requested feature.
type CapabilityNotAvailableError struct {
	Feature string
	Reason  string
}

// Error implements the error interface.
func (e *CapabilityNotAvailableError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("capability not available: %s", e.Feature)
	}
	return fmt.Sprintf("capability not available: %s (%s)", e.Feature, e.Reason)
}

// Error implements the error interface
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limit exceeded: %s (retry after: %s)", e.Message, e.RetryAfter)
}

// NewAPIError creates a new API error
func NewAPIError(statusCode int, message string) *APIError {
	return &APIError{
		StatusCode: statusCode,
		Message:    message,
	}
}

func NewAPIErrorWithDetails(statusCode int, message, details string) *APIError {
	return &APIError{
		StatusCode: statusCode,
		Message:    message,
		Details:    details,
	}
}

func NewValidationError(field, message string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: message,
	}
}

func NewValidationErrorWithValue(field, message string, value interface{}) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: message,
		Value:   value,
	}
}

func NewNetworkError(message string, cause error) *NetworkError {
	return &NetworkError{
		Message: message,
		Cause:   cause,
	}
}

func NewTimeoutError(operation, duration string) *TimeoutError {
	return &TimeoutError{
		Operation: operation,
		Duration:  duration,
	}
}

// NewAuthError creates a new authentication error
func NewAuthError(message string) *AuthError {
	return &AuthError{
		Message: message,
	}
}

func NewRateLimitError(message, retryAfter string) *RateLimitError {
	return &RateLimitError{
		Message:    message,
		RetryAfter: retryAfter,
	}
}

func NewCapabilityNotAvailableError(feature, reason string) *CapabilityNotAvailableError {
	return &CapabilityNotAvailableError{
		Feature: feature,
		Reason:  reason,
	}
}

// ================================
// ERROR CHECKING HELPERS
// ================================

// IsAPIError checks if an error is an APIError
func IsAPIError(err error) bool {
	var target *APIError
	return errors.As(err, &target)
}

// IsValidationError checks if an error is a ValidationError
func IsValidationError(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

// IsNetworkError checks if an error is a NetworkError
func IsNetworkError(err error) bool {
	var target *NetworkError
	return errors.As(err, &target)
}

// IsTimeoutError checks if an error is a TimeoutError
func IsTimeoutError(err error) bool {
	var target *TimeoutError
	return errors.As(err, &target)
}

// IsAuthError checks if an error is an AuthError
func IsAuthError(err error) bool {
	var target *AuthError
	return errors.As(err, &target)
}

// IsRateLimitError checks if an error is a RateLimitError
func IsRateLimitError(err error) bool {
	var target *RateLimitError
	return errors.As(err, &target)
}

// IsCapabilityNotAvailable checks if an error is a CapabilityNotAvailableError.
func IsCapabilityNotAvailable(err error) bool {
	var target *CapabilityNotAvailableError
	return errors.As(err, &target)
}
