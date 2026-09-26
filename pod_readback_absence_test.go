package runpod_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

func TestPodReadbackAbsenceAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, rest, graph       string
		restStatus, graphStatus int
		want                    error
		stage                   runpod.PodReadbackStage
	}{
		{"REST404", `{"error":"pod not found"}`, "", 404, 200, runpod.ErrNotFound, runpod.PodReadbackStageREST},
		{"GraphQLnull", `{"id":"pod-1","desiredStatus":"RUNNING"}`, `{"data":{"pod":null}}`, 200, 200, runpod.ErrIncompleteReadback, runpod.PodReadbackStageGraphQL},
		{"GraphQL404", `{"id":"pod-1","desiredStatus":"RUNNING"}`, `{"error":"projection missing"}`, 200, 404, runpod.ErrIncompleteReadback, runpod.PodReadbackStageGraphQL},
		{"GraphQLunauthorized", `{"id":"pod-1","desiredStatus":"RUNNING"}`, `{"error":"unauthorized"}`, 200, 403, runpod.ErrUnauthorized, runpod.PodReadbackStageGraphQL},
		{"GraphQLmalformed", `{"id":"pod-1","desiredStatus":"RUNNING"}`, `{`, 200, 200, nil, runpod.PodReadbackStageGraphQL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					w.WriteHeader(tc.graphStatus)
					_, _ = w.Write([]byte(tc.graph))
					return
				}
				w.WriteHeader(tc.restStatus)
				_, _ = w.Write([]byte(tc.rest))
			}))
			defer server.Close()
			client, err := runpod.NewClient("fixture", runpod.WithBaseURL(server.URL), runpod.WithGraphQLBaseURL(server.URL), runpod.WithMaxRetryAttempts(1))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetPodReadback(t.Context(), "pod-1", nil)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("classification: %v want %v", err, tc.want)
			}
			if tc.want != runpod.ErrNotFound && errors.Is(err, runpod.ErrNotFound) {
				t.Fatalf("supplemental failure claimed missing pod: %v", err)
			}
			var typed *runpod.PodReadbackError
			if !errors.As(err, &typed) || typed.Stage != tc.stage {
				t.Fatalf("stage diagnostics lost: %v", err)
			}
			if tc.stage == runpod.PodReadbackStageGraphQL && (typed.Partial == nil || typed.Partial.REST == nil || typed.Partial.REST.ID != "pod-1" || typed.Partial.REST.DesiredStatus != "RUNNING") {
				t.Fatalf("primary observation lost: %+v", typed.Partial)
			}
			if tc.want == runpod.ErrIncompleteReadback && !errors.Is(typed.Cause, runpod.ErrNotFound) {
				t.Fatalf("original provider diagnosis lost: %v", typed.Cause)
			}
		})
	}
}
