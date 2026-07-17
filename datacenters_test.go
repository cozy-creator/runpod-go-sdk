package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestListDataCenters(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(req testGraphQLRequest) interface{} {
		if !strings.Contains(req.Query, "dataCenters") || !strings.Contains(req.Query, "gpuAvailability") {
			t.Fatalf("unexpected query: %s", req.Query)
		}
		input, ok := req.Variables["input"].(map[string]any)
		if !ok || input["gpuCount"] != float64(2) || input["secureCloud"] != true || input["minCudaVersion"] != "13.0" {
			t.Fatalf("availability input = %#v", req.Variables["input"])
		}
		return map[string]any{"data": map[string]any{"dataCenters": []map[string]any{
			{
				"id": "US-GA-1", "name": "US-GA-1", "location": "United States",
				"gpuAvailability": []map[string]any{
					{"gpuTypeId": "NVIDIA GeForce RTX 4090", "displayName": "RTX 4090", "stockStatus": "High", "available": true},
					{"gpuTypeId": "NVIDIA H100 80GB HBM3", "displayName": "H100 SXM", "stockStatus": "Low"},
				},
			},
			{
				"id": "EU-RO-1", "name": "EU-RO-1", "location": "Europe",
				"gpuAvailability": []map[string]any{
					{"gpuTypeId": "NVIDIA A100 80GB PCIe", "displayName": "A100 PCIe", "stockStatus": "Medium"},
				},
			},
		}}}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	secure := true
	got, err := client.ListDataCenters(t.Context(), &runpod.GPUAvailabilityFilter{
		GPUCount: 2, SecureCloud: &secure, MinCUDAVersion: "13.0",
	})
	if err != nil {
		t.Fatalf("ListDataCenters: %v", err)
	}
	if len(got) != 2 || got[0].ID != "US-GA-1" || got[1].ID != "EU-RO-1" {
		t.Fatalf("data centers = %#v", got)
	}
	if availability := got[0].GPUAvailability; len(availability) != 2 ||
		availability[0].GPUTypeID != "NVIDIA GeForce RTX 4090" || availability[0].StockStatus != "High" || !availability[0].Available {
		t.Fatalf("gpu availability = %#v", availability)
	}
}

func TestListDataCentersEmptyInventory(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(testGraphQLRequest) interface{} {
		return map[string]any{"data": map[string]any{"dataCenters": []any{}}}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	got, err := client.ListDataCenters(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListDataCenters: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("data centers = %#v, want empty", got)
	}
}

func TestListDataCentersGraphQLError(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(testGraphQLRequest) interface{} {
		return map[string]any{"errors": []map[string]any{{"message": "inventory unavailable"}}}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	_, err := client.ListDataCenters(t.Context(), nil)
	var apiErr *runpod.APIError
	if !errors.As(err, &apiErr) || apiErr.Details != "inventory unavailable" {
		t.Fatalf("error = %v", err)
	}
}

func TestListDataCentersMalformedPayload(t *testing.T) {
	server := newGPUTypeGraphQLServer(t, func(testGraphQLRequest) interface{} {
		return map[string]any{"data": map[string]any{"dataCenters": "not-an-array"}}
	})
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	_, err := client.ListDataCenters(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "unmarshal GraphQL data payload") {
		t.Fatalf("error = %v", err)
	}
}

func TestListDataCentersHonorsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.ListDataCenters(ctx, nil)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		close(release)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("ListDataCenters did not stop after context cancellation")
	}
}

func TestListDataCentersMalformedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode("not-an-envelope")
	}))
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
	_, err := client.ListDataCenters(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "unmarshal GraphQL response envelope") {
		t.Fatalf("error = %v", err)
	}
}

func TestListDataCentersRejectsInvalidShape(t *testing.T) {
	client := mustClient(t, "test_key")
	_, err := client.ListDataCenters(t.Context(), &runpod.GPUAvailabilityFilter{GPUCount: -1})
	var validation *runpod.ValidationError
	if !errors.As(err, &validation) || validation.Field != "gpuCount" {
		t.Fatalf("error = %v", err)
	}
}
