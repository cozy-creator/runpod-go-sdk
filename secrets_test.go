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

func newResourceClient(t *testing.T, handler http.HandlerFunc) *runpod.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test_key" {
			t.Errorf("missing bearer credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return mustClient(t, "test_key", runpod.WithBaseURL(server.URL), runpod.WithMaxRetryAttempts(0))
}
func TestCreateOrUpdateSecretCreatesOnlyAfterExplicitEmptyNameQuery(t *testing.T) {
	calls := 0
	client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if r.Method != "GET" || r.URL.Path != "/account/secrets" || r.URL.Query().Get("name") != "hf/token" {
				t.Errorf("lookup=%s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, `{"secrets":[]}`)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/account/secrets" {
			t.Errorf("create=%s %s", r.Method, r.URL)
		}
		var req runpod.CreateSecretRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Name != "hf/token" || req.Value != "new-value" {
			t.Errorf("request=%+v", req)
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"secret1","name":"hf/token","createdAt":"2026-10-28T12:00:00Z"}`)
	})
	if err := client.CreateOrUpdateSecret(context.Background(), "hf/token", "new-value"); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestCreateOrUpdateSecretPatchesImmutableID(t *testing.T) {
	calls := 0
	client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"secrets":[{"id":"secret/id","name":"HF-Token"}]}`)
			return
		}
		if r.Method != "PATCH" || r.URL.EscapedPath() != "/account/secrets/secret%2Fid" {
			t.Errorf("patch=%s %s", r.Method, r.URL.EscapedPath())
		}
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			t.Error(err)
		}
		if len(patch) != 1 || patch["value"] != "replacement" {
			t.Errorf("patch=%v", patch)
		}
		fmt.Fprint(w, `{"id":"secret/id","name":"HF-Token"}`)
	})
	if err := client.CreateOrUpdateSecret(context.Background(), "hf-token", "replacement"); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestSecretLookupFailuresNeverCreate(t *testing.T) {
	for _, scenario := range []struct {
		status int
		body   string
	}{{404, `{"title":"Not found","status":404,"detail":"missing route"}`}, {401, `{"title":"Unauthorized","status":401,"detail":"bad key"}`}, {403, `{"title":"Forbidden","status":403,"detail":"wrong scope"}`}, {500, `{"title":"Failure","status":500,"detail":"error"}`}, {200, `null`}, {200, `{}`}, {200, `{"secrets":null}`}, {200, `{"secrets":[null]}`}, {200, `{"secrets":[{}]}`}, {200, `{"secrets":[{"id":"s1","name":"other"}]}`}, {200, `{"secrets":[{"id":"s1","name":"token"},{"id":"s2","name":"token"}]}`}} {
		t.Run(fmt.Sprint(scenario.status)+scenario.body, func(t *testing.T) {
			calls := 0
			client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Errorf("lookup failure triggered %s", r.Method)
				}
				w.WriteHeader(scenario.status)
				fmt.Fprint(w, scenario.body)
			})
			if err := client.CreateOrUpdateSecret(context.Background(), "token", "value"); err == nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestSecretDescriptionOnlyPatchCanClearDescription(t *testing.T) {
	client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.Path != "/account/secrets/s1" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 1 || body["description"] != "" {
			t.Errorf("body=%v", body)
		}
		fmt.Fprint(w, `{"id":"s1","name":"token","description":""}`)
	})
	empty := ""
	secret, err := client.UpdateSecret(context.Background(), "s1", &runpod.UpdateSecretRequest{Description: &empty})
	if err != nil || secret.Description == nil || *secret.Description != "" {
		t.Fatalf("secret=%+v err=%v", secret, err)
	}
}
func TestSecretGetUsesIDAndPreservesNotFound(t *testing.T) {
	client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/account/secrets/s1" || len(r.URL.Query()) != 0 {
			t.Errorf("get=%s %s", r.Method, r.URL)
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"title":"Not found","status":404,"detail":"secret missing"}`)
	})
	if _, err := client.GetSecret(context.Background(), "s1"); !errors.Is(err, runpod.ErrNotFound) {
		t.Fatalf("get error=%v", err)
	}
}
func TestSecretResourcesRefuseEmptyAndForeignResponses(t *testing.T) {
	for _, method := range []string{"create", "get", "update"} {
		for _, body := range []string{"", `{}`, `null`, `{"id":"foreign","name":"other"}`} {
			t.Run(method+body, func(t *testing.T) {
				client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) {
					if body == "" {
						w.WriteHeader(204)
					} else {
						fmt.Fprint(w, body)
					}
				})
				var err error
				switch method {
				case "create":
					_, err = client.CreateSecret(context.Background(), &runpod.CreateSecretRequest{Name: "token", Value: "value"})
				case "get":
					_, err = client.GetSecret(context.Background(), "s1")
				case "update":
					_, err = client.UpdateSecret(context.Background(), "s1", &runpod.UpdateSecretRequest{Value: "value"})
				}
				if err == nil {
					t.Fatal("accepted invalid resource identity")
				}
			})
		}
	}
}
func TestSecretValidationDoesNotSendInvalidInput(t *testing.T) {
	client := newResourceClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("HTTP for invalid secret input") })
	for _, name := range []string{"", "RUNPOD_token", "runpod-token", "1bad", "has spaces"} {
		if _, err := client.CreateSecret(context.Background(), &runpod.CreateSecretRequest{Name: name, Value: "value"}); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	if _, err := client.UpdateSecret(context.Background(), "s1", &runpod.UpdateSecretRequest{}); err == nil {
		t.Fatal("accepted empty PATCH")
	}
}
func TestSecretListsRejectNullAndDuplicateRows(t *testing.T) {
	for _, body := range []string{`{}`, `{"secrets":null}`, `{"secrets":[null]}`, `{"secrets":[{"name":"token"}]}`, `{"secrets":[{"id":"s1"}]}`, `{"secrets":[{"id":"s1","name":"one"},{"id":"s1","name":"two"}]}`} {
		t.Run(body, func(t *testing.T) {
			client := newResourceClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			if rows, err := client.ListSecrets(context.Background()); err == nil || rows != nil {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
		})
	}
}
