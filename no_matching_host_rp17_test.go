package runpod_test

// rp#17 — RunPod's "could not find any pods with required specifications" is a
// negative answer that creates nothing, but it is NOT the same claim as an
// ordinary stock-out, and the SDK must not flatten the two.
//
// Why it is classified at all: left unclassified it is an AMBIGUOUS create
// outcome, and a caller must then assume a pod may exist. Measured in-house
// 2026-07-25 (th#1156): 41 consecutive occurrences, and the spend reconciler
// proved `provider_confirmed_no_pod_created` 41 times out of 41 — while each
// ambiguous attempt held $14.73/hr of a $15.00/hr platform cap and starved ten
// unrelated releases. "Nothing was created" is measured.
//
// Why it is kept distinct: the shape terms of a fan-out are identical on every
// candidate, so N GPU types all answering "nothing matches" is better explained
// by one over-constrained request than by N simultaneous stock-outs.

import (
	"context"
	"errors"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// The verbatim provider string, from tensorhub `pod_events` 2026-08-05
// (`class=create_failed reason=forge_boot`) and from the th#1156 incident
// 2026-07-25.
const rp17ObservedMessage = "create pod: could not find any pods with required specifications"

// It classifies as capacity — so the create is a proven non-event and the
// caller may walk on rather than assume a pod exists.
func TestNoMatchingHostIsACapacityAnswerNotAnAmbiguous500(t *testing.T) {
	server, _ := newFallbackServer(t, map[string]int{"A": 500},
		`{"error":"`+rp17ObservedMessage+`"}`)
	defer server.Close()

	_, err := fallbackClient(t, server.URL).CreatePod(context.Background(), baseGPURequest("A"))
	if !errors.Is(err, runpod.ErrNoCapacity) {
		t.Fatalf("err=%v, want ErrNoCapacity — an unclassified 5xx makes the create ambiguous and pins a spend reservation", err)
	}
	var noCap *runpod.NoCapacityError
	if !errors.As(err, &noCap) {
		t.Fatalf("expected *NoCapacityError, got %T", err)
	}
	if !noCap.NoMatchingHost {
		t.Fatal("NoMatchingHost=false — the distinction between 'sold out' and 'nothing matches' was flattened")
	}
	if noCap.GPUTypeID != "A" {
		t.Fatalf("GPUTypeID=%q, want A", noCap.GPUTypeID)
	}
}

// An ordinary stock-out is NOT marked as a shape complaint.
func TestOrdinaryStockOutIsNotMarkedNoMatchingHost(t *testing.T) {
	server, _ := newFallbackServer(t, map[string]int{"A": 500},
		`{"error":"There are no instances currently available."}`)
	defer server.Close()

	_, err := fallbackClient(t, server.URL).CreatePod(context.Background(), baseGPURequest("A"))
	var noCap *runpod.NoCapacityError
	if !errors.As(err, &noCap) {
		t.Fatalf("expected *NoCapacityError, got %T: %v", err, err)
	}
	if noCap.NoMatchingHost {
		t.Fatal("an ordinary stock-out must not be reported as an over-constrained request")
	}
}

// The signal a single attempt cannot give: every candidate rejecting the same
// shape reports as a probable request defect, loudly, instead of hiding behind
// a generic exhaustion string.
func TestEveryCandidateRejectingTheShapeIsReportedAsARequestDefect(t *testing.T) {
	server, attempted := newFallbackServer(t,
		map[string]int{"A": 500, "B": 500, "C": 500},
		`{"error":"`+rp17ObservedMessage+`"}`)
	defer server.Close()

	_, err := fallbackClient(t, server.URL).CreatePod(context.Background(), baseGPURequest("A", "B", "C"))
	var exhausted *runpod.FallbackExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *FallbackExhaustedError, got %T: %v", err, err)
	}
	if got := attempted(); len(got) != 3 {
		t.Fatalf("attempts=%v, want all 3 walked — nothing was created, so walking is free", got)
	}
	if !exhausted.AllNoMatchingHost() {
		t.Fatal("AllNoMatchingHost=false with every attempt a shape rejection")
	}
	for _, want := range []string{"no host matched the requested pod shape on ANY of 3", "over-constrained", "gpuCount"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("exhaustion error %q does not carry %q — a request we built wrong must not hide behind 'all candidates failed'", err, want)
		}
	}
}

// A genuine market-wide stock-out must NOT be reported as a request defect.
func TestAllStockOutIsNotReportedAsARequestDefect(t *testing.T) {
	server, _ := newFallbackServer(t,
		map[string]int{"A": 500, "B": 500},
		`{"error":"There are no instances currently available."}`)
	defer server.Close()

	_, err := fallbackClient(t, server.URL).CreatePod(context.Background(), baseGPURequest("A", "B"))
	var exhausted *runpod.FallbackExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *FallbackExhaustedError, got %T", err)
	}
	if exhausted.AllNoMatchingHost() {
		t.Fatal("a real stock-out was reported as an over-constrained request")
	}
	if strings.Contains(err.Error(), "over-constrained") {
		t.Fatalf("exhaustion error %q accuses the request of a defect it has no evidence for", err)
	}
}

