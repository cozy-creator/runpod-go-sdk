package runpod_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func newRegistryAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/registries":
			w.Write([]byte(`{"registries":[{"id":"auth1","name":"gen-orchestrator:docker.io:bob"}]}`))
		case r.Method == "POST" && r.URL.Path == "/registries":
			var req runpod.CreateContainerRegistryAuthRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if req.Username == "" || req.Password == "" {
				t.Fatalf("credentials not marshalled: %+v", req)
			}
			json.NewEncoder(w).Encode(runpod.ContainerRegistryAuth{ID: "auth2", Name: req.Name})
		case r.Method == "GET" && r.URL.Path == "/registries/auth1":
			w.Write([]byte(`{"id":"auth1","name":"gen-orchestrator:docker.io:bob"}`))
		case r.Method == "DELETE" && r.URL.Path == "/registries/auth1":
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

	if got, err := client.GetContainerRegistryAuth(ctx, "auth1"); err != nil || got.ID != "auth1" {
		t.Fatalf("get registry: %+v %v", got, err)
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

func TestRegistryListsRefuseIncompleteRows(t *testing.T) {
	for _, body := range []string{`{}`, `{"registries":null}`, `{"registries":[null]}`, `{"registries":[{}]}`, `{"registries":[{"id":"r1"}]}`, `{"registries":[{"id":"r1","name":"one"},{"id":"r1","name":"two"}]}`} {
		t.Run(body, func(t *testing.T) {
			client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if rows, err := client.ListContainerRegistryAuths(context.Background()); err == nil || rows != nil {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
		})
	}
}
func TestRegistryGetAndCreateRefuseMissingOrForeignIdentities(t *testing.T) {
	for _, method := range []string{"get", "create"} {
		for _, body := range []string{"", `{}`, `null`, `{"id":"foreign","name":"foreign-name"}`} {
			t.Run(method+body, func(t *testing.T) {
				client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
					if body == "" {
						w.WriteHeader(204)
					} else {
						fmt.Fprint(w, body)
					}
				})
				var err error
				if method == "get" {
					_, err = client.GetContainerRegistryAuth(context.Background(), "r1")
				} else {
					_, err = client.CreateContainerRegistryAuth(context.Background(), &runpod.CreateContainerRegistryAuthRequest{Name: "one", Username: "user", Password: "pass"})
				}
				if err == nil {
					t.Fatal("accepted missing or foreign identity")
				}
			})
		}
	}
}
