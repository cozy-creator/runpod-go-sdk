package runpod_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// RunPod answered 500 after about 6.5 s to the request carrying a whole pass (2026-10-20)
// while it served small quotes. Bounded requests read the pass; a request that still fails
// loses only its own quotes, and a rate limit stops the pass.
func TestObserveInventoryBoundsRequestsAndScopesTheirFailures(t *testing.T) {
	request := inventoryRequestFixture()
	request.GPUOffers = nil
	for i := range 20 {
		request.GPUOffers = append(request.GPUOffers, runpod.GPUOfferFilter{GPUCount: 1, CloudType: "SECURE", DataCenterID: fmt.Sprintf("DC-%d", i), IDs: []string{"one"}})
	}
	// Requests in order: census, prices 0-7, 8-15, 16-19, CPU 0-63, 64-103.
	for _, tc := range []struct {
		name     string
		refuse   func(hit int32, query string) int
		hits     int32
		failed   func(gpu int) bool
		cpuError error
	}{
		{"whole pass 500s", func(_ int32, query string) int {
			if strings.Count(query, ":lowestPrice(") > 8 {
				return http.StatusInternalServerError
			}
			return 0
		}, 6, func(int) bool { return false }, nil},
		{"one request 500s", func(hit int32, _ string) int {
			if hit == 3 {
				return http.StatusInternalServerError
			}
			return 0
		}, 6, func(i int) bool { return i >= 8 && i < 16 }, nil},
		{"rate limit stops the pass", func(hit int32, _ string) int {
			if hit == 3 {
				return http.StatusTooManyRequests
			}
			return 0
		}, 3, func(i int) bool { return i >= 8 }, runpod.ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				if status := tc.refuse(hits.Add(1), string(body)); status != 0 {
					w.Header().Set("Retry-After", "1")
					http.Error(w, "upstream timed out", status)
					return
				}
				inventoryResponseFixture(t, w, r, "")
			}))
			defer server.Close()
			client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
			result, err := client.ObserveInventory(t.Context(), request)
			if err != nil {
				t.Fatalf("a partial pass failed as a whole: %v", err)
			}
			if hits.Load() != tc.hits {
				t.Fatalf("sent %d requests, want %d", hits.Load(), tc.hits)
			}
			for i, census := range result.DataCenters {
				if census.Err != nil {
					t.Errorf("census %d: %v", i, census.Err)
				}
			}
			for i, quote := range result.GPUOffers {
				if (quote.Err != nil) != tc.failed(i) || quote.Err == nil && len(quote.Offers) != 1 {
					t.Errorf("price %d: %+v", i, quote)
				}
			}
			for i, quote := range result.CPUOffers {
				if !errors.Is(quote.Err, tc.cpuError) {
					t.Errorf("CPU %d: %v", i, quote.Err)
				}
			}
		})
	}
}
