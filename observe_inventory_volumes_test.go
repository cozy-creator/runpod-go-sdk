package runpod_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestObserveInventoryVolumeFailuresRemainSeparateFromMarket(t *testing.T) {
	for _, tc := range []struct {
		name, account string
		wantError     bool
		providerPath  []interface{}
	}{
		{name: "missing account", account: "", wantError: true},
		{name: "null account", account: `null`, wantError: true},
		{name: "missing list", account: `{}`, wantError: true},
		{name: "null list", account: `{"networkVolumes":null}`, wantError: true},
		{name: "empty list", account: `{"networkVolumes":[]}`},
		{name: "null row", account: `{"networkVolumes":[null]}`, wantError: true},
		{name: "missing row field", account: `{"networkVolumes":[{"id":"v","name":"cache","size":500}]}`, wantError: true},
		{name: "null row field", account: `{"networkVolumes":[{"id":"v","name":"cache","size":null,"dataCenterId":"DC-1"}]}`, wantError: true},
		{name: "size bool", account: `{"networkVolumes":[{"id":"v","name":"cache","size":false,"dataCenterId":"DC-1"}]}`, wantError: true},
		{name: "size fractional", account: `{"networkVolumes":[{"id":"v","name":"cache","size":1.5,"dataCenterId":"DC-1"}]}`, wantError: true},
		{name: "wrong list shape", account: `{"networkVolumes":{}}`, wantError: true},
		{name: "account error", account: `null`, wantError: true, providerPath: []interface{}{"myself"}},
		{name: "field error", account: `{"networkVolumes":[]}`, wantError: true, providerPath: []interface{}{"myself", "networkVolumes"}},
		{name: "nested row error", account: `{"networkVolumes":[]}`, wantError: true, providerPath: []interface{}{"myself", "networkVolumes", 0, "size"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				rec := httptest.NewRecorder()
				inventoryResponseFixture(t, rec, r, "")
				var response struct {
					Data   map[string]json.RawMessage
					Errors []map[string]interface{}
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Error(err)
					return
				}
				_, asked := response.Data["myself"]
				if tc.account == "" {
					delete(response.Data, "myself")
				} else if asked {
					response.Data["myself"] = json.RawMessage(tc.account)
				}
				if tc.providerPath != nil && asked {
					response.Errors = []map[string]interface{}{{"message": "volume read unavailable", "path": tc.providerPath, "extensions": map[string]string{"code": "CACHE_READ"}}}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
			request := inventoryRequestFixture()
			request.NetworkVolumes = true
			result, err := client.ObserveInventory(t.Context(), request)
			if err != nil {
				t.Fatalf("cache error erased valid marketroots: %v", err)
			}
			if (result.NetworkVolumesErr != nil) != tc.wantError {
				t.Fatalf("snapshot state error=%v volumes=%+v", result.NetworkVolumesErr, result.NetworkVolumes)
			}
			if tc.wantError && result.NetworkVolumes != nil {
				t.Fatalf("unknown snapshot became known list: %+v", result.NetworkVolumes)
			}
			if !tc.wantError && result.NetworkVolumes == nil {
				t.Fatal("valid empty snapshot did not retain known absence")
			}
			if tc.providerPath != nil {
				var providerError *runpod.GraphQLResponseError
				if !errors.As(result.NetworkVolumesErr, &providerError) || string(providerError.Errors[0].Extensions["code"]) != `"CACHE_READ"` {
					t.Fatalf("lost provider cache error: %v", result.NetworkVolumesErr)
				}
			}
			if len(result.DataCenters) != 5 || result.DataCenters[0].Err != nil || len(result.GPUOffers) != 3 || result.GPUOffers[0].Err != nil || len(result.CPUOffers) != 104 || result.CPUOffers[0].Err != nil {
				t.Fatal("volume failure affected market results")
			}
			if hits.Load() != 4 {
				t.Fatalf("market+volume snapshot sent%d HTTP requests", hits.Load())
			}
		})
	}
}

func TestObserveInventoryVolumeOnlyIsOneValidQuery(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Query != "query{myself{networkVolumes{id name size dataCenterId}}}" {
			t.Errorf("volume-only query: %s", request.Query)
		}
		_, _ = w.Write([]byte(`{"data":{"myself":{"networkVolumes":[]}}}`))
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	result, err := client.ObserveInventory(t.Context(), runpod.InventoryRequest{NetworkVolumes: true})
	if err != nil || result.NetworkVolumesErr != nil || result.NetworkVolumes == nil || len(result.NetworkVolumes) != 0 || hits.Load() != 1 {
		t.Fatalf("volume-only snapshot: %+v %v hits%d", result, err, hits.Load())
	}
}

func TestObserveInventoryVolumeTransport429StaysGlobal(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "6000")
		http.Error(w, "limited", 429)
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	for range 2 {
		result, err := client.ObserveInventory(t.Context(), runpod.InventoryRequest{NetworkVolumes: true})
		if !errors.Is(err, runpod.ErrRateLimited) || result.NetworkVolumesErr != nil {
			t.Fatalf("global429 became cache-only error: %+v %v", result, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("volume-only429 bypassed shared cooldown: %d", hits.Load())
	}
}

func TestObserveInventoryDoesNotRequestOptionalVolumeRoot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&request)
		if strings.Contains(request.Query, "myself") || strings.Contains(request.Query, "networkVolumes") {
			t.Error("requested optional volume data without opt-in")
		}
		_, _ = w.Write([]byte(`{"data":{"dataCenters":[]}}`))
	}))
	defer server.Close()
	client := mustClient(t, "fixture", runpod.WithGraphQLBaseURL(server.URL))
	result, err := client.ObserveInventory(t.Context(), runpod.InventoryRequest{Availability: []runpod.GPUAvailabilityFilter{{GPUCount: 1}}})
	if err != nil || result.NetworkVolumesErr != nil || result.NetworkVolumes != nil {
		t.Fatalf("unexpected optional volume state: %+v %v", result, err)
	}
}
