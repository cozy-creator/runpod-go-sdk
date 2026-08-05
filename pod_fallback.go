package runpod

import (
	"context"
	"errors"
	"strings"
)

// noCapacityMessages are the substrings RunPod uses (REST and GraphQL) to
// signal an ordinary stock-out — "this SKU has no free machine right now".
// RunPod reports these as HTTP 500, so status alone cannot distinguish "no
// stock" from a real server fault.
//
// TestNoPodCreatedVocabularyIsPinnedToObservedStrings audits this list: each
// entry is either pinned to a provider string somebody actually observed, or
// declared unpinned there. Two entries are currently unpinned. An unmeasured
// entry is how the vocabulary goes stale (rp#17).
var noCapacityMessages = []string{
	"no instances available",
	"no instances currently available",
	"no longer any instances available",
	// NOTE: "no resources available" was deleted as strictly dead — the
	// shorter "no resources" below already contains it, so it could never
	// match anything the shorter entry did not. See rp#17 for the one entry
	// in this list that is still unpinned ("no resources"): it is broad
	// enough to match non-capacity prose and no observed string justifies
	// it, but narrowing it without evidence would be a guess in the other
	// direction.
	"no resources",
	"does not have the resources to deploy your pod",
	"not enough free gpus",
}

// noMatchingHostMessages is RunPod's OTHER negative answer: "I looked and no
// host matches this request." It is deliberately NOT in noCapacityMessages,
// because the two claims are not the same claim (rp#17).
//
// What the two have in COMMON, and why both may walk to the next candidate:
// neither creates a pod. Measured in-house on 2026-07-25 (th#1156) across 41
// consecutive occurrences of the message below, the spend reconciler proved
// `provider_confirmed_no_pod_created` 41 times out of 41. Walking is free.
//
// Where they DIFFER, and why the distinction is kept: a stock-out is a fact
// about ONE candidate, while "no host matches" may instead be a fact about the
// REQUEST — gpuCount, minRAMPerGPU, minVCPUPerGPU, containerDiskInGB and
// dataCenterIds are identical on every candidate of a fan-out. So a fan-out in
// which EVERY candidate answered this did not observe a market-wide stock-out;
// it more likely observed one over-constrained request, N times. The SDK
// cannot tell which from a single response — RunPod uses one wording for both
// — so it does not guess: it records which wording it saw and lets
// FallbackExhaustedError.AllNoMatchingHost say so loudly at exhaustion.
//
// It is classified as capacity rather than left unclassified because the
// alternative is strictly worse: an unclassified 5xx is an AMBIGUOUS create
// outcome, and a caller must then assume a pod may exist. On 2026-07-25 that
// assumption held $14.73/hr of a $15.00/hr platform cap 41 times over and
// starved ten unrelated releases. "Nothing was created" is the measured fact;
// "which of the two negatives it was" is the open one.
var noMatchingHostMessages = []string{
	"could not find any pods with required specifications",
}

func matchesAny(msg string, vocabulary []string) bool {
	msg = strings.ToLower(msg)
	for _, m := range vocabulary {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

func isNoCapacityMessage(msg string) bool { return matchesAny(msg, noCapacityMessages) }

func isNoMatchingHostMessage(msg string) bool { return matchesAny(msg, noMatchingHostMessages) }

// ClassifiesAsNoPodCreated reports whether a provider message is one of
// RunPod's negative answers — an ordinary stock-out or a "no host matches this
// request" — as opposed to a 5xx that says nothing and may therefore have
// created a pod.
//
// Exported so consumers can pin this vocabulary against provider strings they
// observe in production rather than re-implementing it. A second, disagreeing
// copy of these substrings in a consumer is exactly the drift this replaces:
// on 2026-08-05 tensorhub carried its own list that knew a message this one
// did not, and the two answered differently at two layers of the same create.
func ClassifiesAsNoPodCreated(providerMessage string) bool {
	return isNoCapacityMessage(providerMessage) || isNoMatchingHostMessage(providerMessage)
}

// NoPodCreatedVocabulary returns every substring this package treats as a
// provider answer meaning no pod was created. Exported so the vocabulary can
// be audited — a consumer or a test can enumerate it and ask which entries are
// still justified by a string somebody actually observed.
func NoPodCreatedVocabulary() []string {
	out := make([]string, 0, len(noCapacityMessages)+len(noMatchingHostMessages))
	out = append(out, noCapacityMessages...)
	return append(out, noMatchingHostMessages...)
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
	detail := apiErr.Message + " " + apiErr.Details
	noMatchingHost := isNoMatchingHostMessage(detail)
	if !noMatchingHost && !isNoCapacityMessage(detail) {
		return err
	}
	gpuTypeID := ""
	if len(req.GPUTypeIDs) == 1 {
		gpuTypeID = req.GPUTypeIDs[0]
	}
	return &NoCapacityError{
		GPUTypeID:      gpuTypeID,
		DataCenterIDs:  req.DataCenterIDs,
		NoMatchingHost: noMatchingHost,
		Cause:          err,
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