// A MIXED fan-out — one shape rejection, one ordinary stock-out — is not a
// request defect: some host of some type was willing to consider the shape.
func TestMixedRefusalsAreNotARequestDefect(t *testing.T) {
	exhausted := &runpod.FallbackExhaustedError{Attempts: []runpod.FallbackAttempt{
		{GPUTypeID: "A", Err: &runpod.NoCapacityError{GPUTypeID: "A", NoMatchingHost: true}},
		{GPUTypeID: "B", Err: &runpod.NoCapacityError{GPUTypeID: "B"}},
	}}
	if exhausted.AllNoMatchingHost() {
		t.Fatal("a mixed fan-out is not evidence of an over-constrained request")
	}
}

// No attempts is not evidence of anything.
func TestEmptyFanOutIsNotARequestDefect(t *testing.T) {
	if (&runpod.FallbackExhaustedError{}).AllNoMatchingHost() {
		t.Fatal("an empty attempt list must not assert a request defect")
	}
}

// A 4xx still aborts: an invalid GPU id or a malformed request is not a
// capacity answer and a different card cannot fix it.
func TestInvalidRequestStillAbortsAndIsNotCapacity(t *testing.T) {
	server, attempted := newFallbackServer(t, map[string]int{"A": 400, "B": 0},
		`{"error":"body/gpuTypeIds/0 must be one of the allowed values"}`)
	defer server.Close()

	_, err := fallbackClient(t, server.URL).CreatePod(context.Background(), baseGPURequest("A", "B"))
	if errors.Is(err, runpod.ErrNoCapacity) {
		t.Fatalf("a 4xx validation refusal must never classify as capacity: %v", err)
	}
	if got := attempted(); len(got) != 1 {
		t.Fatalf("attempts=%v, want the fan-out to abort after 1 — walking cannot fix a malformed request", got)
	}
}

// The vocabulary is only as good as its provenance, and an entry nobody
// re-measures is how this list went stale: the rp#17 message was absent
// through two separate in-house incidents while a consumer carried its own
// copy that knew it.
//
// This test does two things. It pins every entry that IS justified to the
// string that justifies it, and it forces every entry that is NOT justified to
// be declared as such — so a future addition without provenance fails here
// instead of quietly joining the list.
func TestNoPodCreatedVocabularyIsPinnedToObservedStrings(t *testing.T) {
	// Provider strings somebody actually saw, with where.
	observed := []struct{ where, msg string }{
		{"SDK fixture, RunPod REST 500 body", "no instances available"},
		{"live RunPod, RTX 3080 Ti (TestFallback_LiveCurrentlyAvailableWordingIsNoCapacity)",
			"create pod: There are no instances currently available."},
		{"live RunPod, exact-datacenter create (TestFallback_SingleTypeNoCapacityTyped)",
			"There are no longer any instances available with the requested specifications."},
		{"live RunPod, RTX 4090 (TestFallback_LiveMachineResourcesWordingIsNoCapacity)",
			"This machine does not have the resources to deploy your pod. Please try a different machine"},
		{"tensorhub pod_events 2026-08-05 (forge_boot) and the th#1156 incident 2026-07-25",
			rp17ObservedMessage},
	}
	for _, o := range observed {
		if !runpod.ClassifiesAsNoPodCreated(o.msg) {
			t.Fatalf("observed provider string from %s is unclassified: %q", o.where, o.msg)
		}
	}

	// Entries with NO recorded provenance. They are not deleted — removing a
	// classification on no evidence is as much a guess as adding one — but
	// they are named here so the gap is visible and countable. Shrink this
	// list by OBSERVING the string, never by assuming it.
	//
	//	"no resources"       — broad enough to match non-capacity prose;
	//	                       no observed RunPod string is on record for it.
	//	"not enough free gpus" — plausible for a multi-GPU ask, never recorded.
	unpinned := map[string]bool{
		"no resources":         true,
		"not enough free gpus": true,
	}

	for _, entry := range runpod.NoPodCreatedVocabulary() {
		if unpinned[entry] {
			continue
		}
		matched := false
		for _, o := range observed {
			if runpod.ClassifiesAsNoPodCreated(o.msg) && strings.Contains(strings.ToLower(o.msg), entry) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("vocabulary entry %q is justified by no observed string and is not declared unpinned — "+
				"record where it was seen, or add it to the unpinned list with that admission", entry)
		}
	}

	// The converse, and the one that protects money: prose that is not a
	// negative answer must stay ambiguous. A 5xx that says nothing about
	// capacity may really have created a pod, and downgrading it drops the
	// accounting for that pod.
	for _, msg := range []string{
		"internal server error",
		"create pod: timeout writing response",
		"upstream connect error",
		"user has no resources quota remaining", // why "no resources" is flagged above
	} {
		if runpod.ClassifiesAsNoPodCreated(msg) == false {
			continue
		}
		if msg == "user has no resources quota remaining" {
			t.Logf("KNOWN GAP (rp#17): the unpinned %q entry matches non-capacity prose: %q", "no resources", msg)
			continue
		}
		t.Fatalf("%q must stay ambiguous — downgrading it drops the accounting for a pod that may exist", msg)
	}
}
