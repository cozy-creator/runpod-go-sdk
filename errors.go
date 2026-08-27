package runpod

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors, matched via errors.Is against errors returned by any SDK
// method.
var (
	// ErrNotFound matches 404 responses.
	ErrNotFound = errors.New("runpod: not found")
	// ErrUnauthorized matches 401/403 responses (bad API key / permissions).
	ErrUnauthorized = errors.New("runpod: unauthorized")
	// ErrRateLimited matches 429 responses.
	ErrRateLimited = errors.New("runpod: rate limited")
	// ErrNoCapacity matches capacity-related pod-create failures. RunPod
	// signals stock-outs as HTTP 500 with a "no instances available" style
	// message; the SDK classifies those into *NoCapacityError.
	ErrNoCapacity = errors.New("runpod: no capacity")
)

// APIError is an error response from the RunPod API. It matches ErrNotFound,
// ErrUnauthorized and ErrRateLimited via errors.Is according to StatusCode.
type APIError struct {
	StatusCode int    `json:"statusCode"`
	Message    string `json:"message"`
	Details    string `json:"details,omitempty"`
	Code       string `json:"code,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
	// ResponseBody preserves the provider's exact error bytes. RunPod has
	// multiple structured and prose error shapes; known fields above remain
	// convenient while this prevents unknown diagnostics from being dropped.
	ResponseBody json.RawMessage `json:"-"`

	// RetryAfter is populated from the Retry-After header on 429 responses.
	RetryAfter time.Duration `json:"-"`
}

func (e *APIError) Error() string {
	if e.Code != "" {
		if e.Details != "" {
			return fmt.Sprintf("RunPod API Error %d (%s): %s - %s", e.StatusCode, e.Code, e.Message, e.Details)
		}
		return fmt.Sprintf("RunPod API Error %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	if e.Details != "" {
		return fmt.Sprintf("RunPod API Error %d: %s - %s", e.StatusCode, e.Message, e.Details)
	}
	return fmt.Sprintf("RunPod API Error %d: %s", e.StatusCode, e.Message)
}

// Is maps status codes onto the package sentinels for errors.Is.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == 404
	case ErrUnauthorized:
		return e.StatusCode == 401 || e.StatusCode == 403
	case ErrRateLimited:
		return e.StatusCode == 429
	}
	return false
}

// IsServerError reports whether the response was a 5xx.
func (e *APIError) IsServerError() bool {
	return e.StatusCode >= 500 && e.StatusCode < 600
}

// NewAPIError creates a new API error.
func NewAPIError(statusCode int, message string) *APIError {
	return &APIError{
		StatusCode: statusCode,
		Message:    message,
	}
}

// NewAPIErrorWithDetails creates an API error with a details string.
func NewAPIErrorWithDetails(statusCode int, message, details string) *APIError {
	return &APIError{
		StatusCode: statusCode,
		Message:    message,
		Details:    details,
	}
}

// ValidationError is a client-side input validation failure; no request was
// sent to the API.
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

// NewValidationError creates a client-side validation error.
func NewValidationError(field, message string) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: message,
	}
}

// NewValidationErrorWithValue creates a validation error carrying the
// offending value.
func NewValidationErrorWithValue(field, message string, value interface{}) *ValidationError {
	return &ValidationError{
		Field:   field,
		Message: message,
		Value:   value,
	}
}

// NoCapacityError is returned when RunPod has no stock for the requested
// GPU type / datacenter combination. errors.Is(err, ErrNoCapacity) is true.
type NoCapacityError struct {
	GPUTypeID     string
	DataCenterIDs []string
	// NoMatchingHost reports that RunPod answered "no host matches this
	// request" rather than "this SKU is out of stock" (rp#17). Both mean no
	// pod was created; only this one may be a statement about the REQUEST's
	// shape instead of about the market, because the shape terms (gpuCount,
	// minRAMPerGPU, minVCPUPerGPU, containerDiskInGB, dataCenterIds) are the
	// same on every candidate of a fan-out. One occurrence proves nothing;
	// see FallbackExhaustedError.AllNoMatchingHost for the signal that does.
	NoMatchingHost bool
	Cause          error
}

func (e *NoCapacityError) Error() string {
	msg := "no capacity"
	if e.NoMatchingHost {
		msg = "no host matched the requested pod shape"
	}
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

// AllNoMatchingHost reports that EVERY attempt answered "no host matched the
// requested pod shape" (rp#17).
//
// This is the signal a single attempt cannot give. The shape terms of a
// fan-out — gpuCount, minRAMPerGPU, minVCPUPerGPU, containerDiskInGB,
// dataCenterIds — are identical on every candidate; only the GPU type varies.
// So N different GPU types all answering "nothing matches" is far better
// explained by ONE over-constrained request than by N simultaneous stock-outs,
// and the caller should surface the request's shape rather than report the
// market as sold out. False for an empty attempt list: no evidence is not
// evidence.
func (e *FallbackExhaustedError) AllNoMatchingHost() bool {
	if len(e.Attempts) == 0 {
		return false
	}
	for _, a := range e.Attempts {
		var noCap *NoCapacityError
		if !errors.As(a.Err, &noCap) || !noCap.NoMatchingHost {
			return false
		}
	}
	return true
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
	if e.AllNoMatchingHost() {
		// Loud on purpose: every candidate rejected the same shape, which is
		// the signature of a request we constructed wrong, not of a stock-out.
		return fmt.Sprintf("runpod: no host matched the requested pod shape on ANY of %d candidate GPU types %v"+
			" — the request is more likely over-constrained (gpuCount / minRAMPerGPU / minVCPUPerGPU /"+
			" containerDiskInGB / dataCenterIds) than the market sold out; last error: %s",
			len(e.Attempts), tried, last)
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
