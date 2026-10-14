package runpod_test

import (
	"context"
	"encoding/json"
	runpod "github.com/cozy-creator/runpod-go-sdk"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRenamePodOnlyPatchesNameOnRESTV2(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "PATCH" || r.URL.Path != "/v2/pods/pod-1" || r.Header.Get("Authorization") != "Bearer test_key" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 1 || body["name"] != "fidika/farnese" {
			t.Errorf("rename changed container fields: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"pod-1","name":"fidika/farnese"}`))
	}))
	defer server.Close()
	client := mustClient(t, "test_key", runpod.WithBaseURL("https://v1.invalid"), runpod.WithRESTV2BaseURL(server.URL+"/v2"))
	if err := client.RenamePod(context.Background(), "pod-1", "fidika/farnese"); err != nil {
		t.Fatal(err)
	}
	if err := client.RenamePod(context.Background(), "", "fidika/farnese"); err == nil {
		t.Fatal("empty pod ID accepted")
	}
	if err := client.RenamePod(context.Background(), "pod-1", "  "); err == nil {
		t.Fatal("empty name accepted")
	}
	if calls != 1 {
		t.Fatalf("made %d requests, want 1", calls)
	}
}

func TestRenamePodRequiresConfirmedName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"id":"pod-1","name":"old-name"}`)) }))
	defer server.Close()
	client := mustClient(t, "test_key", runpod.WithRESTV2BaseURL(server.URL))
	if err := client.RenamePod(context.Background(), "pod-1", "fidika/farnese"); err == nil {
		t.Fatal("unconfirmed name accepted")
	}
}
