package runpod_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cozy-creator/runpod-go-sdk"
	"github.com/joho/godotenv"
)

// loadEnv gates live tests on credentials being present: a plain
// `go test ./...` without RUNPOD_API_KEY skips them instead of failing.
func loadEnv(t *testing.T) (string, string) {
	_ = godotenv.Load()
	apiKey := os.Getenv("RUNPOD_API_KEY")
	endpointID := os.Getenv("RUNPOD_ENDPOINT_ID")
	if apiKey == "" || endpointID == "" {
		t.Skip("live test: RUNPOD_API_KEY / RUNPOD_ENDPOINT_ID not set")
	}
	return apiKey, endpointID
}

func TestRunSyncLive(t *testing.T) {
	apiKey, endpointID := loadEnv(t)
	client := runpod.NewClient(apiKey)
	ctx := context.Background()

	job, err := client.RunSync(ctx, endpointID, map[string]interface{}{
		"prompt": "Tell me a joke about Go programmers.",
	})
	if err != nil {
		t.Fatalf("RunSync failed: %v", err)
	}
	t.Logf("🧠 Output: %v", job.Output)
}

func TestRunAsyncStatusCancelRetryLive(t *testing.T) {
	apiKey, endpointID := loadEnv(t)
	client := runpod.NewClient(apiKey)
	ctx := context.Background()

	job, err := client.RunAsync(ctx, endpointID, map[string]interface{}{
		"prompt": "Explain quantum physics like I'm five.",
	})
	if err != nil {
		t.Fatalf("RunAsync failed: %v", err)
	}
	t.Logf("🚀 Async job ID: %s", job.ID)

	time.Sleep(2 * time.Second) // give it time to queue

	// Get status
	status, err := client.GetJobStatus(ctx, endpointID, job.ID)
	if err != nil {
		t.Errorf("GetJobStatus failed: %v", err)
	} else {
		t.Logf("📦 Status: %s", status.Status)
	}

	// Cancel
	err = client.CancelJob(ctx, endpointID, job.ID)
	if err != nil {
		t.Logf("⚠️ Cancel failed (might be too late): %v", err)
	}

	// Retry
	retry, err := client.RetryJob(ctx, endpointID, job.ID)
	if err != nil {
		t.Logf("⚠️ Retry failed (expected if job not cancelled): %v", err)
	} else {
		t.Logf("🔁 Retry ID: %s", retry.ID)
	}
}

func TestGetHealthLive(t *testing.T) {
	apiKey, endpointID := loadEnv(t)
	client := runpod.NewClient(apiKey)
	ctx := context.Background()

	health, err := client.GetHealth(ctx, endpointID)
	if err != nil {
		t.Fatalf("GetHealth failed: %v", err)
	}
	t.Logf("🩺 Health: %+v", health)
}

func TestStreamAndWaitForJobCompletion(t *testing.T) {
	apiKey, endpointID := loadEnv(t)
	client := runpod.NewClient(apiKey)
	ctx := context.Background()

	job, err := client.RunAsync(ctx, endpointID, map[string]interface{}{
		"prompt": "Why do people love cats?",
	})
	if err != nil {
		t.Fatalf("RunAsync failed: %v", err)
	}
	t.Logf("🔁 Waiting for job %s to complete...", job.ID)

	final, err := client.WaitForJobCompletion(ctx, endpointID, job.ID, 60*time.Second)
	if err != nil {
		t.Fatalf("WaitForJobCompletion failed: %v", err)
	}
	t.Logf("✅ Final Output: %v", final.Output)
}

func TestSubmitMultipleAndPurgeLive(t *testing.T) {
	apiKey, endpointID := loadEnv(t)
	client := runpod.NewClient(apiKey)
	ctx := context.Background()

	inputs := []interface{}{
		map[string]interface{}{"prompt": "Fact about Nigeria"},
		map[string]interface{}{"prompt": "Fact about Kenya"},
		map[string]interface{}{"prompt": "Fact about Ghana"},
	}
	jobs, err := client.SubmitMultipleJobs(ctx, endpointID, inputs)
	if err != nil {
		t.Fatalf("SubmitMultipleJobs failed: %v", err)
	}
	t.Logf("🎯 Jobs submitted: %d", len(jobs))

	time.Sleep(3 * time.Second)
	err = client.PurgeQueue(ctx, endpointID)
	if err != nil {
		t.Errorf("PurgeQueue failed: %v", err)
	} else {
		t.Log("🧹 Queue purged")
	}
}

func TestQuickRunLive(t *testing.T) {
	apiKey, endpointID := loadEnv(t)
	client := runpod.NewClient(apiKey)
	ctx := context.Background()

	job, err := client.QuickRun(ctx, endpointID, map[string]interface{}{
		"prompt": "Give a summary of machine learning.",
	})
	if err != nil {
		t.Fatalf("QuickRun failed: %v", err)
	}
	t.Logf("🏃 Quick output: %v", job.Output)
}

func TestIsJobTerminalLive(t *testing.T) {
	client := runpod.NewClient("dummy")

	tests := map[string]bool{
		"COMPLETED":   true,
		"FAILED":      true,
		"CANCELLED":   true,
		"IN_QUEUE":    false,
		"IN_PROGRESS": false,
	}

	for status, want := range tests {
		got := client.IsJobTerminal(status)
		if got != want {
			t.Errorf("IsJobTerminal(%q) = %v, want %v", status, got, want)
		}
	}
}
