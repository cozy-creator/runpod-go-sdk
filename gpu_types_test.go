package runpod_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

const catalogGPU = `{"id":"gpu/a","name":"GPU A","pool":null,"manufacturer":"NVIDIA","memory":24,"secure":true,"community":true,"price":{"secure":0.31,"community":0.23},"maxCount":{"secure":8,"community":4},"availability":"HIGH","dataCenters":[{"id":"dc1","name":"DC 1","availability":"LOW"}]}`
const catalogCPU = `{"id":"cpu5c","name":"Compute","group":"Gen 5","vcpu":{"min":2,"max":32},"ramGbPerVcpu":2.5,"price":{"securePerVcpu":0.011234,"serverlessPerVcpu":0.01},"availability":"HIGH","dataCenters":[{"id":"dc1","name":"DC 1","availability":"MEDIUM"}]}`
const catalogDCs = `{"dataCenters":[{"id":"dc1","name":"DC 1","region":"EUROPE","networkVolumeTypes":["STANDARD"],"compliance":[],"globalNetwork":false}]}`

func newCatalogClient(t *testing.T, handler http.HandlerFunc) *runpod.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method=%s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test_key" {
			t.Errorf("missing bearer token")
		}
		if r.URL.Query().Has("api_key") {
			t.Errorf("credential leaked in query")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return mustClient(t, "test_key", runpod.WithBaseURL(server.URL+"/v2"))
}
func writeGPUs(w http.ResponseWriter, row string) { fmt.Fprintf(w, `{"gpus":[%s]}`, row) }
func TestListGPUTypesUsesScopedV2Catalog(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/catalog/gpus" {
			t.Errorf("path=%s", r.URL.Path)
		}
		q := r.URL.Query()
		for k, want := range map[string]string{"include": "AVAILABILITY", "product": "POD", "cloud": "COMMUNITY", "count": "4", "minCudaVersion": "12.8", "countryCodes": "FR,DE"} {
			if q.Get(k) != want {
				t.Errorf("%s=%q want %q", k, q.Get(k), want)
			}
		}
		writeGPUs(w, catalogGPU)
	})
	gpus, err := c.ListGPUTypes(context.Background(), &runpod.GPUTypeFilter{IDs: []string{"gpu/a"}, CloudType: "COMMUNITY", GPUCount: 4, MinCudaVersion: "12.8", CountryCodes: []string{"FR", "DE"}})
	if err != nil || len(gpus) != 1 {
		t.Fatalf("gpus=%+v err=%v", gpus, err)
	}
	if gpus[0].Name != "GPU A" || gpus[0].Memory != 24 || gpus[0].Price.Community == nil || *gpus[0].Price.Community != 230000 {
		t.Fatalf("gpu=%+v", gpus[0])
	}
}
func TestGetGPUTypeUsesNativeSingleResource(t *testing.T) {
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v2/catalog/gpus/gpu%2Fa" {
			t.Errorf("escaped path=%s", r.URL.EscapedPath())
		}
		fmt.Fprint(w, catalogGPU)
	})
	gpu, err := c.GetGPUType(context.Background(), "gpu/a")
	if err != nil || gpu.ID != "gpu/a" {
		t.Fatalf("gpu=%+v err=%v", gpu, err)
	}
}
func TestGPUCatalogRefusesIncompleteAndDuplicateRows(t *testing.T) {
	for _, body := range []string{`{}`, `{"gpus":null}`, `[]`, `{"gpus":[{}]}`, `{"gpus":[` + catalogGPU + `,` + catalogGPU + `]}`,
		`{"gpus":[` + strings.Replace(catalogGPU, `"secure":true,`, "", 1) + `]}`, `{"gpus":[` + strings.Replace(catalogGPU, `"availability":"HIGH"`, `"availability":"mystery"`, 1) + `]}`} {
		t.Run(body, func(t *testing.T) {
			c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if _, err := c.ListGPUTypes(context.Background(), nil); err == nil {
				t.Fatal("accepted incomplete catalog")
			}
		})
	}
}
func TestGPUCatalogFiltersValidateBeforeHTTP(t *testing.T) {
	c := newCatalogClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("sent HTTP for invalid filter") })
	for _, f := range []runpod.GPUTypeFilter{{GPUCount: -1}, {CloudType: "other"}, {MinCudaVersion: "12"}, {MinCudaVersion: "12.8", AllowedCudaVersions: []string{"12.9"}}, {CountryCodes: []string{"fr"}}} {
		if _, err := c.ListGPUTypes(context.Background(), &f); err == nil {
			t.Fatalf("accepted %+v", f)
		}
	}
}

// Native v2 returns this sentinel alongside real SKUs. It must not poison the
// complete catalog or become a rentable offer merely because maxCount is set.
func TestGPUCatalogPreservesExplicitZeroMemorySentinel(t *testing.T) {
	sentinel := `{"id":"unknown","name":"unknown","pool":null,"manufacturer":"NVIDIA","memory":0,"secure":false,"community":false,"price":{"secure":0,"community":0},"maxCount":{"secure":8,"community":8},"availability":"NONE"}`
	c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"gpus":[%s,%s]}`, sentinel, catalogGPU) })
	rows, err := c.ListGPUTypes(t.Context(), nil)
	if err != nil || len(rows) != 2 || rows[0].ID != "unknown" || rows[0].Memory != 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	offers, err := c.ListGPUOffers(t.Context(), &runpod.GPUOfferFilter{CloudType: "SECURE", InStockOnly: true})
	if err != nil || len(offers) != 1 || offers[0].GPUTypeID != "gpu/a" {
		t.Fatalf("offers=%+v err=%v", offers, err)
	}
}

func TestGPUCatalogRefusesMissingNullAndNegativeMemory(t *testing.T) {
	for _, row := range []string{
		strings.Replace(catalogGPU, `"memory":24,`, "", 1),
		strings.Replace(catalogGPU, `"memory":24`, `"memory":null`, 1),
		strings.Replace(catalogGPU, `"memory":24`, `"memory":-1`, 1),
	} {
		c := newCatalogClient(t, func(w http.ResponseWriter, r *http.Request) { writeGPUs(w, row) })
		if _, err := c.ListGPUTypes(t.Context(), nil); err == nil {
			t.Fatal("accepted missing, null, or negative memory metadata")
		}
	}
}
