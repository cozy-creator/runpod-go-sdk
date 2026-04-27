package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/cozy-creator/runpod-go-sdk"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal("❌ Failed to load .env")
	}

	apiKey := os.Getenv("RUNPOD_API_KEY")
	endpointID := os.Getenv("RUNPOD_ENDPOINT_ID")

	client := runpod.NewClient(apiKey,
		runpod.WithDebug(true),
		runpod.WithTimeout(60*time.Second),
	)

	ctx := context.Background()

	// Step 1: Prepare multiple prompts
	inputs := []interface{}{
		map[string]interface{}{"prompt": "Job 1 - Just wait"},
		map[string]interface{}{"prompt": "Job 2 - Please wait"},
		map[string]interface{}{"prompt": "Job 3 - This should be purged"},
	}

	fmt.Println("🚀 Submitting multiple jobs...")
	jobs, err := client.SubmitMultipleJobs(ctx, endpointID, inputs)
	if err != nil {
		log.Fatalf("❌ Failed to submit jobs: %v", err)
	}

	for i, job := range jobs {
		if job != nil {
			fmt.Printf("📦 Job %d submitted: %s\n", i+1, job.ID)
		} else {
			fmt.Printf("❌ Job %d submission failed or nil\n", i+1)
		}
	}

	// Step 2: Immediately purge the queue
	fmt.Println("🧹 Purging queued jobs immediately...")
	err = client.PurgeQueue(ctx, endpointID)
	if err != nil {
		log.Fatalf("❌ Failed to purge queue: %v", err)
	}
	fmt.Println("✅ Purge request sent.")
}
