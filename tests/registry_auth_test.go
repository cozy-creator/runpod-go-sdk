package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func newRegistryAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/containerregistryauth":
			w.Write([]byte(`[{"id":"auth1","name":"gen-orchestrator:docker.io:bob"}]`))
		case r.Method == "POST" && r.URL.Path == "/containerregistryauth":
			var req runpod.CreateContainerRegistryAuthRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if req.Username == "" || req.Password == "" {
				t.Fatalf("credentials not marshalled: %+v", req)
			}
			json.NewEncoder(w).Encode(runpod.ContainerRegistryAuth{ID: "auth2", Name: req.Name})
		case r.Method == "DELETE" && r.URL.Path == "/containerregistryauth/auth1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
}

func TestContainerRegistryAuthCRUD(t *testing.T) {
	server := newRegistryAuthServer(t)
	defer server.Close()

	client := mustClient(t, "test_key", runpod.WithBaseURL(server.URL))
	ctx := context.Background()

	auths, err := client.ListContainerRegistryAuths(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(auths) != 1 || auths[0].ID != "auth1" {
		t.Fatalf("unexpected list result %+v", auths)
	}

	created, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{
		Name: "gen-orchestrator:docker.io:alice", Username: "alice", Password: "tok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID != "auth2" {
		t.Fatalf("unexpected create result %+v", created)
	}

	if err := client.DeleteContainerRegistryAuth(ctx, "auth1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := client.CreateContainerRegistryAuth(ctx, &runpod.CreateContainerRegistryAuthRequest{Name: "x"}); err == nil {
		t.Fatal("missing credentials must fail validation")
	}
}

func TestIsRegistryAuthError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"registry auth message", fmt.Errorf("create pod: %w", runpod.NewAPIError(500, "invalid container registry auth id")), true},
		{"pull access denied", runpod.NewAPIError(500, "pull access denied for repository"), true},
		{"capacity error is not auth", &runpod.NoCapacityError{GPUTypeID: "A"}, false},
		{"sdk api-key auth error is not registry auth", runpod.NewAPIError(401, "invalid or expired API key"), false},
		{"unrelated", runpod.NewAPIError(500, "internal error"), false},
	}
	for _, tc := range cases {
		if got := runpod.IsRegistryAuthError(tc.err); got != tc.want {
			t.Errorf("%s: IsRegistryAuthError=%v want %v", tc.name, got, tc.want)
		}
	}
	// A NoCapacityError whose message happens to contain auth-ish words must
	// still classify as capacity, not registry auth.
	err := &runpod.NoCapacityError{GPUTypeID: "A", Cause: errors.New("unauthorized")}
	if runpod.IsRegistryAuthError(err) {
		t.Error("capacity errors must never classify as registry-auth errors")
	}
}

// TestContainerRegistryAuthsLive verifies the REST endpoint against the real API.
func TestContainerRegistryAuthsLive(t *testing.T) {
	apiKey := os.Getenv("RUNPOD_API_KEY")
	if apiKey == "" {
		t.Skip("live test: RUNPOD_API_KEY not set")
	}
	client := mustClient(t, apiKey)
	auths, err := client.ListContainerRegistryAuths(context.Background())
	if err != nil {
		t.Fatalf("live list container registry auths: %v", err)
	}
	for _, a := range auths {
		t.Logf("registry auth id=%s name=%s", a.ID, a.Name)
	}
}
