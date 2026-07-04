package runpod_test

import (
	"testing"

	runpod "github.com/cozy-creator/runpod-go-sdk"
)

// mustClient constructs a client or fails the test.
func mustClient(t *testing.T, apiKey string, opts ...runpod.ClientOption) *runpod.Client {
	t.Helper()
	client, err := runpod.NewClient(apiKey, opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}
