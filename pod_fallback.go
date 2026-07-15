package runpod

import (
	"context"
	"errors"
	"strings"
)

// noCapacityMessages are the substrings RunPod uses (REST and GraphQL) to
// signal an ordinary stock-out. RunPod reports these as HTTP 500, so status
// alone cannot distinguish "no stock" from a real server fault.
var noCapacityMessages = []string{
	"no instances available",
	"no instances currently available",
	"no longer any instances available",
	"no resources available",
	"no resources",
	"does not have the resources to deploy your pod",
	"not enough free gpus",
}

func isNoCapacityMessage(msg string) bool {
	msg = strings.ToLower(msg)
	for _, m := range noCapacityMessages {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// classifyCreatePodError lifts RunPod's untyped 5xx stock-out responses into
// *NoCapacityError so callers can errors.Is(err, ErrNoCapacity).
func classifyCreatePodError(err error, req *CreatePodRequest) error {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	if !apiErr.IsServerError() {
		return err
	}
	if !isNoCapacityMessage(apiErr.Message + " " + apiErr.Details) {
		return err
	}
	gpuTypeID := ""
	if len(req.GPUTypeIDs) == 1 {
		gpuTypeID = req.GPUTypeIDs[0]
	}
	return &NoCapacityError{
		GPUTypeID:     gpuTypeID,
		DataCenterIDs: req.DataCenterIDs,
		Cause:         err,
	}
}

// CreatePodFallbackOptions tunes CreatePodWithFallback. All fields optional.
type CreatePodFallbackOptions struct {
	// CandidateFilter filters/reorders the candidate GPU type IDs before the
	// fan-out — e.g. to skip types that recently failed. Returning an empty
	// slice falls back to the unfiltered candidates.
	CandidateFilter func(candidates []string) []string

	// OnAttemptFailure is invoked after each failed attempt, e.g. to feed a
	// consumer-side failure tracker.
	OnAttemptFailure func(gpuTypeID string, err error)
}

// CreatePodWithFallback creates a pod by trying each candidate GPU type ID in
// order, one request per type. RunPod's REST `POST /pods` does NOT walk a
// multi-entry gpuTypeIds list when the first type has no stock — it returns
// 500 "no instances available" — so the SDK owns the per-type fan-out.
//
// Capacity failures (and other 5xx responses) move on to the next candidate;
// 4xx and transport failures abort immediately since retrying them on a
// different GPU type cannot help. When every candidate fails the returned
// error is *FallbackExhaustedError, which matches errors.Is(err,
// ErrNoCapacity) when any attempt was a stock-out.
//
// candidates may be nil, in which case req.GPUTypeIDs is used.
func (c *Client) CreatePodWithFallback(ctx context.Context, req *CreatePodRequest, candidates []string, opts *CreatePodFallbackOptions) (*Pod, error) {
	if req == nil {
		return nil, NewValidationError("request", "cannot be nil")
	}
	if len(candidates) == 0 {
		candidates = req.GPUTypeIDs
	}
	if len(candidates) == 0 {
		return nil, NewValidationError("candidates", "cannot be empty")
	}
	if opts != nil && opts.CandidateFilter != nil {
		if filtered := opts.CandidateFilter(candidates); len(filtered) > 0 {
			candidates = filtered
		}
	}

	attempts := make([]FallbackAttempt, 0, len(candidates))
	for _, gpuTypeID := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		attempt := *req
		attempt.GPUTypeIDs = []string{gpuTypeID}

		pod, err := c.createPod(ctx, &attempt)
		if err == nil {
			return pod, nil
		}
		if opts != nil && opts.OnAttemptFailure != nil {
			opts.OnAttemptFailure(gpuTypeID, err)
		}

		// Abort on errors a different GPU type cannot fix: validation,
		// auth, malformed requests, transport failures. Continue on 5xx
		// (capacity or otherwise) — RunPod's stock-out wording varies.
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.IsServerError() {
			return nil, err
		}
		attempts = append(attempts, FallbackAttempt{GPUTypeID: gpuTypeID, Err: err})
	}

	return nil, &FallbackExhaustedError{Attempts: attempts}
}
